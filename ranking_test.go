// اختبارات الترتيب: البصمة = 100 حصراً، ثم تطابق الاسم/السنة/الوسوم.
package main

import (
	"testing"
)

func strP(s string) *string   { return &s }
func intP(i int) *int         { return &i }
func i64P(i int64) *int64     { return &i }
func f64P(f float64) *float64 { return &f }

func TestScoreHashMatchAlways100(t *testing.T) {
	c := &Candidate{Provider: "opensubtitles", Ref: "1", Lang: "ar", Release: "Totally.Unrelated.Name",
		HashMatch: true}
	p := ParseFilename("The.Matrix.1999.1080p.BluRay.x264-GRP.mkv")
	if s := Score(c, &p, RankContext{}); s != 100 {
		t.Fatalf("score = %d, want 100", s)
	}
}

func TestScoreNameMatchOrdering(t *testing.T) {
	p := ParseFilename("Movie.Name.2021.1080p.BluRay.x264-GRP.mkv")
	good := &Candidate{Provider: "opensubtitles", Ref: "1", Lang: "ar",
		Release: "Movie.Name.2021.1080p.BluRay.x264-GRP", Downloads: i64P(12000)}
	bad := &Candidate{Provider: "subdl", Ref: "2", Lang: "ar",
		Release: "Some.Other.Movie.720p.WEBRip", Downloads: i64P(5)}
	rg := Rank([]*Candidate{bad, good}, &p, RankContext{})
	if rg[0].C.Ref != "1" {
		t.Fatalf("best = %s, want the exact release", rg[0].C.Ref)
	}
	if rg[0].Match <= rg[1].Match {
		t.Fatalf("match ordering broken: %d vs %d", rg[0].Match, rg[1].Match)
	}
	if rg[0].Match >= 100 || rg[1].Match >= 100 {
		t.Fatalf("only hash matches may reach 100")
	}
}

func TestScoreSlangsPenalty(t *testing.T) {
	p := ParseFilename("Movie.Name.2021.1080p.mkv")
	c := &Candidate{Provider: "x", Ref: "1", Lang: "en", Release: "Movie.Name.2021.1080p"}
	ctx := RankContext{Slangs: map[string]bool{"en": true}}
	without := Score(c, &p, RankContext{})
	with := Score(c, &p, ctx)
	if with >= without {
		t.Fatalf("slangs must penalize: %d vs %d", with, without)
	}
}

func TestScoreSeasonEpisodeMatch(t *testing.T) {
	p := ParseFilename("Show.Name.S02E05.1080p.mkv")
	match := &Candidate{Provider: "x", Ref: "1", Lang: "ar", Release: "Show.Name.S02E05.1080p.WEB-DL"}
	other := &Candidate{Provider: "x", Ref: "2", Lang: "ar", Release: "Show.Name.S03E09.1080p.WEB-DL"}
	sm, so := Score(match, &p, RankContext{}), Score(other, &p, RankContext{})
	if sm <= so {
		t.Fatalf("exact episode must score higher: %d vs %d", sm, so)
	}
}

func TestScoreAiTranslatedPenalty(t *testing.T) {
	p := ParseFilename("Movie.Name.2021.1080p.mkv")
	human := &Candidate{Provider: "x", Ref: "1", Lang: "ar", Release: "Movie.Name.2021.1080p"}
	machine := &Candidate{Provider: "x", Ref: "2", Lang: "ar", Release: "Movie.Name.2021.1080p", AiTranslated: true}
	if Score(machine, &p, RankContext{}) >= Score(human, &p, RankContext{}) {
		t.Fatalf("machine-translated must score lower")
	}
}
