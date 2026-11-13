"""قاعدة SQLite البسيطة: حالة المفاتيح + الكاش + ملفات الترجمة."""
from __future__ import annotations

import os
import sqlite3
import threading
import time

SCHEMA = """
CREATE TABLE IF NOT EXISTS api_keys(
  key_id TEXT PRIMARY KEY,
  provider TEXT NOT NULL,
  used_today INTEGER NOT NULL DEFAULT 0,
  day TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'active',      -- active | cooldown
  cooldown_until REAL NOT NULL DEFAULT 0,
  last_error TEXT
);
CREATE TABLE IF NOT EXISTS title_map(
  name_hash TEXT PRIMARY KEY,
  tmdb_id INTEGER, imdb_id TEXT, kind TEXT, title TEXT, year INTEGER,
  created_at REAL NOT NULL
);
CREATE TABLE IF NOT EXISTS search_cache(
  query_hash TEXT PRIMARY KEY,
  payload TEXT NOT NULL,
  created_at REAL NOT NULL,
  expires_at REAL NOT NULL
);
CREATE TABLE IF NOT EXISTS subtitle_files(
  uid TEXT PRIMARY KEY,
  provider TEXT NOT NULL,
  ref TEXT NOT NULL,
  lang TEXT, release TEXT, rating REAL, downloads INTEGER,
  fmt TEXT, hi INTEGER DEFAULT 0, hash_match INTEGER DEFAULT 0,
  blob BLOB, size INTEGER, encoding TEXT,
  created_at REAL NOT NULL
);
"""


class Db:
    def __init__(self, path: str):
        if path != ":memory:":
            os.makedirs(os.path.dirname(path) or ".", exist_ok=True)
        self.conn = sqlite3.connect(path, check_same_thread=False)
        self.conn.row_factory = sqlite3.Row
        self.lock = threading.Lock()
        with self.lock:
            self.conn.executescript(SCHEMA)
            self.conn.commit()

    def execute(self, sql: str, args: tuple = ()) -> None:
        with self.lock:
            self.conn.execute(sql, args)
            self.conn.commit()

    def one(self, sql: str, args: tuple = ()):
        with self.lock:
            return self.conn.execute(sql, args).fetchone()

    def all(self, sql: str, args: tuple = ()):
        with self.lock:
            return self.conn.execute(sql, args).fetchall()

    def purge_expired(self) -> None:
        self.execute("DELETE FROM search_cache WHERE expires_at < ?", (time.time(),))
