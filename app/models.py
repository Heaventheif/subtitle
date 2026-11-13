from __future__ import annotations

from dataclasses import dataclass, field


@dataclass
class Candidate:
    provider: str            # wyzie | opensubtitles | subdl | yify
    ref: str                 # مرجع التنزيل (رابط أو file_id)
    lang: str = ""
    release: str = ""
    rating: float | None = None
    downloads: int | None = None
    fmt: str = "srt"
    hi: bool = False
    hash_match: bool = False
    extra: dict = field(default_factory=dict)
