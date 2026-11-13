"""الطبقة 2أ: SubDL (واجهتهم الرسمية بمفتاح)."""
from __future__ import annotations

import httpx

from ..keypool import ProviderHTTPError
from ..models import Candidate

API = "https://api.subdl.com/api/v1/subtitles"
DL = "https://dl.subdl.com"
_LANG = {"arabic": "ar", "english": "en", "french": "fr", "spanish": "es", "turkish": "tr", "persian": "fa"}


def _raise(r: httpx.Response) -> None:
    if r.status_code >= 400:
        raise ProviderHTTPError(r.status_code, r.text[:120])


def _norm_lang(v: str) -> str:
    v = (v or "").lower()
    return _LANG.get(v, v[:2])


async def search(http: httpx.AsyncClient, key: str, *, imdb_id: str | None, tmdb_id: int | None, query: str,
                 season: int | None, episode: int | None, lang: str) -> list[Candidate]:
    params: dict = {"api_key": key, "languages": lang.upper(), "subs_per_page": 30, "releases": 1}
    if imdb_id:
        params["imdb_id"] = imdb_id
    elif tmdb_id:
        params["tmdb_id"] = tmdb_id
    elif query:
        params["film_name"] = query
    else:
        return []
    if season and episode:
        params["season_number"], params["episode_number"] = season, episode
        params["type"] = "tv"
    r = await http.get(API, params=params)
    _raise(r)
    out: list[Candidate] = []
    for s in r.json().get("subtitles", []) or []:
        url = s.get("url")
        if not url:
            continue
        out.append(Candidate(
            provider="subdl", ref=url, lang=_norm_lang(s.get("lang") or s.get("language") or lang),
            release=s.get("release_name") or s.get("name") or "", hi=bool(s.get("hi")),
        ))
    return out


async def download(http: httpx.AsyncClient, ref: str) -> bytes:
    url = ref if ref.startswith("http") else DL + ref
    r = await http.get(url, follow_redirects=True)
    _raise(r)
    return r.content
