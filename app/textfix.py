"""استخراج الترجمة من الأرشيف وإصلاح الترميز (Windows-1256 → UTF-8)."""
from __future__ import annotations

import io
import zipfile

_PREF = {"srt": 0, "vtt": 1, "ass": 2, "ssa": 3}


def extract_subtitle(data: bytes) -> tuple[bytes, str]:
    """يعيد (محتوى، صيغة). يدعم ZIP؛ وإلا يعامل البيانات كملف ترجمة مباشر."""
    if data[:2] == b"PK":
        with zipfile.ZipFile(io.BytesIO(data)) as zf:
            cands = []
            for info in zf.infolist():
                ext = info.filename.rsplit(".", 1)[-1].lower() if "." in info.filename else ""
                if ext in _PREF and not info.is_dir():
                    cands.append((_PREF[ext], -info.file_size, info.filename, ext))
            if not cands:
                raise ValueError("no subtitle in archive")
            cands.sort()
            _, _, fname, ext = cands[0]
            return zf.read(fname), ext
    head = data[:400].lstrip(b"\xef\xbb\xbf\xff\xfe\xfe\xff").lower()
    if head.startswith(b"webvtt"):
        return data, "vtt"
    if b"[script info]" in head:
        return data, "ass"
    return data, "srt"


def _arabic_ratio(s: str) -> float:
    letters = [c for c in s if c.isalpha()]
    if not letters:
        return 0.0
    return sum(1 for c in letters if "\u0600" <= c <= "\u06ff") / len(letters)


def decode_text(raw: bytes, lang: str = "ar") -> tuple[str, str]:
    """فك الترميز بذكاء: UTF-8 أولاً، ثم UTF-16 (BOM)، ثم cp1256/iso-8859-6 للعربية."""
    if raw.startswith(b"\xef\xbb\xbf"):
        return raw[3:].decode("utf-8", errors="replace"), "utf-8"
    if raw.startswith((b"\xff\xfe", b"\xfe\xff")):
        return raw.decode("utf-16", errors="replace"), "utf-16"
    try:
        return raw.decode("utf-8"), "utf-8"
    except UnicodeDecodeError:
        pass
    if lang.startswith("ar"):
        best = None
        for enc in ("cp1256", "iso8859_6"):
            try:
                text = raw.decode(enc)
            except UnicodeDecodeError:
                continue
            ratio = _arabic_ratio(text)
            if best is None or ratio > best[0]:
                best = (ratio, text, enc)
        if best and best[0] > 0.2:
            return best[1], best[2]
    for enc in ("cp1252", "latin-1"):
        try:
            return raw.decode(enc), enc
        except UnicodeDecodeError:
            continue
    return raw.decode("utf-8", errors="replace"), "utf-8"


def normalize_subtitle(data: bytes, lang: str) -> tuple[bytes, str, str]:
    """يعيد (UTF-8، الصيغة، الترميز الأصلي المكتشف)."""
    raw, fmt = extract_subtitle(data)
    text, enc = decode_text(raw, lang)
    text = text.replace("\x00", "").replace("\r\n", "\n").replace("\r", "\n").lstrip("\ufeff")
    if not text.endswith("\n"):
        text += "\n"
    return text.encode("utf-8"), fmt, enc
