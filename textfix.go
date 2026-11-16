// تجهيز ملف الترجمة: فك ZIP + إصلاح الترميز (Windows-1256/UTF-16 → UTF-8).
// عمليات محلية فقط — صفر شبكة، ~1ms لملف 100KB. الجداول في text_tables.go.
package main

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// cp1254Table: Turkish = cp1252 with 6 letters replaced.
var cp1254Table = func() [256]rune {
	t := cp1252Table
	t[0xD0], t[0xDD], t[0xDE], t[0xF0], t[0xFD], t[0xFE] = 'Ğ', 'İ', 'Ş', 'ğ', 'ı', 'ş'
	return t
}()

var fmtPref = map[string]int{"srt": 0, "vtt": 1, "ass": 2, "ssa": 3}

// ExtractSubtitle يعيد (المحتوى، الصيغة) — يدعم ZIP وإلا يعامل البيانات كملف مباشر.
func ExtractSubtitle(data []byte) ([]byte, string, error) {
	if len(data) > 1 && data[0] == 'P' && data[1] == 'K' {
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, "", err
		}
		type zcand struct {
			pref, size int
			name       string
			content    []byte
		}
		var cands []zcand
		for _, f := range zr.File {
			if f.FileInfo().IsDir() || strings.HasPrefix(f.Name, "__MACOSX") {
				continue
			}
			dot := strings.LastIndex(f.Name, ".")
			if dot < 0 {
				continue
			}
			ext := strings.ToLower(f.Name[dot+1:])
			if _, ok := fmtPref[ext]; !ok {
				continue
			}
			content, err := readZipFile(f)
			if err != nil {
				continue
			}
			cands = append(cands, zcand{fmtPref[ext], -len(content), f.Name, content})
		}
		if len(cands) == 0 {
			return nil, "", errNoSubtitleInArchive
		}
		best := cands[0]
		for _, c := range cands[1:] {
			if c.pref < best.pref || (c.pref == best.pref && c.size > best.size) {
				best = c
			}
		}
		return best.content, strings.ToLower(best.name[strings.LastIndex(best.name, ".")+1:]), nil
	}
	head := strings.ToLower(string(bytes.TrimLeft(data[:min(len(data), 400)], "\xef\xbb\xbf\xff\xfe\xfe\xff")))
	if strings.HasPrefix(head, "webvtt") {
		return data, "vtt", nil
	}
	if strings.Contains(head, "[script info]") {
		return data, "ass", nil
	}
	return data, "srt", nil
}

type simpleErr string

func (e simpleErr) Error() string { return string(e) }

const errNoSubtitleInArchive = simpleErr("no subtitle in archive")

func readZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	// guard against zip bombs: never inflate more than maxSubtitleBytes
	if f.UncompressedSize64 > maxSubtitleBytes {
		return nil, errTooLarge
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(io.LimitReader(rc, maxSubtitleBytes+1)); err != nil {
		return nil, err
	}
	if buf.Len() > maxSubtitleBytes {
		return nil, errTooLarge
	}
	return buf.Bytes(), nil
}

const (
	maxSubtitleBytes = 5 << 20
	errTooLarge      = simpleErr("subtitle too large")
)

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// arabicRatio نسبة الحروف العربية من الحروف اللاتينية+العربية.
func arabicRatio(s string) float64 {
	letters, arabic := 0, 0
	for _, r := range s {
		latin := (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
		ar := r >= 0x600 && r <= 0x6ff
		if latin || ar {
			letters++
			if ar {
				arabic++
			}
		}
	}
	if letters == 0 {
		return 0
	}
	return float64(arabic) / float64(letters)
}

// decodeSingleByte فك ترميز بايت-مفرد عبر جدول مولّد مسبقاً.
func decodeSingleByte(raw []byte, table *[256]rune) string {
	var b strings.Builder
	b.Grow(len(raw))
	for _, by := range raw {
		b.WriteRune(table[by])
	}
	return b.String()
}

// decodeUTF16 فك UTF-16 مع دعم BOM.
func decodeUTF16(raw []byte) string {
	be := raw[0] == 0xfe && raw[1] == 0xff
	body := raw[2:]
	if len(body)%2 != 0 {
		body = body[:len(body)-1]
	}
	units := make([]uint16, 0, len(body)/2)
	for i := 0; i < len(body); i += 2 {
		if be {
			units = append(units, uint16(body[i])<<8|uint16(body[i+1]))
		} else {
			units = append(units, uint16(body[i+1])<<8|uint16(body[i]))
		}
	}
	return string(utf16.Decode(units)) // handles surrogate pairs
}

// DecodeText فك الترميز بذكاء: UTF-8 أولاً، ثم UTF-16 (BOM)، ثم cp1256/cp1252 للعربية.
func DecodeText(raw []byte, lang string) (string, string) {
	if len(raw) >= 3 && raw[0] == 0xef && raw[1] == 0xbb && raw[2] == 0xbf {
		return string(raw[3:]), "utf-8-sig"
	}
	if len(raw) >= 2 && ((raw[0] == 0xff && raw[1] == 0xfe) || (raw[0] == 0xfe && raw[1] == 0xff)) {
		return decodeUTF16(raw), "utf-16"
	}
	if utf8.Valid(raw) {
		return string(raw), "utf-8"
	}
	l := strings.ToLower(lang)
	if strings.HasPrefix(l, "tr") {
		return decodeSingleByte(raw, &cp1254Table), "windows-1254"
	}
	if strings.HasPrefix(l, "ar") || strings.HasPrefix(l, "fa") || strings.HasPrefix(l, "ur") {
		bestRatio, bestText, bestEnc := 0.0, "", ""
		for _, enc := range []struct {
			name  string
			table *[256]rune
		}{{"windows-1256", &cp1256Table}, {"iso-8859-6", &iso8859_6Table}} {
			text := decodeSingleByte(raw, enc.table)
			ratio := arabicRatio(text)
			if ratio > bestRatio {
				bestRatio, bestText, bestEnc = ratio, text, enc.name
			}
		}
		if bestRatio > 0.2 {
			return bestText, bestEnc
		}
	}
	return decodeSingleByte(raw, &cp1252Table), "windows-1252"
}

// NormalizeSubtitle يعيد (UTF-8، الصيغة، الترميز الأصلي المكتشف).
func NormalizeSubtitle(data []byte, lang string) ([]byte, string, string, error) {
	raw, fmt_, err := ExtractSubtitle(data)
	if err != nil {
		return nil, "", "", err
	}
	text, enc := DecodeText(raw, lang)
	text = strings.NewReplacer("\x00", "", "\r\n", "\n", "\r", "\n").Replace(text)
	text = strings.TrimLeft(text, "\ufeff")
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return []byte(text), fmt_, enc, nil
}
