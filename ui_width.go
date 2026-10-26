package main

import (
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
	"unicode/utf8"
)

// Cell widths and low-power tuning for slow terminals (a 1-core Pentium M
// with roxterm/VTE is the reference target).

// uiRuneW is the number of terminal cells r takes: 0 for combining marks and
// zero-width format characters, 2 for East Asian wide/fullwidth characters
// and emoji with emoji presentation (what VTE/glib wcwidth does), else 1.
func uiRuneW(r rune) int {
	switch {
	case r < 0x300:
		return 1
	case r == 0x200b || r == 0x200c || r == 0x200d || r == 0x2060 || r >= 0xfe00 && r <= 0xfe0f || r == 0xfeff:
		return 0
	case r < 0x1100:
		if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
			return 0
		}
		return 1
	}
	if uiWide(r) {
		return 2
	}
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
		return 0
	}
	return 1
}

// uiWideRanges: East Asian Wide/Fullwidth blocks and the emoji that default to
// emoji presentation (Unicode 15, trimmed to what shows up in practice).
var uiWideRanges = [][2]rune{
	{0x1100, 0x115f}, {0x231a, 0x231b}, {0x2329, 0x232a}, {0x23e9, 0x23ec}, {0x23f0, 0x23f0}, {0x23f3, 0x23f3},
	{0x25fd, 0x25fe}, {0x2614, 0x2615}, {0x2648, 0x2653}, {0x267f, 0x267f}, {0x2693, 0x2693}, {0x26a1, 0x26a1},
	{0x26aa, 0x26ab}, {0x26bd, 0x26be}, {0x26c4, 0x26c5}, {0x26ce, 0x26ce}, {0x26d4, 0x26d4}, {0x26ea, 0x26ea},
	{0x26f2, 0x26f3}, {0x26f5, 0x26f5}, {0x26fa, 0x26fa}, {0x26fd, 0x26fd}, {0x2705, 0x2705}, {0x270a, 0x270b},
	{0x2728, 0x2728}, {0x274c, 0x274c}, {0x274e, 0x274e}, {0x2753, 0x2755}, {0x2757, 0x2757}, {0x2795, 0x2797},
	{0x27b0, 0x27b0}, {0x27bf, 0x27bf}, {0x2b1b, 0x2b1c}, {0x2b50, 0x2b50}, {0x2b55, 0x2b55},
	{0x2e80, 0x303e}, {0x3041, 0x33ff}, {0x3400, 0x4dbf}, {0x4e00, 0x9fff}, {0xa000, 0xa4cf}, {0xa960, 0xa97f},
	{0xac00, 0xd7a3}, {0xf900, 0xfaff}, {0xfe10, 0xfe19}, {0xfe30, 0xfe6f}, {0xff00, 0xff60}, {0xffe0, 0xffe6},
	{0x16fe0, 0x16fe4}, {0x17000, 0x18cff}, {0x1b000, 0x1b2ff}, {0x1f004, 0x1f004}, {0x1f0cf, 0x1f0cf},
	{0x1f18e, 0x1f18e}, {0x1f191, 0x1f19a}, {0x1f200, 0x1f202}, {0x1f210, 0x1f23b}, {0x1f240, 0x1f248},
	{0x1f250, 0x1f251}, {0x1f260, 0x1f265}, {0x1f300, 0x1f320}, {0x1f32d, 0x1f335}, {0x1f337, 0x1f37c},
	{0x1f37e, 0x1f393}, {0x1f3a0, 0x1f3ca}, {0x1f3cf, 0x1f3d3}, {0x1f3e0, 0x1f3f0}, {0x1f3f4, 0x1f3f4},
	{0x1f3f8, 0x1f43e}, {0x1f440, 0x1f440}, {0x1f442, 0x1f4fc}, {0x1f4ff, 0x1f53d}, {0x1f54b, 0x1f54e},
	{0x1f550, 0x1f567}, {0x1f57a, 0x1f57a}, {0x1f595, 0x1f596}, {0x1f5a4, 0x1f5a4}, {0x1f5fb, 0x1f64f},
	{0x1f680, 0x1f6c5}, {0x1f6cc, 0x1f6cc}, {0x1f6d0, 0x1f6d2}, {0x1f6d5, 0x1f6d7}, {0x1f6dc, 0x1f6df},
	{0x1f6eb, 0x1f6ec}, {0x1f6f4, 0x1f6fc}, {0x1f7e0, 0x1f7eb}, {0x1f7f0, 0x1f7f0}, {0x1f90c, 0x1f93a},
	{0x1f93c, 0x1f945}, {0x1f947, 0x1f9ff}, {0x1fa70, 0x1faff}, {0x20000, 0x2fffd}, {0x30000, 0x3fffd},
}

func uiWide(r rune) bool {
	lo, hi := 0, len(uiWideRanges)-1
	for lo <= hi {
		m := (lo + hi) / 2
		switch {
		case r < uiWideRanges[m][0]:
			hi = m - 1
		case r > uiWideRanges[m][1]:
			lo = m + 1
		default:
			return true
		}
	}
	return false
}

// uiStrW is the visible width of s in cells (escape sequences are zero-width,
// other control characters too). Pure ASCII takes the fast path.
func uiStrW(s string) int {
	n := 0
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == 0x1b:
			i = uiEscEnd(s, i)
			continue
		case c < 0x20 || c == 0x7f:
			i++
			continue
		case c < 0x80:
			n++
			i++
			continue
		}
		r, sz := utf8.DecodeRuneInString(s[i:])
		n += uiRuneW(r)
		i += sz
	}
	return n
}

// uiRunesW is the width of a rune slice.
func uiRunesW(rs []rune) int {
	n := 0
	for _, r := range rs {
		if r < 0x80 {
			n++
		} else {
			n += uiRuneW(r)
		}
	}
	return n
}

// uiWidthOverride fixes termWidth (tests drive a virtual 86x27 terminal).
var uiWidthOverride atomic.Int32

// ---------- low power ----------

var (
	uiLowOnce sync.Once
	uiLow     bool
	// uiLowOverride is set by --low-power (1 = on, -1 = off, 0 = auto).
	uiLowOverride atomic.Int32
)

// uiLowPower: fewer frames and animations. On with --low-power or
// JAZZ_LOW_POWER=1; automatic on a single-CPU machine and on Termux (JAZZ_LOW_POWER=0 turns
// the automatic choice off).
func uiLowPower() bool {
	switch uiLowOverride.Load() {
	case 1:
		return true
	case -1:
		return false
	}
	uiLowOnce.Do(func() {
		v := strings.ToLower(strings.TrimSpace(os.Getenv("JAZZ_LOW_POWER")))
		switch v {
		case "1", "on", "true", "yes", "sim":
			uiLow = true
		case "0", "off", "false", "no", "nao", "não":
			uiLow = false
		default:
			uiLow = runtime.NumCPU() == 1 || isTermux() // phones: many slow cores
		}
	})
	return uiLow
}

// uiLowFlag is --low-power: a bare flag means on; =off / =auto also work.
type uiLowFlag struct{ v string }

func (f *uiLowFlag) String() string   { return f.v }
func (f *uiLowFlag) IsBoolFlag() bool { return true }
func (f *uiLowFlag) Set(s string) error {
	f.v = strings.ToLower(strings.TrimSpace(s))
	return nil
}

func (f *uiLowFlag) apply() {
	switch f.v {
	case "", "auto":
	case "0", "off", "false", "no", "nao", "não":
		uiLowOverride.Store(-1)
	default:
		uiLowOverride.Store(1)
	}
}
