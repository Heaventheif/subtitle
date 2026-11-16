package main

import (
	"archive/zip"
	"bytes"
	"net/http"
	"testing"
	"time"
)

func TestZipBombRejected(t *testing.T) {
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	w, _ := zw.Create("x.srt")
	w.Write(bytes.Repeat([]byte("a"), 8<<20))
	zw.Close()
	if _, _, err := ExtractSubtitle(b.Bytes()); err == nil {
		t.Fatal("oversized zip entry must be rejected")
	}
}

func TestZipFormatLowercase(t *testing.T) {
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	w, _ := zw.Create("A.SRT")
	w.Write([]byte("1\n"))
	zw.Close()
	if _, f, _ := ExtractSubtitle(b.Bytes()); f != "srt" {
		t.Fatalf("got %q", f)
	}
}

func TestUTF16Surrogates(t *testing.T) {
	if got := decodeUTF16([]byte{0xff, 0xfe, 0x3d, 0xd8, 0x00, 0xde}); got != "😀" {
		t.Fatalf("got %q", got)
	}
}

func TestTurkishDecode(t *testing.T) {
	got, _ := DecodeText([]byte{0xfe, 0xf0, 0xfd}, "tr")
	if got != "şğı" {
		t.Fatalf("got %q", got)
	}
}

func TestZeroPaddedEpisode(t *testing.T) {
	p := ParseFilename("Show.S01E05.1080p.WEB-DL-GRP.mkv")
	a := Score(&Candidate{Release: "Show 1x05 1080p"}, &p, RankContext{})
	b := Score(&Candidate{Release: "Show 1x5 1080p"}, &p, RankContext{})
	if a != b {
		t.Fatalf("1x05=%d 1x5=%d", a, b)
	}
}

func TestClientIPUsesLastXFF(t *testing.T) {
	r, _ := http.NewRequest("GET", "/", nil)
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 1.2.3.4")
	if ip := clientIP(r); ip != "1.2.3.4" {
		t.Fatalf("got %s", ip)
	}
}

func TestRateLimitNotResetBySweep(t *testing.T) {
	s := &Server{cfg: Config{RateLimitPerMin: 2}, hits: map[string][]int64{}}
	s.rateOk("a")
	s.rateOk("a")
	for i := 0; i < 6000; i++ {
		s.rateOk(time.Duration(i).String() + "x")
	}
	if s.rateOk("a") {
		t.Fatal("limit was reset by map flush")
	}
}

func TestPurgeExpiredDeletesAll(t *testing.T) {
	st, _ := OpenStore(":memory:")
	for _, k := range []string{"a", "b", "c", "d"} {
		st.PutSearch(k, SearchResponse{}, -time.Hour)
	}
	st.PutSearch("live", SearchResponse{}, time.Hour)
	st.PurgeExpired()
	n := 0
	for _, k := range []string{"a", "b", "c", "d"} {
		if _, ok := st.GetSearch(k); ok {
			n++
		}
	}
	if _, ok := st.GetSearch("live"); !ok || n != 0 {
		t.Fatal("purge wrong")
	}
}
