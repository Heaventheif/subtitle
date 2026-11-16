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

# أسماء مواقع/نطاقات تلتصق بالاسم: [Qfilm.tv]  www.site.com -  site.net.
_TLD = r"(?:com|net|org|tv|cc|info|ws|xyz|site|live|club|top|vip|ir|biz|cam|fun|online|movie|film|cinema)"
_BRACKET_SITE = re.compile(r"[\[\(\{][^\]\)\}]*\b[a-z0-9-]+\.%s\b[^\]\)\}]*[\]\)\}]" % _TLD, re.I)
_WWW_SITE = re.compile(r"(?i)\bwww[\s._-]+[a-z0-9-]+[\s._-]+%s\b" % _TLD)
_DOMAIN = re.compile(r"(?i)(?:^|(?<=[\s._\-\[\(]))[a-z0-9-]{2,}\.%s(?=[\s._\-\]\)]|$)" % _TLD)
# مجموعة بين أقواس في البداية بلا معلومات مفيدة (اسم موقع بلا نقطة مثل [Qfilm])
_LEAD_GROUP = re.compile(r"^\s*[\[\(\{]([^\]\)\}]{1,30})[\]\)\}]\s*[-._ ]*")


def strip_site_noise(name: str) -> str:
    """يزيل أسماء المواقع والنطاقات من اسم الملف قبل التحليل."""
    t = _BRACKET_SITE.sub(" ", name)
    t = _WWW_SITE.sub(" ", t)
    t = _DOMAIN.sub(" ", t)
    m = _LEAD_GROUP.match(t)
    if m:
        inner = m.group(1)
        # لا نحذفها إذا كانت دقة/مصدر/سنة (معلومة مفيدة) أو أرقاماً فقط
        if not (_RES.search(inner) or _SRC.search(inner) or _YEAR.search(inner) or inner.strip().isdigit()):
            t = t[m.end():]
    return re.sub(r"\s{2,}", " ", t).strip(" -_.")


def parse_filename(name: str) -> dict:
    raw = (name or "").strip()
    base = strip_site_noise(_EXT.sub("", raw)) or _EXT.sub("", raw)
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

    # أول موضع لوسم إصدار/موسم (لا نحتسب ما يقع في بداية النص قبل أي عنوان)
    tok_cut = len(text)
    if se and text[:se.start()].strip():
        tok_cut = min(tok_cut, se.start())
    for rx in (_RES, _SRC, _CODEC):
        for mm in rx.finditer(text):
            if text[:mm.start()].strip():
                tok_cut = min(tok_cut, mm.start())
                break

    # السنة: آخر سنة معقولة قبل وسوم الإصدار (Blade Runner 2049 2017 → 2017)
    year = None
    cut = tok_cut
    valid = [ym for ym in _YEAR.finditer(text)
             if text[:ym.start()].strip() and ym.start() < tok_cut and int(ym.group(1)) <= 2028]
    if valid:
        year = int(valid[-1].group(1))
        cut = valid[-1].start()

    title = text[:cut]
    for rx in (_RES, _SRC, _CODEC, _JUNK):
        title = rx.sub(" ", title)
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
