"""الخادم الوسيط الهجيني للترجمات: Tier1 + Tier2 + ترجمة عربية آلية عبر Gemini/Groq."""
from __future__ import annotations

import asyncio
import hashlib
import json
import logging
import time
from collections import defaultdict, deque
from contextlib import asynccontextmanager

import httpx
from fastapi import FastAPI, HTTPException, Request
from fastapi.responses import JSONResponse, Response

from . import ai, ranking, textfix, tmdb
from .config import Settings
from .db import Db
from .keypool import KeyPool, NoKeyAvailable, ProviderHTTPError
from .models import Candidate
from .parser import parse_filename
from .providers import opensubtitles as osub
from .providers import subdl, wyzie, yify

log = logging.getLogger("subs")
_LANG_NAMES = {"ar": "arabic", "en": "english", "fr": "french", "es": "spanish", "tr": "turkish", "fa": "persian"}


def create_app(settings: Settings | None = None, transport: httpx.AsyncBaseTransport | None = None) -> FastAPI:
    st = settings or Settings.from_env()
    db = Db(st.db_path)
    pools = {
        "wyzie": KeyPool("wyzie", st.wyzie_keys, st.wyzie_daily_limit, db),
        "osub": KeyPool("opensubtitles", [osub.pack_account(a) for a in st.os_accounts], st.os_daily_limit, db),
        "subdl": KeyPool("subdl", st.subdl_keys, st.subdl_daily_limit, db),
        "fc": KeyPool("firecrawl", st.firecrawl_keys, 500, db),
        "gemini": KeyPool("gemini", st.gemini_keys, st.gemini_daily_limit, db),
        "groq": KeyPool("groq", st.groq_keys, st.groq_daily_limit, db),
    }
    hits: dict[str, deque] = defaultdict(deque)

    # قفل يمنع أكثر من ترجمة واحدة في نفس الوقت (حسب الخطة)
    translate_lock = asyncio.Lock()
    # منع تكرار طلبات الفيديو نفسه أثناء المعالجة
    in_flight: dict[str, asyncio.Future] = {}

    ka = {"enabled": bool(st.keep_alive_url), "last_ok": None, "last_error": None, "pings": 0}

    async def keep_alive_loop(http: httpx.AsyncClient) -> None:
        url = f"{st.keep_alive_url}/health"
        await asyncio.sleep(30)
        while True:
            try:
                r = await http.get(url, timeout=15)
                ka["pings"] += 1
                if r.status_code < 500:
                    ka["last_ok"], ka["last_error"] = time.time(), None
                else:
                    ka["last_error"] = f"http {r.status_code}"
            except asyncio.CancelledError:
                raise
            except Exception as e:
                ka["last_error"] = type(e).__name__
                log.warning("keep-alive ping failed: %s", e)
            await asyncio.sleep(st.keep_alive_interval)

    @asynccontextmanager
    async def lifespan(app: FastAPI):
        app.state.http = httpx.AsyncClient(
            timeout=90,  # أطول بسبب الترجمة
            transport=transport,
            headers={"User-Agent": st.user_agent},
        )
        task = asyncio.create_task(keep_alive_loop(app.state.http)) if st.keep_alive_url else None
        yield
        if task:
            task.cancel()
            await asyncio.gather(task, return_exceptions=True)
        await app.state.http.aclose()

    app = FastAPI(title="Subtitle aggregator", lifespan=lifespan)
    app.state.settings, app.state.db, app.state.pools = st, db, pools

    @app.middleware("http")
    async def guard(request: Request, call_next):
        if request.url.path != "/health":
            if st.app_token and request.headers.get("X-App-Token") != st.app_token:
                return JSONResponse({"error": "unauthorized"}, status_code=401)
            ip = (request.headers.get("X-Forwarded-For", "").split(",")[0].strip()
                  or (request.client.host if request.client else "?"))
            now = time.time()
            q = hits[ip]
            while q and q[0] < now - 60:
                q.popleft()
            if len(q) >= st.rate_limit_per_min:
                return JSONResponse({"error": "rate_limited"}, status_code=429)
            q.append(now)
        return await call_next(request)

    @app.get("/health")
    async def health():
        return {
            "ok": True,
            "tmdb": bool(st.tmdb_key),
            "scraping": st.enable_scraping,
            "ai_translate": st.enable_ai_translate and bool(pools["gemini"] or pools["groq"]),
            "ai_title_clean": st.enable_ai_title_clean and bool(pools["gemini"] or pools["groq"]),
            "keep_alive": ka,
            "pools": {k: v.status() for k, v in pools.items()},
            "ai_models": ai.models_status(),
        }

    @app.get("/ai/selftest")
    async def ai_selftest(request: Request):
        """فحص حيّ لـ Gemini وGroq (محمي بـ X-App-Token). يستهلك طلباً صغيراً لكل مزوّد."""
        return await ai.selftest(
            request.app.state.http,
            pools["gemini"] if pools["gemini"] else None,
            pools["groq"] if pools["groq"] else None,
        )

    # ————— أدوات مساعدة —————

    def _store(c: Candidate) -> str:
        uid = hashlib.sha1(f"{c.provider}|{c.ref}".encode()).hexdigest()[:16]
        db.execute(
            "INSERT INTO subtitle_files(uid,provider,ref,lang,release,rating,downloads,fmt,hi,hash_match,created_at) "
            "VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(uid) DO UPDATE SET lang=excluded.lang, "
            "release=excluded.release, rating=excluded.rating, downloads=excluded.downloads, "
            "fmt=excluded.fmt, hi=excluded.hi, hash_match=excluded.hash_match",
            (uid, c.provider, c.ref, c.lang, c.release, c.rating, c.downloads, c.fmt, int(c.hi),
             int(c.hash_match), time.time()),
        )
        return uid

    def _from_row(r) -> Candidate:
        return Candidate(
            provider=r["provider"], ref=r["ref"], lang=r["lang"] or "", release=r["release"] or "",
            rating=r["rating"], downloads=r["downloads"], fmt=r["fmt"] or "srt",
            hi=bool(r["hi"]), hash_match=bool(r["hash_match"]),
        )

    async def _tier1(http, ids, parsed, langs, moviehash) -> list[Candidate]:
        jobs = []
        if pools["wyzie"] and (ids.get("tmdb_id") or ids.get("imdb_id")):
            for lang in langs:
                jobs.append(pools["wyzie"].call(lambda k, lang=lang: wyzie.search(
                    http, k, tmdb_id=ids.get("tmdb_id"), imdb_id=ids.get("imdb_id"),
                    season=parsed["season"], episode=parsed["episode"], lang=lang)))
        if pools["osub"]:
            query = parsed["title"] + (f" {parsed['year']}" if parsed.get("year") and not parsed["season"] else "")
            jobs.append(pools["osub"].call(lambda k: osub.search(
                http, k, st.user_agent, imdb_id=ids.get("imdb_id"), tmdb_id=ids.get("tmdb_id"), query=query,
                season=parsed["season"], episode=parsed["episode"], lang=",".join(langs),
                moviehash=moviehash), count_use=False))
        out: list[Candidate] = []
        for res in await asyncio.gather(*jobs, return_exceptions=True):
            if isinstance(res, Exception):
                log.warning("tier1 error: %s", res)
            else:
                out += res
        return out

    async def _tier2(http, ids, parsed, langs) -> list[Candidate]:
        jobs = []
        if pools["subdl"]:
            jobs.append(pools["subdl"].call(lambda k: subdl.search(
                http, k, imdb_id=ids.get("imdb_id"), tmdb_id=ids.get("tmdb_id"), query=parsed["title"],
                season=parsed["season"], episode=parsed["episode"], lang=",".join(langs))))
        if st.enable_scraping and not parsed["season"]:
            names = {_LANG_NAMES.get(x, x) for x in langs}
            jobs.append(yify.search(http, pools["fc"] if pools["fc"] else None, ids.get("imdb_id"), names))
        out: list[Candidate] = []
        for res in await asyncio.gather(*jobs, return_exceptions=True):
            if isinstance(res, Exception):
                log.warning("tier2 error: %s", res)
            else:
                out += res
        return out

    async def _download_candidate(http, c: Candidate) -> tuple[bytes, str]:
        """ينزّل محتوى الترجمة الخام."""
        p, ref = c.provider, c.ref
        if p == "wyzie":
            data = await wyzie.download(http, ref)
        elif p == "opensubtitles":
            data = await pools["osub"].call(lambda k: osub.download(http, k, st.user_agent, ref))
        elif p == "subdl":
            data = await subdl.download(http, ref)
        elif p == "yify":
            data = await yify.download(http, ref)
        else:
            raise ValueError(f"unknown provider {p}")
        blob, fmt, _enc = textfix.normalize_subtitle(data, c.lang or "en")
        return blob, fmt

    async def _try_ai_translate(http, ranked: list, parsed: dict) -> dict | None:
        """
        يختار أول نسخة غير عربية حسب الأولوية، يترجمها، ويعيد عنصر نتيجة AI.
        يعيد None عند الفشل (حسب الخطة: لا نرسل ملفاً ناقصاً).
        """
        if not st.enable_ai_translate:
            return None
        if not (pools["gemini"] or pools["groq"]):
            return None

        # هل توجد عربية جيدة أصلاً؟
        has_ar = any((c.lang or "").lower().startswith("ar") for c, _ in ranked[:10])
        if has_ar:
            # نترجم فقط إذا لم تكن هناك عربية — حسب الخطة نضيف AI حتى مع وجود عربية؟
            # القرار النهائي: نترجم أول نسخة EN/FR... ونضعها في الأعلى كخيار إضافي
            pass

        source = ai.pick_source_candidate([c for c, _ in ranked])
        if source is None:
            return None

        async with translate_lock:
            try:
                raw_blob, fmt = await _download_candidate(http, source)
                text = raw_blob.decode("utf-8", errors="replace")
                if fmt not in ("srt", "vtt"):
                    # نحاول معاملة VTT كـ SRT بسيط
                    pass

                translated = await ai.translate_srt_to_arabic(
                    http,
                    pools["gemini"] if pools["gemini"] else None,
                    pools["groq"] if pools["groq"] else None,
                    text,
                    title=parsed.get("title") or "",
                )
                if not translated:
                    return None

                # تخزين الترجمة الآلية كملف جديد
                ai_ref = f"ai:{source.provider}:{source.ref}"
                uid = hashlib.sha1(ai_ref.encode()).hexdigest()[:16]
                title = parsed.get("title") or "Unknown"
                release = f"{title} — AR (AI)"
                db.execute(
                    "INSERT INTO subtitle_files(uid,provider,ref,lang,release,rating,downloads,fmt,hi,hash_match,blob,size,encoding,created_at) "
                    "VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(uid) DO UPDATE SET blob=excluded.blob, size=excluded.size",
                    (uid, "ai", ai_ref, "ar", release, None, None, "srt", 0, 0,
                     translated.encode("utf-8"), len(translated.encode("utf-8")), "utf-8", time.time()),
                )
                return {
                    "id": uid,
                    "source": "ai",
                    "lang": "ar",
                    "release": release,
                    "match": 100,
                    "rating": None,
                    "downloads": None,
                    "format": "srt",
                    "hi": False,
                    "ai": True,
                    "from_lang": (source.lang or "")[:2],
                }
            except Exception as e:
                log.warning("AI translate failed: %s", e)
                return None

    # ————— البحث —————

    @app.get("/search")
    async def search(
        request: Request,
        filename: str = "",
        q: str = "",
        imdb_id: str = "",
        tmdb_id: int = 0,
        season: int = 0,
        episode: int = 0,
        lang: str = "ar",
        hash: str = "",
        refresh: bool = False,
        translate: bool = True,
    ):
        http: httpx.AsyncClient = request.app.state.http
        langs = [x for x in dict.fromkeys(l.strip().lower() for l in lang.split(",")) if x] or ["ar"]
        if not (filename or q or imdb_id or tmdb_id):
            raise HTTPException(400, "filename or q required")

        raw_input = filename or q
        parsed = parse_filename(raw_input)
        if q and filename:
            m = parse_filename(q)
            parsed.update(
                title=m["title"] or parsed["title"],
                year=m["year"],
                season=m["season"],
                episode=m["episode"],
            )
        if season and episode:
            parsed["season"], parsed["episode"] = season, episode

        ids: dict = {"imdb_id": imdb_id or None, "tmdb_id": tmdb_id or None}

        # مفتاح منع التكرار
        flight_key = hashlib.sha1(
            json.dumps([ids, parsed["title"].lower(), parsed["year"], parsed["season"],
                        parsed["episode"], langs, hash, translate], ensure_ascii=False).encode()
        ).hexdigest()

        # إذا كان نفس الطلب قيد المعالجة → انتظر نتيجته
        if flight_key in in_flight:
            return await in_flight[flight_key]

        loop = asyncio.get_event_loop()
        fut: asyncio.Future = loop.create_future()
        in_flight[flight_key] = fut

        try:
            result = await _do_search(
                http, parsed, ids, langs, hash, refresh, translate, raw_input
            )
            fut.set_result(result)
            return result
        except Exception as e:
            fut.set_exception(e)
            raise
        finally:
            in_flight.pop(flight_key, None)

    async def _do_search(http, parsed, ids, langs, hash, refresh, translate, raw_input):
        ckey = hashlib.sha1(json.dumps(
            [ids, parsed["title"].lower(), parsed["year"], parsed["season"], parsed["episode"], langs, hash],
            ensure_ascii=False).encode()).hexdigest()

        cached = None if refresh else db.one(
            "SELECT payload FROM search_cache WHERE query_hash=? AND expires_at>?", (ckey, time.time()))
        tier = 1
        resolved = None
        uids: list[str]
        ai_cleaned = False

        if cached:
            payload = json.loads(cached["payload"])
            uids, resolved, tier = payload["uids"], payload.get("resolved"), payload.get("tier", 1)
        else:
            if not (ids["imdb_id"] or ids["tmdb_id"]):
                resolved = await tmdb.resolve(http, db, st.tmdb_key, parsed)
                if resolved:
                    ids = {"imdb_id": resolved.get("imdb_id"), "tmdb_id": resolved.get("tmdb_id")}

            # إذا فشل التعرف → تنظيف العنوان بـ Gemini
            if not (ids.get("imdb_id") or ids.get("tmdb_id")) and st.enable_ai_title_clean:
                cleaned = await ai.clean_title(
                    http,
                    pools["gemini"] if pools["gemini"] else None,
                    pools["groq"] if pools["groq"] else None,
                    raw_input,
                )
                if cleaned and cleaned.get("title"):
                    ai_cleaned = True
                    parsed["title"] = cleaned["title"]
                    if cleaned.get("year"):
                        parsed["year"] = cleaned["year"]
                    if cleaned.get("season"):
                        parsed["season"] = cleaned["season"]
                    if cleaned.get("episode"):
                        parsed["episode"] = cleaned["episode"]
                    resolved = await tmdb.resolve(http, db, st.tmdb_key, parsed)
                    if resolved:
                        ids = {"imdb_id": resolved.get("imdb_id"), "tmdb_id": resolved.get("tmdb_id")}

            cands = await _tier1(http, ids, parsed, langs, hash or None)
            want_ar = langs[0] if langs else "ar"
            if not any(c.lang == want_ar for c in cands):
                # نبحث أيضاً بالإنجليزية وبقية اللغات لتمكين الترجمة لاحقاً
                extra_langs = list(dict.fromkeys(langs + ["en", "fr", "es", "pt", "ru", "tr"]))
                tier = 2
                cands += await _tier2(http, ids, parsed, extra_langs)
                # إعادة tier1 بلغات إضافية إن لزم
                more = await _tier1(http, ids, parsed, [l for l in extra_langs if l not in langs], hash or None)
                cands += more

            seen, uniq = set(), []
            for c in cands:
                if (c.provider, c.ref) not in seen:
                    seen.add((c.provider, c.ref))
                    uniq.append(c)
            uids = [_store(c) for c in uniq]
            if uids:
                expires = time.time() + st.search_cache_hours * 3600
                db.execute(
                    "INSERT OR REPLACE INTO search_cache(query_hash,payload,created_at,expires_at) VALUES(?,?,?,?)",
                    (ckey, json.dumps({"uids": uids, "resolved": resolved, "tier": tier}, ensure_ascii=False),
                     time.time(), expires),
                )

        rows = [r for r in (db.one("SELECT * FROM subtitle_files WHERE uid=?", (u,)) for u in uids) if r]
        ranked = ranking.rank([_from_row(r) for r in rows], parsed, langs)
        uid_of = {(r["provider"], r["ref"]): r["uid"] for r in rows}

        results = [{
            "id": uid_of[(c.provider, c.ref)],
            "source": c.provider,
            "lang": c.lang,
            "release": c.release,
            "match": sc,
            "rating": c.rating,
            "downloads": c.downloads,
            "format": c.fmt,
            "hi": c.hi,
        } for c, sc in ranked[:30]]

        # ── الترجمة الآلية (ننتظر اكتمالها ثم نرسل قائمة واحدة) ──
        ai_result = None
        if translate and st.enable_ai_translate:
            ai_result = await _try_ai_translate(http, ranked, parsed)

        if ai_result:
            results.insert(0, ai_result)

        return {
            "query": {
                "title": parsed["title"],
                "year": parsed["year"],
                "season": parsed["season"],
                "episode": parsed["episode"],
            },
            "resolved": resolved,
            "cached": bool(cached),
            "tier": tier,
            "ai_cleaned": ai_cleaned,
            "ai_translated": bool(ai_result),
            "results": results,
        }

    # ————— الملف —————

    @app.get("/file/{uid}")
    async def file(uid: str, request: Request):
        http: httpx.AsyncClient = request.app.state.http
        row = db.one("SELECT * FROM subtitle_files WHERE uid=?", (uid,))
        if not row:
            raise HTTPException(404, "unknown id")
        blob, fmt = row["blob"], row["fmt"] or "srt"
        if blob is None:
            try:
                p, ref = row["provider"], row["ref"]
                if p == "ai":
                    raise HTTPException(404, "ai subtitle missing blob")
                if p == "wyzie":
                    data = await wyzie.download(http, ref)
                elif p == "opensubtitles":
                    data = await pools["osub"].call(lambda k: osub.download(http, k, st.user_agent, ref))
                elif p == "subdl":
                    data = await subdl.download(http, ref)
                elif p == "yify":
                    data = await yify.download(http, ref)
                else:
                    raise HTTPException(400, "bad provider")
                blob, fmt, enc = textfix.normalize_subtitle(data, row["lang"] or "ar")
            except NoKeyAvailable:
                raise HTTPException(503, "no key available (daily limits reached)")
            except ProviderHTTPError as e:
                raise HTTPException(502, f"provider error {e.status}")
            except (ValueError, KeyError) as e:
                raise HTTPException(502, f"bad subtitle: {e}")
            db.execute(
                "UPDATE subtitle_files SET blob=?, size=?, encoding=?, fmt=? WHERE uid=?",
                (blob, len(blob), enc, fmt, uid),
            )
        return Response(
            content=blob,
            media_type="text/plain; charset=utf-8",
            headers={"X-Subtitle-Format": fmt, "X-Subtitle-Lang": row["lang"] or ""},
        )

    return app
