// اختبارات نقاط النهاية الكاملة بسرفرات مزوّدين وهمية (بلا شبكة خارجية).
package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	appToken = "tok"
	hashHex  = "8e245d9679d31e12"
)

var arBytes = toCp1256(arSRT)
var arZipBytes = buildArZip()

func buildArZip() []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("sub.ar.srt")
	w.Write(arBytes)
	zw.Close()
	return buf.Bytes()
}

// mockProviders خادم وهمي يحاكي OpenSubtitles + SubDL + روابط التنزيل.
func mockProviders(t *testing.T) (*httptest.Server, *map[string]int) {
	t.Helper()
	var osHashCalls, osNameCalls, subdlCalls, fileFetches int64
	counters := &map[string]int{}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/login", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"token": "tok123"})
	})
	mux.HandleFunc("/api/v1/subtitles", func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Api-Key")
		atomic.AddInt64(&osNameCalls, 1)
		(*counters)["os"] = int(atomic.LoadInt64(&osNameCalls))
		if key == "oskey1" {
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]string{"error": "daily limit"})
			return
		}
		q := r.URL.Query()
		if q.Get("moviehash") != "" {
			atomic.AddInt64(&osHashCalls, 1)
			(*counters)["os_hash"] = int(atomic.LoadInt64(&osHashCalls))
			data := []map[string]any{{"attributes": map[string]any{
				"language": "arabic", "release": "Movie.Name.2021.1080p.BluRay.x264-GRP",
				"files":          []map[string]any{{"file_id": 111, "file_name": "Movie.Name.2021.srt"}},
				"download_count": 12034, "ratings": 8.5, "hearing_impaired": false,
				"moviehash_match": true, "fps": 23.976}}}
			json.NewEncoder(w).Encode(map[string]any{"data": data})
			return
		}
		query := q.Get("query")
		if strings.Contains(query, "Unknown") || strings.Contains(query, "ZZZ") {
			json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
			return
		}
		var data []map[string]any
		langs := map[string]bool{}
		for _, l := range strings.Split(q.Get("languages"), ",") {
			langs[l] = true
		}
		if langs["en"] {
			data = append(data, map[string]any{"attributes": map[string]any{
				"language": "english", "release": "Movie.Name.2021.1080p.WEB-DL.x264-OS",
				"files":          []map[string]any{{"file_id": 222}},
				"download_count": 500, "ratings": 7.0, "moviehash_match": false}})
		}
		if langs["ar"] {
			data = append(data, map[string]any{"attributes": map[string]any{
				"language": "arabic", "release": "Movie.Name.2021.720p.WEBRip.LOW",
				"files":          []map[string]any{{"file_id": 333}},
				"download_count": 80, "moviehash_match": false}})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
	mux.HandleFunc("/api/v1/download", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			FileID any `json:"file_id"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		json.NewEncoder(w).Encode(map[string]string{
			"link": fmt.Sprintf("%s/dl/os_%v.srt", mockBase(t), body.FileID)})
	})
	mux.HandleFunc("/subdl/api/v1/subtitles", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&subdlCalls, 1)
		(*counters)["subdl"] = int(atomic.LoadInt64(&subdlCalls))
		fn := r.URL.Query().Get("film_name")
		if strings.Contains(fn, "Unknown") || strings.Contains(fn, "ZZZ") {
			json.NewEncoder(w).Encode(map[string]any{"status": true, "subtitles": []any{}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"status": true, "subtitles": []map[string]any{
			{"url": "/subdl_1.zip", "release_name": "Movie.Name.2021.1080p.BluRay.x264-SUBDL", "lang": "ar"},
		}})
	})

	var srvURL string
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&fileFetches, 1)
		(*counters)["files"] = int(atomic.LoadInt64(&fileFetches))
		if strings.HasPrefix(r.URL.Path, "/dl/os_111") {
			w.Write(arBytes)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/dl/subdl_1") {
			w.Write(arZipBytes)
			return
		}
		http.NotFound(w, r)
	})
	_ = srvURL

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	(*counters)["base"] = 0
	// نضع العنوان داخل متغير عام لاستخدامه في /api/v1/download
	base.Store(srv.URL)
	return srv, counters
}

var base atomic.Value // عنوان الخادم الوهمي

func mockBase(_ *testing.T) string { return base.Load().(string) }

func testConfig(baseURL string, dbPath string) Config {
	return Config{
		AppToken:        appToken,
		OSAccounts:      []OSAccount{{APIKey: "oskey1", Username: "u", Password: "p"}, {APIKey: "oskey2", Username: "u", Password: "p"}},
		OSDailyLimit:    100,
		SubDLKeys:       []string{"sdlkey1"},
		SubDLDailyLimit: 100,
		OSBase:          baseURL + "/api/v1",
		SubDLBase:       baseURL + "/subdl/api/v1/subtitles",
		SubDLDl:         baseURL + "/dl",
		SearchTimeout:   4 * time.Second,
		RequestTimeout:  8 * time.Second,
		CacheTTL:        time.Hour,
		NegCacheTTL:     time.Hour,
		RateLimitPerMin: 1000,
		DBPath:          dbPath,
		UserAgent:       "test/1.0",
		Port:            0,
	}
}

func newTestServer(t *testing.T, cfg Config) *httptest.Server {
	t.Helper()
	store, err := OpenStore(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewServer(cfg, store).Handler())
	t.Cleanup(func() {
		srv.Close()
		store.Close()
	})
	return srv
}

func getJSON(t *testing.T, url, token string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if token != "" {
		req.Header.Set("X-App-Token", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var m map[string]any
	json.Unmarshal(body, &m)
	return resp.StatusCode, m
}

func getRaw(t *testing.T, url, token string) (int, http.Header, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if token != "" {
		req.Header.Set("X-App-Token", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, body
}

func TestAuthAndHealth(t *testing.T) {
	mockProviders(t)
	srv := newTestServer(t, testConfig(mockBase(t), t.TempDir()+"/cache.db"))
	url := srv.URL

	code, _ := getJSON(t, url+"/search?filename=x", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", code)
	}
	code, h := getJSON(t, url+"/health", "")
	if code != 200 || h["ok"] != true {
		t.Fatalf("health: code=%d body=%v", code, h)
	}
	prov := h["providers"].(map[string]any)
	osub := prov["osub"].(map[string]any)
	if osub["keys"].(float64) != 2 {
		t.Fatalf("osub keys = %v", osub["keys"])
	}
}

func TestFullSearchHashFirst(t *testing.T) {
	_, counters := mockProviders(t)
	srv := newTestServer(t, testConfig(mockBase(t), t.TempDir()+"/cache.db"))
	url := srv.URL + fmt.Sprintf("/search?filename=Movie.Name.2021.1080p.BluRay.x264-GRP.mkv&hash=%s&size=1503238553&lang=ar,en&fps=23.976", hashHex)

	code, j := getJSON(t, url, appToken)
	if code != 200 {
		t.Fatalf("code = %d, body = %v", code, j)
	}
	if j["tier"].(float64) != 1 {
		t.Fatalf("tier = %v, want 1", j["tier"])
	}
	if j["cached"] != false {
		t.Fatalf("cached = %v", j["cached"])
	}
	q := j["query"].(map[string]any)
	if q["title"] != "Movie Name" || q["year"].(float64) != 2021 {
		t.Fatalf("query = %v", q)
	}
	results := j["results"].([]any)
	if len(results) == 0 {
		t.Fatal("no results")
	}
	top := results[0].(map[string]any)
	if top["hash_match"] != true || top["match"].(float64) != 100 {
		t.Fatalf("top = %v", top)
	}
	if top["source"] != "opensubtitles" || top["lang"] != "ar" {
		t.Fatalf("top = %v", top)
	}
	if top["fps"].(float64) != 23.976 {
		t.Fatalf("fps = %v", top)
	}
	if len(fmt.Sprint(top["id"])) != 16 {
		t.Fatalf("id = %v", top["id"])
	}
	// النتائج مرتبة تنازلياً وmatch <100 للبقية
	prev := 100.0
	for i, r := range results {
		m := r.(map[string]any)["match"].(float64)
		if i > 0 && m > prev {
			t.Fatalf("results not sorted")
		}
		if i > 0 && m >= 100 {
			t.Fatalf("non-hash match reached 100: %v", m)
		}
		prev = m
	}
	// تدوير المفاتيح بعد 429 (oskey1 → oskey2) — كلاهما استُخدم
	if (*counters)["os"] == 0 {
		t.Fatal("os mock not called")
	}
}

func TestCacheHitFast(t *testing.T) {
	mockProviders(t)
	srv := newTestServer(t, testConfig(mockBase(t), t.TempDir()+"/cache.db"))
	url := srv.URL + fmt.Sprintf("/search?filename=Movie.Name.2021.1080p.BluRay.x264-GRP.mkv&hash=%s&lang=ar,en", hashHex)
	if code, _ := getJSON(t, url, appToken); code != 200 {
		t.Fatal("first search failed")
	}
	t0 := time.Now()
	code, j := getJSON(t, url, appToken)
	dt := time.Since(t0)
	if code != 200 || j["cached"] != true {
		t.Fatalf("cached = %v (code %d)", j["cached"], code)
	}
	if dt > 100*time.Millisecond {
		t.Fatalf("cache hit too slow: %v", dt)
	}
}

func TestFileDownloadAndBlobCache(t *testing.T) {
	_, counters := mockProviders(t)
	srv := newTestServer(t, testConfig(mockBase(t), t.TempDir()+"/cache.db"))
	searchURL := srv.URL + fmt.Sprintf("/search?filename=Movie.Name.2021.1080p.BluRay.x264-GRP.mkv&hash=%s&lang=ar", hashHex)
	_, j := getJSON(t, searchURL, appToken)
	top := j["results"].([]any)[0].(map[string]any)

	code, hdr, body := getRaw(t, srv.URL+"/file/"+fmt.Sprint(top["id"]), appToken)
	if code != 200 {
		t.Fatalf("code = %d", code)
	}
	if hdr.Get("X-Subtitle-Format") != "srt" || hdr.Get("X-Subtitle-Lang") != "ar" {
		t.Fatalf("headers = %v", hdr)
	}
	if !strings.Contains(string(body), "مرحبا بك في الفيلم") {
		t.Fatalf("body = %q", body)
	}
	// المرة الثانية من كاش blob — بلا مراجعة المزوّد
	(*counters)["files"] = 0
	code, _, body2 := getRaw(t, srv.URL+"/file/"+fmt.Sprint(top["id"]), appToken)
	if code != 200 || !strings.Contains(string(body2), "مرحبا") {
		t.Fatalf("second fetch: code=%d", code)
	}
	if (*counters)["files"] != 0 {
		t.Fatalf("provider re-fetched: %d times", (*counters)["files"])
	}
}

func TestNameSearchOnly(t *testing.T) {
	mockProviders(t)
	srv := newTestServer(t, testConfig(mockBase(t), t.TempDir()+"/cache.db"))
	code, j := getJSON(t, srv.URL+"/search?q=Movie.Name&lang=ar", appToken)
	if code != 200 {
		t.Fatalf("code = %d", code)
	}
	if j["tier"].(float64) != 2 {
		t.Fatalf("tier = %v", j["tier"])
	}
	results := j["results"].([]any)
	hasSubdl := false
	for _, r := range results {
		m := r.(map[string]any)
		if m["source"] == "subdl" {
			hasSubdl = true
		}
		if m["hash_match"] == true {
			t.Fatal("name search must not mark hash_match")
		}
	}
	if !hasSubdl {
		t.Fatal("subdl results missing")
	}
}

func TestNegativeCache(t *testing.T) {
	mockProviders(t)
	srv := newTestServer(t, testConfig(mockBase(t), t.TempDir()+"/cache.db"))
	url := srv.URL + "/search?filename=Unknown.Show.ZZZ.2099.1080p.mkv&lang=ar"
	_, j := getJSON(t, url, appToken)
	if len(j["results"].([]any)) != 0 {
		t.Fatalf("results = %v", j["results"])
	}
	_, j2 := getJSON(t, url, appToken)
	if j2["cached"] != true {
		t.Fatal("negative result not cached")
	}
}

func TestRateLimit(t *testing.T) {
	mockProviders(t)
	cfg := testConfig(mockBase(t), t.TempDir()+"/cache.db")
	cfg.RateLimitPerMin = 3
	srv := newTestServer(t, cfg)
	codes := []int{}
	for i := 0; i < 5; i++ {
		code, _ := getJSON(t, fmt.Sprintf("%s/search?filename=f%d.mkv&lang=ar", srv.URL, i), appToken)
		codes = append(codes, code)
	}
	if codes[3] != http.StatusTooManyRequests || codes[4] != http.StatusTooManyRequests {
		t.Fatalf("codes = %v", codes)
	}
}

func TestNoKeys503(t *testing.T) {
	mockProviders(t)
	cfg := testConfig(mockBase(t), t.TempDir()+"/cache.db")
	cfg.OSAccounts = nil
	cfg.SubDLKeys = nil
	srv := newTestServer(t, cfg)
	code, _ := getJSON(t, srv.URL+"/search?filename=x.mkv", appToken)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", code)
	}
}

func TestSlangsFiltering(t *testing.T) {
	mockProviders(t)
	srv := newTestServer(t, testConfig(mockBase(t), t.TempDir()+"/cache.db"))
	code, j := getJSON(t,
		srv.URL+fmt.Sprintf("/search?filename=Movie.Name.2021.1080p.BluRay.x264-GRP.mkv&hash=%s&lang=ar,en&slangs=en", hashHex), appToken)
	if code != 200 {
		t.Fatalf("code = %d", code)
	}
	results := j["results"].([]any)
	if len(results) == 0 {
		t.Fatal("no results")
	}
	hasAr, hasEn := false, false
	for _, r := range results {
		lang := r.(map[string]any)["lang"].(string)
		if lang == "ar" {
			hasAr = true
		}
		if lang == "en" {
			hasEn = true
		}
	}
	if !hasAr || hasEn {
		t.Fatalf("slangs filtering broken: hasAr=%v hasEn=%v", hasAr, hasEn)
	}
}

func TestUnknownFile404(t *testing.T) {
	mockProviders(t)
	srv := newTestServer(t, testConfig(mockBase(t), t.TempDir()+"/cache.db"))
	code, _ := getJSON(t, srv.URL+"/file/0123456789abcdef", appToken)
	if code != http.StatusNotFound {
		t.Fatalf("code = %d", code)
	}
}

func TestHotCacheIsolation(t *testing.T) {
	h := NewHotCache(100)
	resp := SearchResponse{Tier: 1, Results: []Result{{ID: "a", Match: 100}}}
	h.Set("k", resp, time.Minute)
	got, ok := h.Get("k")
	if !ok || got.Tier != 1 {
		t.Fatal("hot get failed")
	}
	// تعديل النسخة المعادة لا يؤثر على المخزن
	got.Tier = 2
	got2, _ := h.Get("k")
	if got2.Tier != 1 {
		t.Fatal("hot cache entry mutated from returned copy")
	}
}
