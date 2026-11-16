// اختبارات إصلاح الترميز وفك الأرشيف.
package main

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

const arSRT = "1\n00:00:01,000 --> 00:00:03,000\nمرحبا بك في الفيلم\n\n"

// toCp1256 خريطة عكسية من الجدول المولّد — ترميز مضمون بلا حفظ يدوي.
func toCp1256(s string) []byte {
	rev := map[rune]byte{}
	for b := 0; b < 256; b++ {
		if _, exists := rev[cp1256Table[b]]; !exists {
			rev[cp1256Table[b]] = byte(b)
		}
	}
	out := make([]byte, 0, len(s))
	for _, r := range s {
		if b, ok := rev[r]; ok {
			out = append(out, b)
		} else {
			out = append(out, '?')
		}
	}
	return out
}

func TestCp1256Decode(t *testing.T) {
	text, enc := DecodeText(toCp1256(arSRT), "ar")
	if enc != "windows-1256" {
		t.Fatalf("enc = %s", enc)
	}
	if !strings.Contains(text, "مرحبا") {
		t.Fatalf("text = %q", text)
	}
}

func TestUTF8Detect(t *testing.T) {
	_, enc := DecodeText([]byte(arSRT), "ar")
	if enc != "utf-8" {
		t.Fatalf("enc = %s", enc)
	}
}

func TestUTF16BOM(t *testing.T) {
	var buf bytes.Buffer
	buf.Write([]byte{0xff, 0xfe}) // BOM little-endian
	for _, r := range arSRT {
		buf.WriteByte(byte(r))
		buf.WriteByte(byte(r >> 8))
	}
	text, enc := DecodeText(buf.Bytes(), "ar")
	if enc != "utf-16" {
		t.Fatalf("enc = %s", enc)
	}
	if !strings.Contains(text, "مرحبا") {
		t.Fatalf("text = %q", text)
	}
}

func TestZipExtract(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range []struct{ name, body string }{
		{"readme.txt", "not a subtitle"},
		{"movie.ar.srt", arSRT},
	} {
		w, err := zw.Create(f.name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(f.body))
	}
	zw.Close()

	blob, fmt_, err := ExtractSubtitle(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if fmt_ != "srt" {
		t.Fatalf("fmt = %s", fmt_)
	}
	if !strings.Contains(string(blob), "مرحبا") {
		t.Fatalf("blob = %q", blob)
	}
}

func TestNormalizeZipCp1256(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("sub.srt")
	w.Write(toCp1256(arSRT))
	zw.Close()

	blob, fmt_, _, err := NormalizeSubtitle(buf.Bytes(), "ar")
	if err != nil {
		t.Fatal(err)
	}
	if fmt_ != "srt" {
		t.Fatalf("fmt = %s", fmt_)
	}
	if !strings.Contains(string(blob), "مرحبا بك في الفيلم") {
		t.Fatalf("blob = %q", blob)
	}
}

func TestVTTSniff(t *testing.T) {
	_, fmt_, err := ExtractSubtitle([]byte("WEBVTT\n\n00:01.000 --> 00:02.000\nhi\n"))
	if err != nil || fmt_ != "vtt" {
		t.Fatalf("fmt=%s err=%v", fmt_, err)
	}
}
