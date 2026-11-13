"""تحويل العنوان إلى معرّف TMDB/IMDb (مطلوب لأن Wyzie يعمل بالمعرّفات)."""
from __future__ import annotations

import hashlib
import time

import httpx

from .db import Db

BASE = "https://api.themoviedb.org/3"


def _key(parsed: dict) -> str:
    raw = f"{parsed['title'].lower()}|{parsed.get('year')}|{'tv' if parsed.get('season') else 'movie'}"
    return hashlib.sha1(raw.encode()).hexdigest()


async def resolve(http: httpx.AsyncClient, db: Db, api_key: str, parsed: dict) -> dict | None:
    if not api_key or not parsed.get("title"):
        return None
    nh = _key(parsed)
    row = db.one("SELECT * FROM title_map WHERE name_hash=?", (nh,))
    if row:
        return {"tmdb_id": row["tmdb_id"], "imdb_id": row["imdb_id"], "kind": row["kind"],
                "title": row["title"], "year": row["year"]}
    kind = "tv" if parsed.get("season") else "movie"
    headers, params = {}, {"query": parsed["title"], "language": "en-US"}
    if api_key.startswith("eyJ"):
        headers["Authorization"] = f"Bearer {api_key}"
    else:
        params["api_key"] = api_key
    if parsed.get("year"):
        params["first_air_date_year" if kind == "tv" else "year"] = parsed["year"]
    r = await http.get(f"{BASE}/search/{kind}", params=params, headers=headers)
    if r.status_code != 200:
        return None
    results = r.json().get("results") or []
    if not results and parsed.get("year"):
        params.pop("first_air_date_year", None)
        params.pop("year", None)
        r = await http.get(f"{BASE}/search/{kind}", params=params, headers=headers)
        results = r.json().get("results") or [] if r.status_code == 200 else []
    if not results:
        return None
    top = results[0]
    tmdb_id = top["id"]
    imdb_id = None
    p2 = {} if api_key.startswith("eyJ") else {"api_key": api_key}
    r2 = await http.get(f"{BASE}/{kind}/{tmdb_id}/external_ids", params=p2, headers=headers)
    if r2.status_code == 200:
        imdb_id = r2.json().get("imdb_id")
    title = top.get("title") or top.get("name") or parsed["title"]
    date = top.get("release_date") or top.get("first_air_date") or ""
    year = int(date[:4]) if date[:4].isdigit() else parsed.get("year")
    db.execute(
        "INSERT OR REPLACE INTO title_map(name_hash,tmdb_id,imdb_id,kind,title,year,created_at) VALUES(?,?,?,?,?,?,?)",
        (nh, tmdb_id, imdb_id, kind, title, year, time.time()),
    )
    return {"tmdb_id": tmdb_id, "imdb_id": imdb_id, "kind": kind, "title": title, "year": year}
