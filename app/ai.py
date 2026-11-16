"""خدمات الذكاء الاصطناعي: تنظيف العنوان + ترجمة الترجمات إلى العربية.

يستخدم Gemini أولاً ثم Groq كبديل، مع تدوير المفاتيح (KeyPool).
"""
from __future__ import annotations

import asyncio
import json
import logging
import re
from typing import Any

import httpx

from .keypool import KeyPool, NoKeyAvailable, ProviderHTTPError

log = logging.getLogger("subs.ai")

# أولوية اللغات المصدر عند غياب العربية
SOURCE_LANG_PRIORITY = ["en", "fr", "es", "pt", "ru", "tr", "de", "it"]

# حجم الدفعة (عدد المقاطع في كل طلب ترجمة)
BATCH_SIZE = 40

GEMINI_URL = "https://generativelanguage.googleapis.com/v1beta/models/{model}:generateContent"
GROQ_URL = "https://api.groq.com/openai/v1/chat/completions"

GEMINI_MODEL = "gemini-2.0-flash"
GROQ_MODEL = "llama-3.3-70b-versatile"


# ───────────────── مساعدات عامة ─────────────────

def _extract_json(text: str) -> Any:
    """يستخرج JSON من رد قد يحتوي على markdown."""
    text = text.strip()
    if text.startswith("```"):
        text = re.sub(r"^```(?:json)?\s*", "", text)
        text = re.sub(r"\s*```$", "", text)
    return json.loads(text)


async def _gemini_generate(http: httpx.AsyncClient, key: str, prompt: str, temperature: float = 0.2) -> str:
    url = GEMINI_URL.format(model=GEMINI_MODEL)
    payload = {
        "contents": [{"parts": [{"text": prompt}]}],
        "generationConfig": {"temperature": temperature, "responseMimeType": "application/json"},
    }
    r = await http.post(url, params={"key": key}, json=payload, timeout=90)
    if r.status_code in (401, 403, 429):
        raise ProviderHTTPError(r.status_code, r.text[:200])
    if r.status_code >= 400:
        raise ProviderHTTPError(r.status_code, r.text[:300])
    data = r.json()
    try:
        return data["candidates"][0]["content"]["parts"][0]["text"]
    except (KeyError, IndexError) as e:
        raise ProviderHTTPError(502, f"bad gemini response: {e}")


async def _groq_generate(http: httpx.AsyncClient, key: str, prompt: str, temperature: float = 0.2) -> str:
    payload = {
        "model": GROQ_MODEL,
        "messages": [{"role": "user", "content": prompt}],
        "temperature": temperature,
        "response_format": {"type": "json_object"},
    }
    r = await http.post(
        GROQ_URL,
        headers={"Authorization": f"Bearer {key}", "Content-Type": "application/json"},
        json=payload,
        timeout=90,
    )
    if r.status_code in (401, 403, 429):
        raise ProviderHTTPError(r.status_code, r.text[:200])
    if r.status_code >= 400:
        raise ProviderHTTPError(r.status_code, r.text[:300])
    data = r.json()
    try:
        return data["choices"][0]["message"]["content"]
    except (KeyError, IndexError) as e:
        raise ProviderHTTPError(502, f"bad groq response: {e}")


async def _call_ai(
    http: httpx.AsyncClient,
    gemini_pool: KeyPool | None,
    groq_pool: KeyPool | None,
    prompt: str,
    temperature: float = 0.2,
) -> str:
    """يحاول Gemini ثم Groq مع تدوير المفاتيح."""
    errors: list[str] = []

    if gemini_pool:
        try:
            return await gemini_pool.call(
                lambda k: _gemini_generate(http, k, prompt, temperature)
            )
        except NoKeyAvailable:
            errors.append("gemini: no key")
        except ProviderHTTPError as e:
            errors.append(f"gemini: {e}")
            log.warning("Gemini failed: %s", e)
        except Exception as e:
            errors.append(f"gemini: {type(e).__name__}")
            log.warning("Gemini unexpected error: %s", e)

    if groq_pool:
        try:
            return await groq_pool.call(
                lambda k: _groq_generate(http, k, prompt, temperature)
            )
        except NoKeyAvailable:
            errors.append("groq: no key")
        except ProviderHTTPError as e:
            errors.append(f"groq: {e}")
            log.warning("Groq failed: %s", e)
        except Exception as e:
            errors.append(f"groq: {type(e).__name__}")
            log.warning("Groq unexpected error: %s", e)

    raise RuntimeError("AI unavailable: " + "; ".join(errors) if errors else "no AI pools configured")


# ───────────────── تنظيف العنوان ─────────────────

_CLEAN_PROMPT = """أنت مساعد متخصص في تنظيف أسماء ملفات الأفلام والمسلسلات.
المدخل قد يحتوي على أسماء مواقع تحميل، مجموعات إصدار، رموز، دقة، ترميز...

أرجع JSON فقط بالشكل:
{{
  "title": "العنوان الأصلي النظيف بالإنجليزية",
  "year": 2014 أو null,
  "season": رقم أو null,
  "episode": رقم أو null,
  "kind": "movie" أو "tv",
  "confidence": 0.0 إلى 1.0
}}

قواعد:
- احذف أسماء المواقع ومجموعات الإصدار والرموز والدقة والترميز.
- لا تختلق عنواناً غير موجود في المدخل.
- إذا كان الاسم عشوائياً تماماً (مثل random_84721) أرجع title فارغاً وconfidence منخفض.
- العنوان يجب أن يكون بالإنجليزية إن أمكن.

المدخل: {raw}
"""


async def clean_title(
    http: httpx.AsyncClient,
    gemini_pool: KeyPool | None,
    groq_pool: KeyPool | None,
    raw: str,
) -> dict | None:
    """ينظف عنواناً مشوّشاً ويعيد dict جاهزاً لـ parser/tmdb."""
    if not raw or not (gemini_pool or groq_pool):
        return None
    try:
        text = await _call_ai(http, gemini_pool, groq_pool, _CLEAN_PROMPT.format(raw=raw[:300]))
        data = _extract_json(text)
        title = (data.get("title") or "").strip()
        if not title or float(data.get("confidence") or 0) < 0.35:
            return None
        return {
            "title": title,
            "year": data.get("year"),
            "season": data.get("season"),
            "episode": data.get("episode"),
            "kind": data.get("kind") or ("tv" if data.get("season") else "movie"),
        }
    except Exception as e:
        log.warning("clean_title failed: %s", e)
        return None


# ───────────────── ترجمة SRT ─────────────────

_SRT_BLOCK = re.compile(
    r"(\d+)\s*\n"
    r"(\d{2}:\d{2}:\d{2}[,.]\d{3}\s*-->\s*\d{2}:\d{2}:\d{2}[,.]\d{3})\s*\n"
    r"([\s\S]*?)(?=\n\d+\s*\n\d{2}:|\Z)",
    re.MULTILINE,
)

_TRANSLATE_PROMPT = """ترجم مقاطع الترجمة التالية إلى العربية الفصحى المعاصرة.
قواعد صارمة:
1. ترجم المعنى والسياق، لا تترجم حرفياً كلمة بكلمة.
2. حافظ على عدد المقاطع وترتيبها تماماً.
3. لا تحذف أي مقطع ولا تضف مقاطع جديدة.
4. لا تغيّر التوقيتات (هي خارج النصوص).
5. أسماء الشخصيات والأماكن تُترك كما هي أو تُترجم إن كانت شائعة.
6. أرجع JSON فقط بالشكل: {{"lines": ["ترجمة1", "ترجمة2", ...]}}
7. عدد العناصر في lines يجب أن يساوي عدد النصوص المرسلة تماماً.

النصوص (مفصولة بـ ---):
{texts}
"""


def parse_srt(content: str) -> list[dict]:
    """يفصل ملف SRT إلى قائمة مقاطع."""
    blocks = []
    for m in _SRT_BLOCK.finditer(content.replace("\r\n", "\n").replace("\r", "\n")):
        idx, timing, text = m.group(1), m.group(2), m.group(3).strip()
        blocks.append({"index": int(idx), "timing": timing.replace(".", ","), "text": text})
    return blocks


def build_srt(blocks: list[dict]) -> str:
    parts = []
    for i, b in enumerate(blocks, 1):
        parts.append(f"{i}\n{b['timing']}\n{b['text']}\n")
    return "\n".join(parts)


async def _translate_batch(
    http: httpx.AsyncClient,
    gemini_pool: KeyPool | None,
    groq_pool: KeyPool | None,
    texts: list[str],
) -> list[str]:
    """يترجم دفعة نصوص ويعيد قائمة بنفس الطول."""
    joined = "\n---\n".join(texts)
    prompt = _TRANSLATE_PROMPT.format(texts=joined)
    raw = await _call_ai(http, gemini_pool, groq_pool, prompt, temperature=0.15)
    data = _extract_json(raw)
    lines = data.get("lines") or data.get("translations") or []
    if not isinstance(lines, list):
        raise ValueError("AI response missing lines array")
    # إذا نقصت بعض الأسطر نعيد المحاولة بتقسيم أصغر لاحقاً
    if len(lines) != len(texts):
        raise ValueError(f"line count mismatch: got {len(lines)} expected {len(texts)}")
    return [str(x).strip() for x in lines]


async def translate_srt_to_arabic(
    http: httpx.AsyncClient,
    gemini_pool: KeyPool | None,
    groq_pool: KeyPool | None,
    srt_content: str,
    max_retries: int = 2,
) -> str | None:
    """
    يترجم ملف SRT كاملاً إلى العربية على دفعات.
    يعيد المحتوى المترجم أو None عند الفشل الكامل.
    """
    if not (gemini_pool or groq_pool):
        return None

    blocks = parse_srt(srt_content)
    if not blocks:
        return None

    translated: list[str | None] = [None] * len(blocks)
    i = 0
    while i < len(blocks):
        batch = blocks[i : i + BATCH_SIZE]
        texts = [b["text"] for b in batch]
        success = False
        size = len(texts)

        for attempt in range(max_retries + 1):
            try:
                # عند الفشل السابق نقسم الدفعة
                if attempt > 0 and size > 8:
                    size = max(8, size // 2)
                    texts = texts[:size]
                    batch = batch[:size]

                lines = await _translate_batch(http, gemini_pool, groq_pool, texts)
                for j, line in enumerate(lines):
                    translated[i + j] = line
                i += len(lines)
                success = True
                break
            except Exception as e:
                log.warning("translate batch failed (attempt %s, size %s): %s", attempt + 1, size, e)
                await asyncio.sleep(0.8 * (attempt + 1))

        if not success:
            log.error("failed to translate batch starting at %s", i)
            return None  # حسب الخطة: لا نرسل ملفاً ناقصاً

    # بناء الملف النهائي
    out_blocks = []
    for idx, b in enumerate(blocks):
        out_blocks.append({
            "index": idx + 1,
            "timing": b["timing"],
            "text": translated[idx] or b["text"],
        })
    return build_srt(out_blocks)


def pick_source_candidate(candidates: list, preferred_langs: list[str] | None = None):
    """
    يختار أفضل مرشح للترجمة حسب أولوية اللغات.
    candidates: قائمة Candidate أو dict فيها lang.
    """
    order = preferred_langs or SOURCE_LANG_PRIORITY
    by_lang: dict[str, list] = {}
    for c in candidates:
        lang = (getattr(c, "lang", None) or c.get("lang") if isinstance(c, dict) else "") or ""
        lang = lang.lower()[:2]
        if lang == "ar":
            continue
        by_lang.setdefault(lang, []).append(c)

    for lang in order:
        if by_lang.get(lang):
            return by_lang[lang][0]
    # أي لغة أخرى
    for lang, items in by_lang.items():
        if items:
            return items[0]
    return None
