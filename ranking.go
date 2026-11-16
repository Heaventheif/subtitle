// الترتيب الذكي السريع: تطابق البصمة = 100 دائماً، ثم تطابق الاسم/الإصدار/السنة/الموسم.
package main

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
)

var seGenericRx = regexp.MustCompile(`(?i)\bS\d{1,2}\s?[._-]?E\d{1,3}\b|\b\d{1,2}x\d{2,3}\b`)
var anyYearRx = regexp.MustCompile(`(19|20)\d{2}`)

// كاش regex الـ موسم/حلقة ووسوم الإصدار — يمنع إعادة التجميع في كل تقييم
var (
	seRxMu      sync.RWMutex
	seRxCache   = map[string]*regexp.Regexp{}
	relTokMu    sync.RWMutex
	relTokCache = map[string][]string{}
	relTokCap   = 8192
)

func seRegex(key string) *regexp.Regexp {
	seRxMu.RLock()
	rx, ok := seRxCache[key]
	seRxMu.RUnlock()
	if ok {
		return rx
	}
	var pattern string
	switch key[0] {
	case 'S': // "S2E5"
		pattern = key[1:]
		var se string
		if i := strings.Index(pattern, "E"); i >= 0 {
			se = `\bS0*` + pattern[:i] + `[._ -]?E0*` + pattern[i+1:] + `\b`
		} else {
			se = `\bS0*` + pattern + `\b`
		}
		rx = regexp.MustCompile(`(?i)` + se)
	default: // "2x5"
		// "2x5" → also matches zero-padded "2x05"
		if i := strings.Index(key, "x"); i >= 0 {
			rx = regexp.MustCompile(`(?i)\b0*` + key[:i] + `x0*` + key[i+1:] + `\b`)
		} else {
			rx = regexp.MustCompile(`(?i)\b0*` + key + `\b`)
		}
	}
	seRxMu.Lock()
	if len(seRxCache) > 2048 {
		seRxCache = map[string]*regexp.Regexp{}
	}
	seRxCache[key] = rx
	seRxMu.Unlock()
	return rx
}

// ReleaseTokensCached وسوم إصدار مع تخزين مؤقت (أسماء الإصدارات تتكرر كثيراً).
func ReleaseTokensCached(text string) []string {
	relTokMu.RLock()
	toks, ok := relTokCache[text]
	relTokMu.RUnlock()
	if ok {
		return toks
	}
	toks = ReleaseTokens(text)
	relTokMu.Lock()
	if len(relTokCache) >= relTokCap {
		relTokCache = map[string][]string{}
	}
	relTokCache[text] = toks
	relTokMu.Unlock()
	return toks
}

// RankContext سياق الترتيب: fps للفيديو + اللغات المضمّنة.
type RankContext struct {
	VideoFps *float64
	Slangs   map[string]bool
}

// Score يعيد نسبة تطابق 0-100 (100 للبصمة حصراً حسب بروتوكول v2).
func Score(c *Candidate, p *Parsed, ctx RankContext) int {
	if c.HashMatch {
		return 100
	}
	rel := strings.ToLower(c.Release)
	relNorm := normText(rel)
	s := 0.0

	// 1) تطابق العنوان (الأقوى بعد البصمة)
	if p.TitleNorm != "" {
		if strings.Contains(relNorm, p.TitleNorm) {
			s += 35
		} else if len(p.TitleNorm) >= len(relNorm) && strings.Contains(p.TitleNorm, relNorm) && len(relNorm) >= 6 {
			s += 18
		}
	}

	// 2) تقاطع وسوم الإصدار
	if len(p.Tokens) > 0 {
		s += 25 * jaccard(p.Tokens, ReleaseTokensCached(c.Release))
	}

	// 3) السنة
	if p.Year != nil {
		if yearMatches(rel, *p.Year) {
			s += 10
		} else if anyYearRx.MatchString(rel) {
			s -= 10 // سنة مختلفة صراحةً
		}
	}

	// 4) الموسم/الحلقة
	if p.Season != nil && p.Episode != nil {
		seKey := fmt.Sprintf("S%dE%d", *p.Season, *p.Episode)
		altKey := fmt.Sprintf("%dx%d", *p.Season, *p.Episode)
		if seRegex(seKey).MatchString(rel) || seRegex(altKey).MatchString(rel) {
			s += 15
		} else if seGenericRx.MatchString(rel) {
			s -= 8 // حلقة مختلفة
		}
	} else if p.Season != nil && p.Episode == nil {
		if seRegex(fmt.Sprintf("S%d", *p.Season)).MatchString(rel) {
			s += 8
		}
	}

	// 5) fps قريب من الفيديو (بروتوكول v2)
	if ctx.VideoFps != nil && c.Fps != nil {
		d := math.Abs(*ctx.VideoFps - *c.Fps)
		if d <= 0.3 {
			s += 3
		} else if d > 1 {
			s -= 6
		}
	}

	// 6) شعبية وجودة
	if c.Downloads != nil && *c.Downloads > 0 {
		s += math.Min(12, math.Log10(1+float64(*c.Downloads))*2.4)
	}
	if c.Rating != nil && *c.Rating > 0 {
		s += math.Min(8, *c.Rating)
	}
	if c.Hi {
		s -= 4
	}
	// 7) ترجمة آلية بالمزوّد؟ خفض حاد — المستخدم رفض ميزات AI
	if c.AiTranslated {
		s -= 15
	}
	// 8) لغة مضمّنة أصلاً في الفيديو (slangs في v2) → خفض حاد
	if ctx.Slangs[c.Lang] {
		s -= 100
	}

	return clampScore(int(math.Round(s)))
}

func clampScore(v int) int {
	if v < 1 {
		return 1
	}
	if v > 99 {
		return 99
	}
	return v
}

func jaccard(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	setA := make(map[string]bool, len(a))
	for _, x := range a {
		setA[x] = true
	}
	inter := 0
	seen := map[string]bool{}
	for _, x := range b {
		if setA[x] && !seen[x] {
			inter++
			seen[x] = true
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// Ranked نتيجة مرتبة: المرشّح + درجته.
type Ranked struct {
	C     *Candidate
	Match int
}

// Rank يرتّب المرشّحين تنازلياً حسب التطابق ثم التحميلات.
func Rank(cands []*Candidate, p *Parsed, ctx RankContext) []Ranked {
	out := make([]Ranked, 0, len(cands))
	for _, c := range cands {
		out = append(out, Ranked{c, Score(c, p, ctx)})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Match != out[j].Match {
			return out[i].Match > out[j].Match
		}
		var di, dj int64
		if out[i].C.Downloads != nil {
			di = *out[i].C.Downloads
		}
		if out[j].C.Downloads != nil {
			dj = *out[j].C.Downloads
		}
		return di > dj
	})
	return out
}
