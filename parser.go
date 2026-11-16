// استخراج ذكي للعنوان من اسم الملف: العنوان + السنة + الموسم/الحلقة + وسوم الإصدار.
// كل العمليات regex محلية — صفر IO، زمن < 0.05ms.
//
// ملاحظة: Go RE2 لا يدعم lookbehind/lookahead — فحص حدود الأرقام يدوي.
package main

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	extRx    = regexp.MustCompile(`(?i)\.(mkv|mp4|avi|mov|wmv|flv|webm|m4v|ts|m2ts|mpg|mpeg|srt|vtt|ass|ssa|zip)$`)
	yearRx   = regexp.MustCompile(`(19\d{2}|20\d{2})`)
	resRx    = regexp.MustCompile(`(?i)\b(480p|576p|720p|1080p|1440p|2160p|4k|uhd)\b`)
	srcRx    = regexp.MustCompile(`(?i)\b(web-?dl|web-?rip|webrip|web\b|blu-?ray|bdrip|brrip|hdrip|dvdrip|hdtv|remux|hdcam|cam|amzn|nf|dsnp|hmax|atvp|pmtp|dvdscr)\b`)
	codecRx  = regexp.MustCompile(`(?i)\b(x264|x265|h\.?264|h\.?265|hevc|avc|av1|xvid|10bit|8bit|hdr10?\+?|dolby|vision|aac2?|ac3|eac3|dts-?hd|ddp?5?\.?1?|atmos|truehd|flac|opus)\b`)
	junkRx   = regexp.MustCompile(`(?i)\b(proper|repack|extended|unrated|uncut|internal|multi|dualaudio|dual|dubbed|subbed|softsub|hardsub|arabic|english|remastered|complete|official|trailer|sample|imax)\b`)
	symRx    = regexp.MustCompile(`[™®©]+`)
	brkRx    = regexp.MustCompile(`(?i)[\[\(\{][^\]\)\}]*\b[a-z0-9-]+\.(?:com|net|org|tv|cc|info|ws|xyz|site|live|club|top|vip|ir|biz|cam|fun|online|movie|film|cinema|me|io)\b[^\]\)\}]*[\]\)\}]`)
	wwwRx    = regexp.MustCompile(`(?i)\bwww[\s._-]+[a-z0-9-]+[\s._-]+(?:com|net|org|tv|cc|info|ws|xyz|site|live|club|top|vip|ir|biz|cam|fun|online|movie|film|cinema|me|io)\b`)
	domainRx = regexp.MustCompile(`(?i)[a-z0-9-]{2,}\.(?:com|net|org|tv|cc|info|ws|xyz|site|live|club|top|vip|ir|biz|cam|fun|online|movie|film|cinema|me|io)`)
	leadRx   = regexp.MustCompile(`^\s*[\[\(\{]([^\]\)\}]{1,30})[\]\)\}][\s]*[-._ ]*`)
	dotsRx   = regexp.MustCompile(`[._]+`)
	spaceRx  = regexp.MustCompile(`\s{2,}`)
	rgTrRx   = regexp.MustCompile(`-([A-Za-z0-9]{2,12})$`)

	// أنماط الموسم/الحلقة — بالترتيب (الأدق أولاً)
	seRx       = regexp.MustCompile(`(?i)\bS(\d{1,2})[\s._-]?E(\d{1,3})(?:-E?(\d{1,3}))?\b`)
	nxnnRx     = regexp.MustCompile(`(?i)\b(\d{1,2})x(\d{2,3})\b`)
	seasonEpRx = regexp.MustCompile(`(?i)\bSeason\s?(\d{1,2})\s?[-._ ]?\s?Episode\s?(\d{1,3})\b`)
	sOnlyRx    = regexp.MustCompile(`(?i)\bS(\d{1,2})\b`)
	seasonRx   = regexp.MustCompile(`(?i)\bSeason\s?(\d{1,2})\b`)
	eOnlyRx    = regexp.MustCompile(`(?i)\bE(\d{1,3})\b`)
	leadYearRx = regexp.MustCompile(`^[\s]*[\[\(\{]\s*((?:19|20)\d{2})\s*[\]\)\}][\s._-]*`)
)

// Parsed نتيجة تحليل اسم الملف.
type Parsed struct {
	Title     string
	Year      *int
	Season    *int
	Episode   *int
	Tokens    []string
	Norm      string
	TitleNorm string
}

func normText(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || (r >= 0x600 && r <= 0x6ff) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isDigitByte(b byte) bool { return b >= '0' && b <= '9' }

func currentYear() int { return time.Now().Year() }

// yearMatches يتحقق من وجود السنة بحدود رقمية (بديل الـ lookbehind).
func yearMatches(s string, year int) bool {
	ys := strconv.Itoa(year)
	for _, loc := range yearRx.FindAllStringIndex(s, -1) {
		if s[loc[0]:loc[1]] == ys {
			before := byte(' ')
			if loc[0] > 0 {
				before = s[loc[0]-1]
			}
			var after byte
			if loc[1] < len(s) {
				after = s[loc[1]]
			}
			if !isDigitByte(before) && !isDigitByte(after) {
				return true
			}
		}
	}
	return false
}

func stripSiteNoise(name string) string {
	t := brkRx.ReplaceAllString(name, " ")
	t = wwwRx.ReplaceAllString(t, " ")
	// نطاق غارق في الاسم: site.tv — مع فحص حدود يدوي (بديل lookaround)
	for {
		loc := domainRx.FindStringIndex(t)
		if loc == nil {
			break
		}
		okBefore := loc[0] == 0 || strings.ContainsRune(" ._-[(", rune(t[loc[0]-1]))
		okAfter := loc[1] == len(t) || strings.ContainsRune(" ._-])", rune(t[loc[1]]))
		if okBefore && okAfter {
			t = t[:loc[0]] + " " + t[loc[1]:]
			continue
		}
		// النطاق جزء من كلمة أطول — نتخطاه بأمان (نستبدل نقطة مؤقتاً كي لا نعلق)
		dot := strings.Index(t[loc[0]:loc[1]], ".")
		t = t[:loc[0]+dot] + "\x00" + t[loc[0]+dot+1:]
	}
	t = strings.ReplaceAll(t, "\x00", ".")

	if m := leadRx.FindStringSubmatch(t); m != nil {
		inner := m[1]
		useful := resRx.MatchString(inner) || srcRx.MatchString(inner) ||
			yearRx.MatchString(inner) || isAllDigits(inner)
		if !useful {
			t = t[len(m[0]):]
		}
	}
	t = spaceRx.ReplaceAllString(t, " ")
	return strings.Trim(t, " -_.")
}

func isAllDigits(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

// ParseFilename يحلل اسم الملف إلى العنوان/السنة/الموسم/الحلقة/الوسوم.
func ParseFilename(input string) Parsed {
	raw := strings.TrimSpace(input)
	// سنة بين أقواس في البداية «(2023) Oppenheimer» = سنة إصدار وليست جزءاً من العنوان
	var bracketYear *int
	if m := leadYearRx.FindStringSubmatch(raw); m != nil {
		y := atoi(m[1])
		bracketYear = &y
		raw = strings.TrimSpace(spaceRx.ReplaceAllString(raw[len(m[0]):], " "))
	}
	noExt := extRx.ReplaceAllString(raw, "")
	base := stripSiteNoise(noExt)
	if base == "" {
		base = noExt
	}
	text := symRx.ReplaceAllString(base, " ")
	text = strings.Map(func(r rune) rune {
		if r == '[' || r == ']' || r == '(' || r == ')' || r == '{' || r == '}' {
			return ' '
		}
		return r
	}, text)
	text = strings.TrimSpace(dotsRx.ReplaceAllString(text, " "))
	text = spaceRx.ReplaceAllString(text, " ")

	// الموسم/الحلقة: أول نمط يطابق ويُحذف موقعه من النص
	var season, episode *int
	for _, p := range []struct {
		rx   *regexp.Regexp
		both bool
		isSe bool
	}{
		{seRx, true, false},
		{nxnnRx, true, false},
		{seasonEpRx, true, false},
		{sOnlyRx, false, true},
		{seasonRx, false, true},
		{eOnlyRx, false, false},
	} {
		m := p.rx.FindStringSubmatch(text)
		if m == nil {
			continue
		}
		ok := false
		if p.both {
			if m[1] != "" && m[2] != "" {
				s := atoi(m[1])
				e := atoi(m[2])
				season, episode = &s, &e
				ok = true
			}
		} else if p.isSe {
			s := atoi(m[1])
			season = &s
			ok = true
		} else {
			e := atoi(m[1])
			episode = &e
			ok = true
		}
		if !ok {
			continue
		}
		loc := p.rx.FindStringIndex(text)
		if loc[0] == 0 && strings.TrimSpace(text[loc[1]:]) == "" {
			break // لا نحذف إن كان الاسم كله الرقم
		}
		text = strings.TrimSpace(spaceRx.ReplaceAllString(text[:loc[0]]+" "+text[loc[1]:], " "))
		break
	}

	// نقاط القص: أول موضع لوسم إصدار
	cutMin := func() int {
		best := len(text)
		for _, rx := range []*regexp.Regexp{resRx, srcRx, codecRx, junkRx} {
			if loc := rx.FindStringIndex(text); loc != nil && loc[0] > 0 && loc[0] < best {
				best = loc[0]
			}
		}
		return best
	}
	beforeCut := cutMin()

	// السنة: آخر سنة معقولة قبل الوسوم
	var year *int
	yearCut := len(text)
	maxYear := currentYear() + 1
	type cand struct{ y, i int }
	var cands []cand
	for _, loc := range yearRx.FindAllStringIndex(text, -1) {
		if loc[0] == 0 {
			continue // سنة في بداية الاسم ليست سنة إصدار
		}
		y := atoi(text[loc[0]:loc[1]])
		if y <= maxYear {
			cands = append(cands, cand{y, loc[0]})
		}
	}
	if len(cands) > 0 {
		pick := cands[0]
		for _, c := range cands {
			if c.i < beforeCut {
				pick = c // آخر واحدة قبل الوسوم
			}
		}
		y := pick.y
		year = &y
		yearCut = pick.i
	}
	if year == nil && bracketYear != nil {
		year = bracketYear
	}

	cut := yearCut
	if beforeCut < cut {
		cut = beforeCut
	}
	title := text[:cut]
	title = resRx.ReplaceAllString(title, " ")
	title = srcRx.ReplaceAllString(title, " ")
	title = codecRx.ReplaceAllString(title, " ")
	title = junkRx.ReplaceAllString(title, " ")
	title = spaceRx.ReplaceAllString(title, " ")
	title = strings.Trim(title, " -_")
	if m := rgTrRx.FindStringSubmatch(title); m != nil {
		title = title[:len(title)-len(m[0])]
		title = strings.TrimRight(title, " -_")
	}
	if title == "" && text != "" {
		title = strings.SplitN(text, " ", 2)[0]
	}

	// وسوم الإصدار: دقة/مصدر/ترميز/مجموعة
	tokenSet := map[string]bool{}
	for _, rx := range []*regexp.Regexp{resRx, srcRx, codecRx} {
		for _, m := range rx.FindAllString(text, -1) {
			tokenSet[strings.NewReplacer("-", "", ".", "").Replace(strings.ToLower(m))] = true
		}
	}
	if m := rgTrRx.FindStringSubmatch(base); m != nil {
		tokenSet[strings.ToLower(m[1])] = true
	}
	tokens := make([]string, 0, len(tokenSet))
	for t := range tokenSet {
		tokens = append(tokens, t)
	}

	return Parsed{
		Title:     title,
		Year:      year,
		Season:    season,
		Episode:   episode,
		Tokens:    tokens,
		Norm:      normText(base),
		TitleNorm: normText(title),
	}
}

// ReleaseTokens وسوم إصدار من نص حر (اسم إصدار الترجمة).
func ReleaseTokens(text string) []string {
	if text == "" {
		return nil
	}
	return ParseFilename(text).Tokens
}
