"""إعدادات الخادم من متغيرات البيئة."""
from __future__ import annotations

import json
import os
from dataclasses import dataclass, field


def _list(name: str) -> list[str]:
    return [x.strip() for x in os.getenv(name, "").split(",") if x.strip()]


def _accounts() -> list[dict]:
    raw = os.getenv("OPENSUBTITLES_ACCOUNTS", "").strip()
    if not raw:
        return []
    try:
        data = json.loads(raw)
        return [a for a in data if a.get("api_key") and a.get("username") and a.get("password")]
    except Exception:
        return []


@dataclass
class Settings:
    app_token: str = ""
    tmdb_key: str = ""
    wyzie_keys: list[str] = field(default_factory=list)
    wyzie_daily_limit: int = 1000
    os_accounts: list[dict] = field(default_factory=list)
    os_daily_limit: int = 200
    subdl_keys: list[str] = field(default_factory=list)
    subdl_daily_limit: int = 500
    firecrawl_keys: list[str] = field(default_factory=list)
    enable_scraping: bool = False

    # ── مفاتيح الذكاء الاصطناعي (مفصولة بفواصل — تدوير تلقائي) ──
    gemini_keys: list[str] = field(default_factory=list)
    gemini_daily_limit: int = 1500
    groq_keys: list[str] = field(default_factory=list)
    groq_daily_limit: int = 1000
    enable_ai_translate: bool = True
    enable_ai_title_clean: bool = True

    db_path: str = "data/cache.db"
    search_cache_hours: int = 720          # 30 يوماً (حسب الخطة)
    rate_limit_per_min: int = 60
    user_agent: str = "MediaPlayerSubs/1.0"
    # إبقاء الخادم مستيقظاً على Render المجاني
    keep_alive_url: str = ""
    keep_alive_interval: int = 600

    @classmethod
    def from_env(cls) -> "Settings":
        return cls(
            app_token=os.getenv("APP_TOKEN", ""),
            tmdb_key=os.getenv("TMDB_API_KEY", ""),
            wyzie_keys=_list("WYZIE_KEYS"),
            wyzie_daily_limit=int(os.getenv("WYZIE_DAILY_LIMIT", "1000")),
            os_accounts=_accounts(),
            os_daily_limit=int(os.getenv("OPENSUBTITLES_DAILY_LIMIT", "200")),
            subdl_keys=_list("SUBDL_API_KEYS"),
            subdl_daily_limit=int(os.getenv("SUBDL_DAILY_LIMIT", "500")),
            firecrawl_keys=_list("FIRECRAWL_KEYS") or _list("FIRECRUL_API"),
            enable_scraping=os.getenv("ENABLE_SCRAPING", "0") == "1",
            # AI keys — تدعم عدة مفاتيح مفصولة بفاصلة
            gemini_keys=_list("GEMINI_API_KEYS") or _list("GEMINI_API_KEY"),
            gemini_daily_limit=int(os.getenv("GEMINI_DAILY_LIMIT", "1500")),
            groq_keys=_list("GROQ_API_KEYS") or _list("GROQ_API_KEY"),
            groq_daily_limit=int(os.getenv("GROQ_DAILY_LIMIT", "1000")),
            enable_ai_translate=os.getenv("ENABLE_AI_TRANSLATE", "1") == "1",
            enable_ai_title_clean=os.getenv("ENABLE_AI_TITLE_CLEAN", "1") == "1",
            db_path=os.getenv("DB_PATH", "data/cache.db"),
            search_cache_hours=int(os.getenv("SEARCH_CACHE_HOURS", "720")),
            keep_alive_url=(os.getenv("KEEP_ALIVE_URL") or os.getenv("RENDER_EXTERNAL_URL", "")).rstrip("/"),
            keep_alive_interval=max(60, int(os.getenv("KEEP_ALIVE_INTERVAL", "600"))),
        )
