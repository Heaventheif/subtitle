// اختبارات المحلل — حالات حقيقية من أسماء ملفات شائعة.
package main

import (
	"testing"
)

func eqInt(t *testing.T, got *int, want int) {
	t.Helper()
	if got == nil {
		t.Fatalf("expected %d, got nil", want)
	}
	if *got != want {
		t.Fatalf("expected %d, got %d", want, *got)
	}
}

func TestParseMovie(t *testing.T) {
	p := ParseFilename("The.Matrix.1999.1080p.BluRay.x264-GRP.mkv")
	if p.Title != "The Matrix" {
		t.Fatalf("title = %q", p.Title)
	}
	eqInt(t, p.Year, 1999)
	if p.Season != nil || p.Episode != nil {
		t.Fatalf("season/episode should be nil")
	}
	tokens := map[string]bool{}
	for _, tk := range p.Tokens {
		tokens[tk] = true
	}
	for _, want := range []string{"1080p", "bluray", "x264", "grp"} {
		if !tokens[want] {
			t.Fatalf("missing token %q in %v", want, p.Tokens)
		}
	}
}

func TestParseEpisodeSxxEyy(t *testing.T) {
	p := ParseFilename("Breaking.Bad.S02E05.720p.WEB-DL.mkv")
	if p.Title != "Breaking Bad" {
		t.Fatalf("title = %q", p.Title)
	}
	eqInt(t, p.Season, 2)
	eqInt(t, p.Episode, 5)
}

func TestParseEpisodeNxNN(t *testing.T) {
	p := ParseFilename("Friends.2x05.720p.mkv")
	if p.Title != "Friends" {
		t.Fatalf("title = %q", p.Title)
	}
	eqInt(t, p.Season, 2)
	eqInt(t, p.Episode, 5)
}

func TestParseTitleWithYearInside(t *testing.T) {
	p := ParseFilename("Blade.Runner.2049.2017.1080p.BluRay.mkv")
	if p.Title != "Blade Runner 2049" {
		t.Fatalf("title = %q", p.Title)
	}
	eqInt(t, p.Year, 2017)
}

func TestParseSiteNoise(t *testing.T) {
	p := ParseFilename("[Qfilm.tv]Movie.Name.2021.1080p.mkv")
	if p.Title != "Movie Name" {
		t.Fatalf("title = %q", p.Title)
	}
	p2 := ParseFilename("Movie.Name.2021.1080p.WEB-DL.www.MovSite.com.mkv")
	if p2.Title != "Movie Name" {
		t.Fatalf("title2 = %q", p2.Title)
	}
	if got := stripSiteNoise("www.TestSite.com - Movie.mkv"); got != "Movie.mkv" {
		t.Fatalf("stripSiteNoise = %q", got)
	}
}

func TestParseBracketsThenTitle(t *testing.T) {
	p := ParseFilename("(2023) Oppenheimer 2160p UHD.mkv")
	if p.Title != "Oppenheimer" {
		t.Fatalf("title = %q", p.Title)
	}
	eqInt(t, p.Year, 2023)
}

func TestParseApostrophe(t *testing.T) {
	p := ParseFilename("Don't.Look.Up.2021.1080p.NF.WEB-DL.DDP5.1.Atmos.x264-ETRG.mkv")
	if p.Title != "Don't Look Up" {
		t.Fatalf("title = %q", p.Title)
	}
	eqInt(t, p.Year, 2021)
}

func TestParseSeasonOnly(t *testing.T) {
	p := ParseFilename("True.Detective.S01.1080p.WEB-DL.mkv")
	if p.Title != "True Detective" {
		t.Fatalf("title = %q", p.Title)
	}
	eqInt(t, p.Season, 1)
}

func TestParseEmpty(t *testing.T) {
	p := ParseFilename("")
	if p.Title != "" || p.Year != nil {
		t.Fatalf("empty parse = %+v", p)
	}
}
