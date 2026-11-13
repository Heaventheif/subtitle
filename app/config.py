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
    db_path: str = "data/cache.db"
    search_cache_hours: int = 24
    rate_limit_per_min: int = 60
    user_agent: str = "MediaPlayerSubs/1.0"
    # إبقاء الخادم مستيقظاً على Render المجاني (ينام بعد ~15 دقيقة بلا طلبات واردة)
    keep_alive_url: str = ""          # فارغ = معطّل (محلياً وفي الاختبارات)
    keep_alive_interval: int = 600    # ثوانٍ؛ أقل من 15 دقيقة بهامش أمان

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
            firecrawl_keys=_list("FIRECRAWL_KEYS"),
            enable_scraping=os.getenv("ENABLE_SCRAPING", "0") == "1",
            db_path=os.getenv("DB_PATH", "data/cache.db"),
            search_cache_hours=int(os.getenv("SEARCH_CACHE_HOURS", "24")),
            # Render يحقن RENDER_EXTERNAL_URL تلقائياً؛ KEEP_ALIVE_URL لتجاوزه يدوياً
            keep_alive_url=(os.getenv("KEEP_ALIVE_URL") or os.getenv("RENDER_EXTERNAL_URL", "")).rstrip("/"),
            keep_alive_interval=max(60, int(os.getenv("KEEP_ALIVE_INTERVAL", "600"))),
        )
