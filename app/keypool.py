"""تدوير المفاتيح: يوزّع الطلبات على المفاتيح المتاحة ويتجاوز المستنفد تلقائياً."""
from __future__ import annotations

import hashlib
import time
from datetime import datetime, timedelta, timezone
from typing import Awaitable, Callable, TypeVar

from .db import Db

T = TypeVar("T")


class ProviderHTTPError(Exception):
    def __init__(self, status: int, message: str = "", retry_after: float | None = None):
        super().__init__(f"HTTP {status}: {message}")
        self.status = status
        self.retry_after = retry_after


class NoKeyAvailable(Exception):
    pass


def _today() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%d")


def _next_utc_midnight() -> float:
    now = datetime.now(timezone.utc)
    nxt = (now + timedelta(days=1)).replace(hour=0, minute=0, second=5, microsecond=0)
    return nxt.timestamp()


class KeyPool:
    def __init__(self, provider: str, keys: list[str], daily_limit: int, db: Db):
        self.provider = provider
        self.keys = list(dict.fromkeys(keys))
        self.daily_limit = daily_limit
        self.db = db
        for k in self.keys:
            db.execute(
                "INSERT OR IGNORE INTO api_keys(key_id, provider, day) VALUES(?,?,?)",
                (self._id(k), provider, _today()),
            )

    @staticmethod
    def _id(key: str) -> str:
        return hashlib.sha256(key.encode()).hexdigest()[:12]

    def __bool__(self) -> bool:
        return bool(self.keys)

    def _roll_day(self) -> None:
        self.db.execute(
            "UPDATE api_keys SET used_today=0, day=?, status='active', cooldown_until=0 "
            "WHERE provider=? AND day<>?",
            (_today(), self.provider, _today()),
        )

    def acquire(self, exclude: set[str] | None = None) -> str | None:
        """أقل مفتاح استخداماً ضمن حدّه اليومي وغير الموقوف مؤقتاً."""
        self._roll_day()
        now = time.time()
        best, best_used = None, None
        for k in self.keys:
            if exclude and k in exclude:
                continue
            row = self.db.one("SELECT used_today, cooldown_until FROM api_keys WHERE key_id=?", (self._id(k),))
            if row is None or row["cooldown_until"] > now or row["used_today"] >= self.daily_limit:
                continue
            if best is None or row["used_today"] < best_used:
                best, best_used = k, row["used_today"]
        return best

    def record_use(self, key: str) -> None:
        self.db.execute("UPDATE api_keys SET used_today=used_today+1 WHERE key_id=?", (self._id(key),))

    def report_failure(self, key: str, status: int, retry_after: float | None = None) -> None:
        if status in (429, 402):
            until = time.time() + retry_after if retry_after else _next_utc_midnight()
        elif status in (401, 403):
            until = time.time() + 6 * 3600
        else:
            return
        self.db.execute(
            "UPDATE api_keys SET status='cooldown', cooldown_until=?, last_error=? WHERE key_id=?",
            (until, f"http {status}", self._id(key)),
        )

    async def call(self, fn: Callable[[str], Awaitable[T]], count_use: bool = True) -> T:
        """ينفّذ fn بمفتاح؛ عند 429/402/401/403 يوقف المفتاح ويجرّب التالي."""
        tried: set[str] = set()
        while True:
            key = self.acquire(exclude=tried)
            if key is None:
                raise NoKeyAvailable(self.provider)
            tried.add(key)
            if count_use:
                self.record_use(key)
            try:
                return await fn(key)
            except ProviderHTTPError as e:
                self.report_failure(key, e.status, e.retry_after)
                if e.status in (401, 402, 403, 429):
                    continue
                raise

    def status(self) -> dict:
        self._roll_day()
        rows = self.db.all("SELECT used_today, status, cooldown_until FROM api_keys WHERE provider=?", (self.provider,))
        now = time.time()
        return {
            "keys": len(self.keys),
            "available": sum(1 for r in rows if r["cooldown_until"] <= now and r["used_today"] < self.daily_limit),
            "used_today": sum(r["used_today"] for r in rows),
        }
