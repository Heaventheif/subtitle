"""الطبقة 1ب: OpenSubtitles REST API (البحث بمفتاح، والتنزيل بتسجيل دخول الحساب)."""
from __future__ import annotations

import time

import httpx

from ..keypool import ProviderHTTPError
from ..models import Candidate

BASE = "https://api.opensubtitles.com/api/v1"
_tokens: dict[str, tuple[str, float]] = {}


def split_account(packed: str) -> tuple[str, str, str]:
    api_key, username, password = packed.split("|", 2)
    return api_key, username, password


def pack_account(a: dict) -> str:
    return f"{a['api_key']}|{a['username']}|{a['password']}"


def _headers(api_key: str, ua: str, token: str | None = None) -> dict:
    h = {"Api-Key": api_key, "User-Agent": ua, "Accept": "application/json", "Content-Type": "application/json"}
    if token:
        h["Authorization"] = f"Bearer {token}"
    return h


def _raise(r: httpx.Response) -> None:
    if r.status_code >= 400:
        ra = r.headers.get("Retry-After")
        raise ProviderHTTPError(r.status_code, r.text[:120], float(ra) if ra and ra.isdigit() else None)


async def search(http: httpx.AsyncClient, packed: str, ua: str, *, imdb_id: str | None, tmdb_id: int | None,
                 query: str, season: int | None, episode: int | None, lang: str,
                 moviehash: str | None) -> list[Candidate]:
    api_key, _, _ = split_account(packed)
    params: dict = {"languages": lang, "order_by": "download_count"}
    if moviehash:
        params["moviehash"] = moviehash
    if imdb_id:
        params["imdb_id"] = imdb_id.lstrip("t")
    elif tmdb_id:
        params["tmdb_id"] = tmdb_id
    elif query:
        params["query"] = query
    else:
        return []
    if season and episode:
        params["season_number"], params["episode_number"] = season, episode
    r = await http.get(f"{BASE}/subtitles", params=params, headers=_headers(api_key, ua))
    _raise(r)
    out: list[Candidate] = []
    for d in r.json().get("data", []):
        a = d.get("attributes", {})
        files = a.get("files") or []
        if not files:
            continue
        out.append(Candidate(
            provider="opensubtitles", ref=str(files[0]["file_id"]),
            lang=(a.get("language") or lang).lower()[:2],
            release=a.get("release") or files[0].get("file_name") or "",
            rating=a.get("ratings") or None, downloads=a.get("download_count"),
            hi=bool(a.get("hearing_impaired")), hash_match=bool(a.get("moviehash_match")),
        ))
    return out


async def _login(http: httpx.AsyncClient, packed: str, ua: str) -> str:
    cached = _tokens.get(packed)
    if cached and cached[1] > time.time():
        return cached[0]
    api_key, user, pwd = split_account(packed)
    r = await http.post(f"{BASE}/login", json={"username": user, "password": pwd}, headers=_headers(api_key, ua))
    _raise(r)
    token = r.json()["token"]
    _tokens[packed] = (token, time.time() + 20 * 3600)
    return token


async def download(http: httpx.AsyncClient, packed: str, ua: str, file_id: str) -> bytes:
    api_key, _, _ = split_account(packed)
    token = await _login(http, packed, ua)
    r = await http.post(f"{BASE}/download", json={"file_id": int(file_id)}, headers=_headers(api_key, ua, token))
    if r.status_code == 401:
        _tokens.pop(packed, None)
    _raise(r)
    link = r.json().get("link")
    if not link:
        raise ProviderHTTPError(502, "no link")
    f = await http.get(link, follow_redirects=True)
    _raise(f)
    return f.content
