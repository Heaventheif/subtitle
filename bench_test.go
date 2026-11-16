// قياسات الأداء: المحلل، الترتيب، الكاش الساخن، والبحث الكامل عبر HTTP.
package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func BenchmarkParseFilename(b *testing.B) {
	names := []string{
		"The.Matrix.1999.1080p.BluRay.x264-GRP.mkv",
		"Breaking.Bad.S02E05.720p.WEB-DL.DDP5.1.Atmos.x264-[Qfilm.tv].mkv",
		"(2023) Oppenheimer.2160p.UHD.BluRay.x265.mkv",
	}
	for _, n := range names {
		b.Run(n[:12], func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				ParseFilename(n)
			}
		})
	}
}

func BenchmarkScore(b *testing.B) {
	p := ParseFilename("Movie.Name.2021.1080p.BluRay.x264-GRP.mkv")
	c := &Candidate{Provider: "opensubtitles", Ref: "1", Lang: "ar",
		Release: "Movie.Name.2021.1080p.BluRay.x264-GRP", Downloads: i64P(12034), Rating: f64P(8.5)}
	ctx := RankContext{VideoFps: f64P(23.976), Slangs: map[string]bool{"en": true}}
	for i := 0; i < b.N; i++ {
		Score(c, &p, ctx)
	}
}

func BenchmarkHotCacheGet(b *testing.B) {
	h := NewHotCache(3000)
	h.Set("k", SearchResponse{Tier: 1}, time.Minute)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.Get("k")
	}
}

// benchMock مزوّدون وهميون بلا حاجة لـ testing.T.
func benchMock() {
	m := http.NewServeMux()
	m.HandleFunc("/api/v1/login", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"token":"tok123"}`))
	})
	m.HandleFunc("/api/v1/subtitles", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Api-Key") == "oskey1" {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		if r.URL.Query().Get("moviehash") != "" {
			w.Write([]byte(`{"data":[{"attributes":{"language":"arabic","release":"Movie.Name.2021.1080p.BluRay.x264-GRP","files":[{"file_id":111}],"download_count":12034,"ratings":8.5,"moviehash_match":true,"fps":23.976}}]}`))
			return
		}
		w.Write([]byte(`{"data":[{"attributes":{"language":"arabic","release":"Movie.Name.2021.720p.WEBRip","files":[{"file_id":333}],"download_count":80,"moviehash_match":false}}]}`))
	})
	m.HandleFunc("/subdl/api/v1/subtitles", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":true,"subtitles":[{"url":"/subdl_1.zip","release_name":"Movie.Name.2021.1080p.BluRay.x264-SUBDL","lang":"ar"}]}`))
	})
	m.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		w.Write(arBytes)
	})
	base.Store(httptest.NewServer(m).URL)
}

// BenchmarkSearchCached سرعة الطلب الكامل من الكاش الساخن عبر HTTP حقيقي.
func BenchmarkSearchCached(b *testing.B) {
	benchMock()
	cfg := testConfig(mockBase(nil), b.TempDir()+"/cache.db")
	cfg.AppToken = ""
	srv := httptest.NewServer(NewServer(cfg, mustStore(cfg)).Handler())
	defer srv.Close()
	url := srv.URL + fmt.Sprintf("/search?filename=Movie.Name.2021.1080p.BluRay.x264-GRP.mkv&hash=%s&lang=ar,en", hashHex)
	// تدفئة الكاش
	resp, err := http.Get(url)
	if err != nil {
		b.Fatal(err)
	}
	resp.Body.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r, err := http.Get(url)
		if err != nil {
			b.Fatal(err)
		}
		r.Body.Close()
	}
}

// BenchmarkSearchUpstream الطلب الكامل مع مزوّدين وهميين بزمن استجابة 150ms (أدنى حد واقعي).
func BenchmarkSearchUpstream(b *testing.B) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		if r.URL.Path == "/api/v1/subtitles" {
			if r.URL.Query().Get("moviehash") != "" {
				w.Write([]byte(`{"data":[{"attributes":{"language":"arabic","release":"Movie.Name.2021.1080p.BluRay.x264-GRP","files":[{"file_id":111}],"download_count":12034,"ratings":8.5,"moviehash_match":true,"fps":23.976}}]}`))
				return
			}
			w.Write([]byte(`{"data":[{"attributes":{"language":"arabic","release":"Movie.Name.2021.720p.WEBRip","files":[{"file_id":333}],"download_count":80,"moviehash_match":false}}]}`))
			return
		}
		if r.Header.Get("Api-Key") == "oskey1" {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`{"status":true,"subtitles":[{"url":"/subdl_1.zip","release_name":"Movie.Name.2021.1080p.BluRay.x264-SUBDL","lang":"ar"}]}`))
	}))
	defer slow.Close()

	cfg := testConfig(slow.URL, b.TempDir()+"/cache.db")
	cfg.AppToken = ""
	srv := httptest.NewServer(NewServer(cfg, mustStore(cfg)).Handler())
	defer srv.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		url := srv.URL + fmt.Sprintf("/search?filename=Movie.Name.2021.1080p.BluRay.x264-GRP.mkv&hash=%s&lang=ar,en&refresh=1&i=%d", hashHex, i)
		r, err := http.Get(url)
		if err != nil {
			b.Fatal(err)
		}
		r.Body.Close()
	}
}

func mustStore(cfg Config) *Store {
	s, err := OpenStore(cfg.DBPath)
	if err != nil {
		panic(err)
	}
	return s
}
