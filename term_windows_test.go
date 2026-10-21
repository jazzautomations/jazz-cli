//go:build windows

package main

import (
	"os"
	"testing"
	"time"
)

// Windows-only checks of the console layer that work without a real console
// (CI runs with pipes): key-record translation, non-console fallbacks.

func rec(ch rune, vk uint16, ctrl uint32) winInputRecord {
	return winInputRecord{EventType: winKeyEvent, KeyDown: 1, Repeat: 1, VKey: vk, Char: uint16(ch), CtrlState: ctrl}
}

func TestWinKeyBytesTranslatesLegacyKeys(t *testing.T) {
	old := winVTInput
	winVTInput = false
	defer func() { winVTInput = old }()
	var hi uint16
	cases := []struct {
		r    winInputRecord
		want string
	}{
		{rec('a', 0x41, 0), "a"},
		{rec('ç', 0, 0), "ç"},
		{rec(0x03, 0x43, winLeftCtrl), "\x03"},
		{rec(0, vkUp, 0), "\x1b[A"},
		{rec(0, vkDown, 0), "\x1b[B"},
		{rec(0, vkRight, 0), "\x1b[C"},
		{rec(0, vkLeft, winLeftCtrl), "\x1b[1;5D"},
		{rec(0, vkHome, 0), "\x1b[H"},
		{rec(0, vkEnd, 0), "\x1b[F"},
		{rec(0, vkDelete, 0), "\x1b[3~"},
		{rec('\t', vkTab, winShift), "\x1b[Z"},
		{rec('b', 0x42, winLeftAlt), "\x1bb"},
		{rec('@', 0x32, winRightAlt|winLeftCtrl), "@"}, // AltGr is a character, not Alt
		{rec(0, 0x10, winShift), ""},                   // Shift alone
	}
	for _, c := range cases {
		if got := string(winKeyBytes(c.r, &hi)); got != c.want {
			t.Errorf("winKeyBytes(%+v) = %q want %q", c.r, got, c.want)
		}
	}
	up := rec('x', 0x58, 0)
	up.KeyDown = 0
	if b := winKeyBytes(up, &hi); b != nil {
		t.Errorf("key-up must produce nothing, got %q", b)
	}
	rep := rec('z', 0x5A, 0)
	rep.Repeat = 3
	if got := string(winKeyBytes(rep, &hi)); got != "zzz" {
		t.Errorf("repeat count: %q", got)
	}
	// a surrogate pair split over two records
	if b := winKeyBytes(rec(0xD83D, 0, 0), &hi); b != nil {
		t.Errorf("high surrogate alone must wait, got %q", b)
	}
	if got := string(winKeyBytes(rec(0xDE00, 0, 0), &hi)); got != "😀" {
		t.Errorf("surrogate pair = %q", got)
	}
}

func TestWinKeyBytesVTInputPassesThrough(t *testing.T) {
	old := winVTInput
	winVTInput = true
	defer func() { winVTInput = old }()
	var hi uint16
	// with VT input the console already sends ESC b for Alt+b: no second ESC
	if got := string(winKeyBytes(rec('b', 0x42, winLeftAlt), &hi)); got != "b" {
		t.Errorf("VT input Alt+b = %q", got)
	}
}

// The parser in term.go sees the same bytes as on Unix.
func TestWinKeyBytesFeedTheSharedParser(t *testing.T) {
	var hi uint16
	var in []byte
	for _, r := range []winInputRecord{rec(0, vkLeft, 0), rec('o', 0x4F, 0), rec('k', 0x4B, 0), rec(0, vkDelete, 0)} {
		in = append(in, winKeyBytes(r, &hi)...)
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	pw.Write(in)
	pw.Close()
	kr := uiKeyReaderFrom(pr)
	want := []KeyCode{KLeft, KRune, KRune, KDelete}
	for i, w := range want {
		select {
		case k := <-kr.Keys:
			if k.Code != w {
				t.Fatalf("key %d = %v want %v", i, k.Code, w)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("key %d: timeout", i)
		}
	}
}

func TestWinConsoleFallbacksOnPipes(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	defer pw.Close()
	if isTTY(pr) {
		t.Error("a pipe is not a console")
	}
	if _, err := enterCbreak(int(pr.Fd())); err == nil {
		t.Error("enterCbreak on a pipe must fail (the caller falls back)")
	}
	if w := termWidth(); w <= 0 {
		t.Errorf("termWidth = %d", w)
	}
	_ = termVT()        // must not panic without a console
	uiCooked(func() {}) // without saved state it just runs f
}
