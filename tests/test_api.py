import httpx
from fastapi.testclient import TestClient

from app.config import Settings
from app.main import create_app
from app.parser import parse_filename
from app.textfix import decode_text, normalize_subtitle

ARABIC = "1\n00:00:01,000 --> 00:00:03,000\nمرحبا بك في الفيلم\n\n"


def make_transport(state):
    def handler(req: httpx.Request) -> httpx.Response:
        u = str(req.url)
        if "themoviedb.org/3/search/movie" in u:
            return httpx.Response(200, json={"results": [{"id": 603, "title": "The Matrix", "release_date": "1999-03-31"}]})
        if "external_ids" in u:
            return httpx.Response(200, json={"imdb_id": "tt0133093"})
        if "sub.wyzie.io/search" in u:
            state["wyzie_calls"].append(req.url.params["key"])
            if req.url.params["key"] == "k1":
                return httpx.Response(429, json={"error": "daily limit"})
            return httpx.Response(200, json=[
                {"url": "https://sub.wyzie.io/c/abc.srt", "language": "ar", "format": "srt",
                 "release": "The.Matrix.1999.1080p.BluRay.x264-GRP", "downloads": 1000},
                {"url": "https://sub.wyzie.io/c/def.srt", "language": "ar", "format": "srt",
                 "release": "Matrix.720p.WEBRip", "downloads": 5},
            ])
        if u.endswith("/c/abc.srt"):
            return httpx.Response(200, content=ARABIC.encode("cp1256"))
        return httpx.Response(404)
    return httpx.MockTransport(handler)


def client_for(state):
    st = Settings(app_token="tok", tmdb_key="t", wyzie_keys=["k1", "k2"], db_path=":memory:")
    return TestClient(create_app(st, make_transport(state)), headers={"X-App-Token": "tok"})


def test_parser():
    p = parse_filename("The.Matrix.1999.1080p.BluRay.x264-GRP.mkv")
    assert p["title"] == "The Matrix" and p["year"] == 1999
    assert {"1080p", "bluray", "x264", "grp"} <= set(p["tokens"])
    t = parse_filename("Breaking.Bad.S02E05.720p.WEB-DL.mkv")
    assert t["title"] == "Breaking Bad" and (t["season"], t["episode"]) == (2, 5)


def test_cp1256_to_utf8():
    text, enc = decode_text(ARABIC.encode("cp1256"), "ar")
    assert enc == "cp1256" and "مرحبا" in text
    out, fmt, _ = normalize_subtitle(ARABIC.encode("cp1256"), "ar")
    assert fmt == "srt" and "مرحبا بك" in out.decode("utf-8")


def test_search_rotation_cache_and_file():
    state = {"wyzie_calls": []}
    with client_for(state) as c:
        r = c.get("/search", params={"filename": "The.Matrix.1999.1080p.BluRay.x264-GRP.mkv"})
        assert r.status_code == 200, r.text
        j = r.json()
        assert j["resolved"]["imdb_id"] == "tt0133093"
        assert len(j["results"]) == 2
        best = j["results"][0]
        assert best["lang"] == "ar" and best["match"] > j["results"][1]["match"]
        assert "k1" in state["wyzie_calls"] and "k2" in state["wyzie_calls"]   # تدوير بعد 429
        f = c.get(f"/file/{best['id']}")
        assert f.status_code == 200 and "مرحبا بك" in f.content.decode("utf-8")
        r2 = c.get("/search", params={"filename": "The.Matrix.1999.1080p.BluRay.x264-GRP.mkv"})
        assert r2.json()["cached"] is True
        assert c.get("/health").json()["pools"]["wyzie"]["keys"] == 2


def test_auth():
    state = {"wyzie_calls": []}
    st = Settings(app_token="tok", db_path=":memory:")
    with TestClient(create_app(st, make_transport(state))) as c:
        assert c.get("/search?q=x").status_code == 401
        assert c.get("/health").status_code == 200
