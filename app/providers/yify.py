"""الطبقة 2ب: كشط YIFY Subtitles (أفلام فقط، بمعرّف IMDb) مع Firecrawl عند الحماية.

ملاحظة: بنية الصفحة قد تتغير؛ المحلّل متساهل ويُهمل ما لا يفهمه.
"""
from __future__ import annotations

import httpx
from bs4 import BeautifulSoup

from ..keypool import KeyPool, ProviderHTTPError
from ..models import Candidate

SITE = "https://yifysubtitles.ch"
_UA = "Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 Chrome/124 Mobile Safari/537.36"


def _blocked(r: httpx.Response) -> bool:
    if r.status_code in (403, 429, 503):
        return True
    t = r.text[:2000].lower()
    return "just a moment" in t or "cf-chl" in t


async def _firecrawl(http: httpx.AsyncClient, pool: KeyPool, url: str) -> str:
    async def go(key: str) -> str:
        r = await http.post(
            "https://api.firecrawl.dev/v1/scrape",
            json={"url": url, "formats": ["rawHtml"]},
            headers={"Authorization": f"Bearer {key}"},
            timeout=60,
        )
        if r.status_code >= 400:
            raise ProviderHTTPError(r.status_code, r.text[:120])
        return (r.json().get("data") or {}).get("rawHtml") or ""
    return await pool.call(go)


def parse_listing(html: str, lang_names: set[str]) -> list[Candidate]:
    soup = BeautifulSoup(html, "html.parser")
    out: list[Candidate] = []
    for tr in soup.select("table tbody tr"):
        lang_el = tr.select_one(".sub-lang")
        link = tr.select_one("a[href*='/subtitles/']")
        if not lang_el or not link:
            continue
        lang_name = lang_el.get_text(strip=True).lower()
        if lang_names and lang_name not in lang_names:
            continue
        rating_el = tr.select_one(".rating-cell .label, .rating-cell")
        try:
            rating = float(rating_el.get_text(strip=True)) if rating_el else None
        except ValueError:
            rating = None
        name = link.get_text(" ", strip=True).replace("subtitle", "", 1).strip()
        href = link["href"]
        out.append(Candidate(
            provider="yify", ref=href, lang="ar" if lang_name == "arabic" else lang_name[:2],
            release=name, rating=rating, hi="hi" in tr.get("class", []),
        ))
    return out


async def search(http: httpx.AsyncClient, fc_pool: KeyPool | None, imdb_id: str | None,
                 lang_names: set[str]) -> list[Candidate]:
    if not imdb_id:
        return []
    url = f"{SITE}/movie-imdb/{imdb_id}"
    r = await http.get(url, headers={"User-Agent": _UA}, follow_redirects=True)
    if _blocked(r):
        if not fc_pool:
            raise ProviderHTTPError(r.status_code, "blocked")
        html = await _firecrawl(http, fc_pool, url)
    else:
        html = r.text
    return parse_listing(html, lang_names)


async def download(http: httpx.AsyncClient, ref: str) -> bytes:
    url = SITE + ref.replace("/subtitles/", "/subtitle/", 1) + ".zip"
    r = await http.get(url, headers={"User-Agent": _UA}, follow_redirects=True)
    if r.status_code >= 400:
        raise ProviderHTTPError(r.status_code, "download failed")
    return r.content
