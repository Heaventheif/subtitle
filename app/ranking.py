"""الترتيب الذكي: العربية أولاً، ثم تطابق الإصدار/البصمة، ثم التقييم والتحميلات."""
from __future__ import annotations

import math
import re

from .models import Candidate
from .parser import release_tokens


def score(c: Candidate, parsed: dict, langs: list[str]) -> int:
    s = 0.0
    primary = langs[0] if langs else "ar"
    if c.lang == primary:
        s += 40
    elif c.lang in langs:
        s += 10
    if c.hash_match:
        s += 60
    want = set(parsed.get("tokens", []))
    have = release_tokens(c.release)
    if want and have:
        s += 40 * len(want & have) / len(want | have)
    norm = re.sub(r"[^a-z0-9\u0600-\u06ff]+", "", (c.release or "").lower())
    if norm and parsed.get("norm") and (norm in parsed["norm"] or parsed["norm"] in norm):
        s += 20
    if c.rating:
        s += min(10.0, float(c.rating))
    if c.downloads:
        s += min(10.0, math.log10(1 + c.downloads) * 2)
    if c.hi:
        s -= 3
    return max(0, min(100, round(s)))


def rank(cands: list[Candidate], parsed: dict, langs: list[str]) -> list[tuple[Candidate, int]]:
    scored = [(c, score(c, parsed, langs)) for c in cands]
    scored.sort(key=lambda x: (-x[1], -(x[0].downloads or 0)))
    return scored
