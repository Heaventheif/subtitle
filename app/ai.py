"""خدمات الذكاء الاصطناعي: تنظيف العنوان + ترجمة الترجمات إلى العربية.

نظام ذاتي الإصلاح:
  • يكتشف النماذج المتاحة فعلاً من واجهة Gemini وGroq ويختار الأفضل تلقائياً
    (لا اعتماد على أسماء ثابتة قد تُوقف).
  • عند 404 يُعلّم النموذج «ميتاً» ويجرّب التالي ويعيد الاكتشاف.
  • يتراجع تلقائياً عن الإعدادات غير المدعومة (JSON mode / reasoning_effort).
  • الترجمة تعتمد على ترقيم كل سطر (i) فلا تنهار عند اختلاف العدد.
"""
from __future__ import annotations

import asyncio
import json
import logging
import os
import re
import time
from typing import Any, Awaitable, Callable

import httpx

from .keypool import KeyPool, NoKeyAvailable, ProviderHTTPError

log = logging.getLogger("subs.ai")

# أولوية اللغات المصدر عند غياب العربية
SOURCE_LANG_PRIORITY = ["en", "fr", "es", "pt", "ru", "tr", "de", "it"]

BATCH_SIZE = 40          # عدد المقاطع في كل طلب ترجمة
CONCURRENCY = 3          # دفعات متوازية

GEMINI_URL = "https://generativelanguage.googleapis.com/v1beta/models/{model}:generateContent"
GEMINI_LIST_URL = "https://generativelanguage.googleapis.com/v1beta/models"
GROQ_URL = "https://api.groq.com/openai/v1/chat/completions"
GROQ_LIST_URL = "https://api.groq.com/openai/v1/models"

_DISCOVERY_TTL = 6 * 3600      # إعادة اكتشاف النماذج كل 6 ساعات
_DISCOVERY_RETRY = 300         # عند فشل الاكتشاف نعيد بعد 5 دقائق
_DEAD_TTL = 3600               # النموذج الميت يُتجاهل ساعة


# ───────────────── سجل النماذج (اكتشاف تلقائي) ─────────────────

def _ver(txt: str) -> tuple[int, ...]:
    return tuple(int(x) for x in re.findall(r"\d+", txt)[:3]) or (0,)


def _neg(v: tuple[int, ...]) -> tuple[int, ...]:
    return tuple(-x for x in v)


def rank_gemini(models: list[dict]) -> list[str]:
    """يرتّب نماذج Gemini: flash المستقر الأحدث أولاً، ثم lite، ثم preview."""
    scored = []
    for m in models:
        name = (m.get("name") or "").replace("models/", "")
        methods = m.get("supportedGenerationMethods") or []
        if methods and "generateContent" not in methods:
            continue
        mm = re.fullmatch(
            r"gemini-(\d+(?:\.\d+)?)-flash(-lite)?(-preview[\w.-]*|-exp[\w.-]*|-latest|-\d{2,3})?", name)
        if not mm:
            continue
        suffix = mm.group(3) or ""
        preview = "preview" in suffix or "exp" in suffix
        scored.append((bool(mm.group(2)), preview, _neg(_ver(mm.group(1))), bool(suffix), name))
    scored.sort()
    return [x[-1] for x in scored]


_GROQ_EXCLUDE = re.compile(r"whisper|tts|orpheus|guard|safeguard|embed|compound|playai|moderation", re.I)


def rank_groq(models: list[dict]) -> list[str]:
    """يرتّب نماذج Groq النصية: gpt-oss-120b ثم qwen الأحدث ثم llama 70b ..."""
    scored = []
    for m in models:
        mid = m.get("id") or ""
        if not mid or _GROQ_EXCLUDE.search(mid) or m.get("active") is False:
            continue
        low = mid.lower()
        if "gpt-oss-120b" in low:
            tier, v = 0, (0,)
        elif (q := re.search(r"qwen[-\d.]*?(\d+(?:\.\d+)?)-?(\d+)b", low)) or "qwen" in low:
            tier, v = 1, _neg(_ver(q.group(1))) if q else (0,)
        elif "llama-3.3-70b" in low:
            tier, v = 2, (0,)
        elif "gpt-oss-20b" in low:
            tier, v = 3, (0,)
        elif "llama-4" in low or "llama4" in low:
            tier, v = 4, (0,)
        elif "llama" in low or "versatile" in low or "instruct" in low:
            tier, v = 5, (0,)
        else:
            continue
        scored.append((tier, v, mid))
    scored.sort()
    return [x[-1] for x in scored]


class ModelRegistry:
    """يحتفظ بقائمة النماذج الصالحة لمزوّد واحد ويعيد اكتشافها عند الحاجة."""

    def __init__(self, name: str, env_var: str, static: list[str]):
        self.name = name
        self.override = [m.strip() for m in os.getenv(env_var, "").split(",") if m.strip()]
        self.static = static
        self.discovered: list[str] = []
        self.discovered_at = 0.0
        self.refresh_after = 0.0
        self.dead: dict[str, float] = {}
        self.last_ok: str | None = None
        self.last_error = ""
        self.blocked_until = 0.0      # قاطع دائرة: عند فشل كل النماذج نتوقف دقيقتين
        self._lock = asyncio.Lock()

    def ordered(self) -> list[str]:
        now = time.time()
        out: list[str] = []
        for m in self.override + self.discovered + ([self.last_ok] if self.last_ok else []) + self.static:
            if m and m not in out and self.dead.get(m, 0) < now:
                out.append(m)
        return out

    def mark_dead(self, model: str, err: str = "") -> None:
        self.dead[model] = time.time() + _DEAD_TTL
        self.last_error = err[:200]
        self.refresh_after = 0.0          # أعد الاكتشاف في أقرب فرصة
        log.warning("%s model %s marked unavailable", self.name, model)

    def mark_ok(self, model: str) -> None:
        self.last_ok, self.last_error = model, ""
        self.dead.pop(model, None)

    async def ensure(self, discover: Callable[[], Awaitable[list[str]]], force: bool = False) -> None:
        if not force and time.time() < self.refresh_after:
            return
        async with self._lock:
            if not force and time.time() < self.refresh_after:
                return
            try:
                found = await discover()
                if found:
                    self.discovered = found
                    self.discovered_at = time.time()
                    # الاكتشاف الناجح يعني أن ما ظهر فيه صالح → ارفع علامة «ميت» عنه
                    for m in found:
                        self.dead.pop(m, None)
                    self.refresh_after = time.time() + _DISCOVERY_TTL
                    log.info("%s models discovered: %s", self.name, found[:5])
                else:
                    self.refresh_after = time.time() + _DISCOVERY_RETRY
            except Exception as e:  # الاكتشاف تحسين فقط، لا يجب أن يكسر الطلب
                self.refresh_after = time.time() + _DISCOVERY_RETRY
                log.warning("%s model discovery failed: %s", self.name, e)

    def status(self) -> dict:
        return {
            "active": (self.ordered() or [None])[0],
            "last_ok": self.last_ok,
            "discovered": self.discovered[:6],
            "dead": [m for m, t in self.dead.items() if t > time.time()],
            "last_error": self.last_error,
            "cooling_down": self.blocked_until > time.time(),
        }


GEMINI = ModelRegistry("gemini", "GEMINI_MODEL", ["gemini-3.8-flash", "gemini-2.5-flash"])
GROQ = ModelRegistry("groq", "GROQ_MODEL", ["openai/gpt-oss-120b", "qwen/qwen3.6-27b"])


def models_status() -> dict:
    return {"gemini": GEMINI.status(), "groq": GROQ.status()}


# ───────────────── مساعدات عامة ─────────────────

def _extract_json(text: str) -> Any:
    """يستخرج JSON من رد قد يحتوي markdown أو نصاً قبل/بعد الـ JSON."""
    text = (text or "").strip()
    text = re.sub(r"^```(?:json)?\s*|\s*```$", "", text)
    try:
        return json.loads(text)
    except json.JSONDecodeError:
        pass
    starts = [i for i in (text.find("{"), text.find("[")) if i >= 0]
    if not starts:
        raise ValueError("no JSON in AI response")
    obj, _ = json.JSONDecoder().raw_decode(text[min(starts):])
    return obj


def _http_error(r: httpx.Response) -> ProviderHTTPError:
    body = r.text[:300]
    # Gemini يعيد 400 لمفتاح غير صالح → عاملها كـ 401 ليدور المفتاح
    if r.status_code == 400 and ("API_KEY_INVALID" in r.text or "API key not valid" in r.text):
        return ProviderHTTPError(401, body)
    ra = r.headers.get("Retry-After")
    return ProviderHTTPError(r.status_code, body, float(ra) if ra and ra.replace(".", "", 1).isdigit() else None)


async def _run_models(
    reg: ModelRegistry,
    discover: Callable[[], Awaitable[list[str]]],
    attempt: Callable[[str], Awaitable[str]],
) -> str:
    """يجرّب النماذج بالترتيب؛ 404 → ميت؛ 5xx/400 → التالي؛ 401/402/403/429 → للـ KeyPool."""
    if time.time() < reg.blocked_until:
        raise ProviderHTTPError(503, f"{reg.name}: all models unavailable (cooling down)")
    await reg.ensure(discover)
    errors: list[str] = []
    last: ProviderHTTPError | None = None
    for round_no in range(2):
        for model in reg.ordered()[:5]:
            try:
                text = await attempt(model)
                reg.mark_ok(model)
                return text
            except ProviderHTTPError as e:
                if e.status in (401, 402, 403, 429):
                    raise
                last = e
                errors.append(f"{model}: {e}")
                if e.status == 404:
                    reg.mark_dead(model, str(e))
        if round_no == 0:
            await reg.ensure(discover, force=True)   # أعد الاكتشاف مرة واحدة ثم جرّب
            if not reg.ordered():
                break
    if last is not None and last.status == 404:
        reg.blocked_until = time.time() + 120      # كل النماذج 404 → لا نعيد الطرق فوراً
    raise ProviderHTTPError(last.status if last else 404, " | ".join(errors)[:400] or "no model available")


# ───────────────── Gemini ─────────────────

async def _gemini_one(http: httpx.AsyncClient, key: str, model: str, prompt: str, temperature: float) -> str:
    body = {
        "contents": [{"parts": [{"text": prompt}]}],
        "generationConfig": {"temperature": temperature, "responseMimeType": "application/json"},
    }
    url = GEMINI_URL.format(model=model)
    r = await http.post(url, params={"key": key}, json=body, timeout=90)
    if r.status_code == 400 and _http_error(r).status != 401:
        # قد لا يدعم النموذج بعض الإعدادات → أعد بلا responseMimeType
        body["generationConfig"] = {"temperature": temperature}
        r = await http.post(url, params={"key": key}, json=body, timeout=90)
    if r.status_code >= 400:
        raise _http_error(r)
    try:
        parts = r.json()["candidates"][0]["content"]["parts"]
        return "".join(p.get("text", "") for p in parts if not p.get("thought"))
    except (KeyError, IndexError, TypeError) as e:
        raise ProviderHTTPError(502, f"bad gemini response: {e}")


async def _gemini_generate(http: httpx.AsyncClient, key: str, prompt: str, temperature: float = 0.2) -> str:
    async def discover() -> list[str]:
        r = await http.get(GEMINI_LIST_URL, params={"key": key, "pageSize": 200}, timeout=20)
        if r.status_code >= 400:
            raise _http_error(r)
        return rank_gemini(r.json().get("models") or [])

    return await _run_models(GEMINI, discover, lambda m: _gemini_one(http, key, m, prompt, temperature))


# ───────────────── Groq ─────────────────

async def _groq_one(http: httpx.AsyncClient, key: str, model: str, prompt: str, temperature: float) -> str:
    body: dict = {
        "model": model,
        "messages": [{"role": "user", "content": prompt}],
        "temperature": temperature,
        "response_format": {"type": "json_object"},
    }
    if "gpt-oss" in model.lower():
        body["reasoning_effort"] = "low"      # نموذج تفكير: أسرع وأقل توكنات
    headers = {"Authorization": f"Bearer {key}", "Content-Type": "application/json"}
    r = await http.post(GROQ_URL, headers=headers, json=body, timeout=90)
    if r.status_code == 400:
        # تراجع عن الإعدادات الاختيارية (JSON mode / reasoning_effort) وأعد المحاولة
        body.pop("response_format", None)
        body.pop("reasoning_effort", None)
        r = await http.post(GROQ_URL, headers=headers, json=body, timeout=90)
    if r.status_code >= 400:
        raise _http_error(r)
    try:
        return r.json()["choices"][0]["message"]["content"] or ""
    except (KeyError, IndexError, TypeError) as e:
        raise ProviderHTTPError(502, f"bad groq response: {e}")


async def _groq_generate(http: httpx.AsyncClient, key: str, prompt: str, temperature: float = 0.2) -> str:
    async def discover() -> list[str]:
        r = await http.get(GROQ_LIST_URL, headers={"Authorization": f"Bearer {key}"}, timeout=20)
        if r.status_code >= 400:
            raise _http_error(r)
        return rank_groq(r.json().get("data") or [])

    return await _run_models(GROQ, discover, lambda m: _groq_one(http, key, m, prompt, temperature))


async def _call_ai(
    http: httpx.AsyncClient,
    gemini_pool: KeyPool | None,
    groq_pool: KeyPool | None,
    prompt: str,
    temperature: float = 0.2,
) -> str:
    """يحاول Gemini ثم Groq مع تدوير المفاتيح والنماذج."""
    errors: list[str] = []

    if gemini_pool:
        try:
            return await gemini_pool.call(lambda k: _gemini_generate(http, k, prompt, temperature))
        except NoKeyAvailable:
            errors.append("gemini: no key")
        except ProviderHTTPError as e:
            errors.append(f"gemini: {e}")
            log.warning("Gemini failed: %s", e)
        except Exception as e:
            errors.append(f"gemini: {type(e).__name__}: {e}")
            log.warning("Gemini unexpected error: %s", e)

    if groq_pool:
        try:
            return await groq_pool.call(lambda k: _groq_generate(http, k, prompt, temperature))
        except NoKeyAvailable:
            errors.append("groq: no key")
        except ProviderHTTPError as e:
            errors.append(f"groq: {e}")
            log.warning("Groq failed: %s", e)
        except Exception as e:
            errors.append(f"groq: {type(e).__name__}: {e}")
            log.warning("Groq unexpected error: %s", e)

    raise RuntimeError("AI unavailable: " + "; ".join(errors) if errors else "no AI pools configured")


async def selftest(http: httpx.AsyncClient, gemini_pool: KeyPool | None, groq_pool: KeyPool | None) -> dict:
    """فحص حيّ: يجرّب كل مزوّد بطلب صغير ويعيد النموذج المستخدم أو سبب الفشل."""
    out: dict = {}
    for name, pool, fn in (("gemini", gemini_pool, _gemini_generate), ("groq", groq_pool, _groq_generate)):
        if not pool:
            out[name] = {"ok": False, "error": "no keys"}
            continue
        t0 = time.time()
        try:
            txt = await pool.call(lambda k, fn=fn: fn(http, k, 'Return JSON only: {"ok": true}', 0.0), count_use=False)
            out[name] = {"ok": True, "ms": int((time.time() - t0) * 1000), "reply": txt[:60]}
        except Exception as e:
            out[name] = {"ok": False, "error": str(e)[:300]}
    out["models"] = models_status()
    return out


# ───────────────── تنظيف العنوان ─────────────────

_CLEAN_PROMPT = """أنت مساعد متخصص في استخراج اسم الفيلم/المسلسل من اسم ملف فيديو.
المدخل قد يحتوي على أسماء مواقع تحميل (Qfilm.tv, YTS, Torrenting...)، مجموعات إصدار، رموز، دقة، ترميز، لغة.

أرجع JSON فقط بالشكل:
{{
  "title": "العنوان الأصلي النظيف كما هو معروف عالمياً (بالإنجليزية غالباً)",
  "year": 2014 أو null,
  "season": رقم أو null,
  "episode": رقم أو null,
  "kind": "movie" أو "tv",
  "confidence": 0.0 إلى 1.0
}}

قواعد:
- احذف أسماء المواقع ومجموعات الإصدار والرموز والدقة والترميز واللغات.
- إذا كان المدخل عنواناً عربياً لفيلم أجنبي معروف فأرجع عنوانه الإنجليزي الأصلي.
- لا تختلق عنواناً غير موجود في المدخل أو غير معروف لك.
- إذا كان الاسم عشوائياً تماماً (مثل random_84721) أرجع title فارغاً وconfidence منخفض.

أمثلة:
"[Qfilm.tv].Donnie.Brasco.1997.BluRay.720p.mp4" → {{"title":"Donnie Brasco","year":1997,"season":null,"episode":null,"kind":"movie","confidence":0.95}}
"www.site.com - Breaking.Bad.S02E03.720p.HDTV.x264-CTU.mkv" → {{"title":"Breaking Bad","year":null,"season":2,"episode":3,"kind":"tv","confidence":0.95}}

المدخل: {raw}
"""


async def clean_title(
    http: httpx.AsyncClient,
    gemini_pool: KeyPool | None,
    groq_pool: KeyPool | None,
    raw: str,
) -> dict | None:
    """ينظف عنواناً مشوّشاً ويعيد dict جاهزاً لـ parser/tmdb."""
    if not raw or not (gemini_pool or groq_pool):
        return None
    try:
        text = await _call_ai(http, gemini_pool, groq_pool, _CLEAN_PROMPT.format(raw=raw[:300]))
        data = _extract_json(text)
        if isinstance(data, list) and data:
            data = data[0]
        title = (data.get("title") or "").strip()
        if not title or float(data.get("confidence") or 0) < 0.35:
            return None

        def _int(v):
            try:
                return int(v) if v not in (None, "", "null") else None
            except (TypeError, ValueError):
                return None

        season, episode = _int(data.get("season")), _int(data.get("episode"))
        return {
            "title": title,
            "year": _int(data.get("year")),
            "season": season,
            "episode": episode,
            "kind": data.get("kind") or ("tv" if season else "movie"),
        }
    except Exception as e:
        log.warning("clean_title failed: %s", e)
        return None


# ───────────────── ترجمة SRT ─────────────────

_SRT_BLOCK = re.compile(
    r"(\d+)\s*\n"
    r"(\d{2}:\d{2}:\d{2}[,.]\d{3}\s*-->\s*\d{2}:\d{2}:\d{2}[,.]\d{3})\s*\n"
    r"([\s\S]*?)(?=\n\d+\s*\n\d{2}:|\Z)",
    re.MULTILINE,
)

_TRANSLATE_PROMPT = """ترجم نصوص الترجمة (subtitles) التالية إلى العربية الفصحى المعاصرة المناسبة للشاشة.
قواعد صارمة:
1. ترجم المعنى والسياق، لا تترجم حرفياً كلمة بكلمة، وبأسلوب طبيعي مختصر يناسب القراءة السريعة.
2. كل عنصر له رقم "i": أرجع لكل رقم ترجمته بنفس الرقم، ولا تحذف ولا تدمج ولا تضف عناصر.
3. احتفظ بالوسوم كما هي (مثل <i> و </i> و {{\\an8}}) وبفواصل الأسطر داخل النص.
4. أسماء الشخصيات والأماكن تُترك أو تُكتب بالعربية الشائعة.
5. إذا كان النص رمزاً أو موسيقى (♪) أرجعه كما هو.
6. أرجع JSON فقط بالشكل: {{"lines": [{{"i": 1, "t": "الترجمة"}}, ...]}}

الأفلام/المسلسل: __TITLE__

النصوص:
__PAYLOAD__
"""

_AR = re.compile(r"[\u0600-\u06ff]")
_LATIN = re.compile(r"[A-Za-z]{3,}")


def parse_srt(content: str) -> list[dict]:
    """يفصل ملف SRT إلى قائمة مقاطع."""
    blocks = []
    for m in _SRT_BLOCK.finditer(content.replace("\r\n", "\n").replace("\r", "\n")):
        idx, timing, text = m.group(1), m.group(2), m.group(3).strip()
        blocks.append({"index": int(idx), "timing": timing.replace(".", ","), "text": text})
    return blocks


def build_srt(blocks: list[dict]) -> str:
    parts = []
    for i, b in enumerate(blocks, 1):
        parts.append(f"{i}\n{b['timing']}\n{b['text']}\n")
    return "\n".join(parts)


async def _translate_batch(
    http: httpx.AsyncClient,
    gemini_pool: KeyPool | None,
    groq_pool: KeyPool | None,
    items: list[tuple[int, str]],
    title: str = "",
) -> dict[int, str]:
    """يترجم دفعة (فهرس، نص) ويعيد ما نجح فقط {فهرس: ترجمة}. لا يفشل بسبب اختلاف العدد."""
    payload = json.dumps([{"i": i, "t": t} for i, t in items], ensure_ascii=False)
    prompt = _TRANSLATE_PROMPT.replace("__PAYLOAD__", payload).replace("__TITLE__", title or "غير معروف")
    raw = await _call_ai(http, gemini_pool, groq_pool, prompt, temperature=0.15)
    data = _extract_json(raw)
    lines = data.get("lines") if isinstance(data, dict) else data
    if not isinstance(lines, list):
        raise ValueError("AI response missing lines array")

    wanted = {i for i, _ in items}
    got: dict[int, str] = {}
    if lines and all(isinstance(x, str) for x in lines):
        # نموذج أرجع نصوصاً بلا أرقام: نقبلها فقط إذا طابق العدد تماماً
        if len(lines) == len(items):
            return {i: str(t).strip() for (i, _), t in zip(items, lines) if str(t).strip()}
        raise ValueError(f"line count mismatch: got {len(lines)} expected {len(items)}")
    for x in lines:
        if not isinstance(x, dict):
            continue
        try:
            i = int(x.get("i"))
        except (TypeError, ValueError):
            continue
        t = x.get("t") if x.get("t") is not None else x.get("text")
        if i in wanted and isinstance(t, str) and t.strip():
            got[i] = t.strip()
    return got


async def translate_srt_to_arabic(
    http: httpx.AsyncClient,
    gemini_pool: KeyPool | None,
    groq_pool: KeyPool | None,
    srt_content: str,
    max_retries: int = 2,
    title: str = "",
) -> str | None:
    """
    يترجم ملف SRT كاملاً إلى العربية على دفعات متوازية.
    كل سطر مرقّم، فالنقص يُعاد ترجمته وحده بدل إعادة الدفعة كلها.
    يعيد None إذا تعذّر إكمال الملف (لا نرسل ملفاً ناقصاً).
    """
    if not (gemini_pool or groq_pool):
        return None

    blocks = parse_srt(srt_content)
    if not blocks:
        return None

    results: dict[int, str] = {}
    sem = asyncio.Semaphore(CONCURRENCY)

    async def work(chunk: list[int]) -> list[int]:
        todo, size, fails = list(chunk), len(chunk), 0
        async with sem:
            while todo and fails <= max_retries:
                sub = todo[:size]
                try:
                    got = await _translate_batch(
                        http, gemini_pool, groq_pool, [(i, blocks[i]["text"]) for i in sub], title)
                except Exception as e:
                    log.warning("translate batch failed (size %s, fails %s): %s", len(sub), fails, e)
                    got = {}
                results.update(got)
                todo = [i for i in todo if i not in results]
                if len(got) < len(sub):
                    size = max(5, size // 2)          # قسّم الدفعة عند النقص
                    if not got:
                        fails += 1
                        await asyncio.sleep(0.8 * fails)
                else:
                    fails = 0
        return todo

    chunks = [list(range(i, min(i + BATCH_SIZE, len(blocks)))) for i in range(0, len(blocks), BATCH_SIZE)]
    missing = [i for left in await asyncio.gather(*(work(c) for c in chunks)) for i in left]
    if missing:
        log.error("failed to translate %s/%s lines (first missing: %s)", len(missing), len(blocks), missing[0])
        return None

    # فحص جودة: يجب أن تكون الأغلبية عربية فعلاً (لا إرجاع النص الإنجليزي كما هو)
    checked = [i for i, b in enumerate(blocks) if _LATIN.search(b["text"])]
    if checked:
        arabic = sum(1 for i in checked if _AR.search(results[i]))
        if arabic / len(checked) < 0.5:
            log.error("translation rejected: only %s/%s lines are Arabic", arabic, len(checked))
            return None

    return build_srt([
        {"index": i + 1, "timing": b["timing"], "text": results[i]} for i, b in enumerate(blocks)
    ])


def pick_source_candidate(candidates: list, preferred_langs: list[str] | None = None):
    """
    يختار أفضل مرشح للترجمة حسب أولوية اللغات.
    candidates: قائمة Candidate أو dict فيها lang.
    """
    order = preferred_langs or SOURCE_LANG_PRIORITY
    by_lang: dict[str, list] = {}
    for c in candidates:
        lang = (getattr(c, "lang", None) or c.get("lang") if isinstance(c, dict) else "") or ""
        lang = lang.lower()[:2]
        if lang == "ar":
            continue
        by_lang.setdefault(lang, []).append(c)

    for lang in order:
        if by_lang.get(lang):
            return by_lang[lang][0]
    # أي لغة أخرى
    for lang, items in by_lang.items():
        if items:
            return items[0]
    return None
