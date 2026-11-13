"""الطبقة 1أ: Wyzie Subs (بالمعرّفات IMDb/TMDB)."""
from __future__ import annotations

import httpx

from ..keypool import ProviderHTTPError
from ..models import Candidate

BASE = "https://sub.wyzie.io/search"


def _raise(r: httpx.Response) -> None:
    if r.status_code >= 400:
        ra = r.headers.get("Retry-After")
        raise ProviderHTTPError(r.status_code, r.text[:120], float(ra) if ra and ra.isdigit() else None)


async def search(http: httpx.AsyncClient, key: str, *, tmdb_id: int | None, imdb_id: str | None,
                 season: int | None, episode: int | None, lang: str, filename: str = "") -> list[Candidate]:
    params: dict = {"key": key, "language": lang}
    if tmdb_id:
        params["tmdb_id"] = tmdb_id
    elif imdb_id:
        params["id"] = imdb_id
    else:
        return []
    if season and episode:
        params["season"], params["episode"] = season, episode
    r = await http.get(BASE, params=params)
    _raise(r)
    out: list[Candidate] = []
    for it in r.json() or []:
        url = it.get("url")
        if not url:
            continue
        rel = it.get("release") or it.get("fileName") or it.get("file") or it.get("media") or it.get("display") or ""
        if isinstance(rel, list):
            rel = " ".join(map(str, rel))
        dl = it.get("downloads") or it.get("downloadCount")
        out.append(Candidate(
            provider="wyzie", ref=url, lang=(it.get("language") or lang).lower()[:2],
            release=str(rel), downloads=int(dl) if str(dl).isdigit() else None,
            fmt=(it.get("format") or "srt").lower(), hi=bool(it.get("isHearingImpaired")),
        ))
    return out


async def download(http: httpx.AsyncClient, ref: str) -> bytes:
    r = await http.get(ref, follow_redirects=True)
    _raise(r)
    return r.content
