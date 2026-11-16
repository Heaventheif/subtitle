"""تحليل اسم الملف: العنوان، السنة، الموسم/الحلقة، وسوم الإصدار."""
from __future__ import annotations

import re

_EXT = re.compile(r"\.(mkv|mp4|avi|mov|wmv|flv|webm|m4v|ts|srt|vtt|ass|ssa|zip)$", re.I)
_SE = re.compile(r"(?i)\bS(\d{1,2})[ ._-]?E(\d{1,3})\b|\b(\d{1,2})x(\d{2,3})\b")
_YEAR = re.compile(r"(?<!\d)(19\d{2}|20\d{2})(?!\d)")
_RES = re.compile(r"(?i)\b(480p|576p|720p|1080p|1440p|2160p|4k|uhd)\b")
_SRC = re.compile(r"(?i)\b(web-?dl|web-?rip|webrip|blu-?ray|bdrip|brrip|hdrip|dvdrip|hdtv|remux|hdcam|cam|amzn|nf|dsnp|hmax)\b")
_CODEC = re.compile(r"(?i)\b(x264|x265|h\.?264|h\.?265|hevc|avc|av1|xvid|10bit|hdr|aac|ac3|dts|ddp5?\.?1?|atmos)\b")
_JUNK = re.compile(
    r"(?i)\b("
    r"proper|repack|extended|unrated|internal|multi|dual|dubbed|arabic|subbed|"
    r"yify|yts|rarbg|ettv|eztv|rartv|sparks|geckos|ctrlhd|dimension|killers|"
    r"official|trailer|sample|softsub|hardsub"
    r")\b"
)
# رموز وعلامات تجارية شائعة
_SYMBOLS = re.compile(r"[™®©]+")


def parse_filename(name: str) -> dict:
    raw = (name or "").strip()
    base = _EXT.sub("", raw)
    release_group = ""
    m = re.search(r"-([A-Za-z0-9]{2,12})$", base)
    if m:
        release_group = m.group(1).lower()
    text = _SYMBOLS.sub(" ", base)
    text = re.sub(r"[\[\]\(\)\{\}]", " ", text)
    text = re.sub(r"[._]+", " ", text)

    season = episode = None
    se = _SE.search(text)
    if se:
        season = int(se.group(1) or se.group(3))
        episode = int(se.group(2) or se.group(4))

    year = None
    cut = len(text)
    for ym in _YEAR.finditer(text):
        if ym.start() > 0:           # السنة في بداية الاسم جزء من العنوان غالباً
            year = int(ym.group(1))
            cut = min(cut, ym.start())
            break
    if se:
        cut = min(cut, se.start())
    for rx in (_RES, _SRC, _CODEC):
        mm = rx.search(text)
        if mm and mm.start() > 0:
            cut = min(cut, mm.start())

    title = _JUNK.sub(" ", text[:cut])
    title = re.sub(r"\s+", " ", title).strip(" -_")

    tokens: set[str] = set()
    for rx in (_RES, _SRC, _CODEC):
        for mm in rx.finditer(text):
            tokens.add(mm.group(1).lower().replace("-", "").replace(".", ""))
    if release_group:
        tokens.add(release_group)
    return {
        "title": title,
        "year": year,
        "season": season,
        "episode": episode,
        "tokens": sorted(tokens),
        "norm": re.sub(r"[^a-z0-9\u0600-\u06ff]+", "", base.lower()),
    }


def release_tokens(text: str) -> set[str]:
    """وسوم الإصدار من نص حر (اسم إصدار الترجمة)."""
    if not text:
        return set()
    p = parse_filename(text)
    return set(p["tokens"])
