//go:build windows

package main

import (
	"os"
	"sync"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Terminal access, Windows flavour. Output: ENABLE_VIRTUAL_TERMINAL_PROCESSING
// turns the console (Windows Terminal, ConHost on Windows 10+) into a VT
// terminal, so the ANSI UI works unchanged; when the console refuses it the
// theme falls back to no color and ASCII glyphs (termVT). Input: echo, line
// editing and Ctrl+C processing are switched off (Ctrl+C arrives as 0x03, like
// cbreak mode with ISIG off on Unix) and ENABLE_VIRTUAL_TERMINAL_INPUT is
// asked for. Keys are read as console input records and turned into the same
// byte stream a Unix terminal produces, so the shared parser in term.go handles
// both: characters become UTF-8, and keys without a character (arrows, Home,
// End, Delete on a console without VT input) become their xterm sequences.

const winEnableMouseInput = 0x10

// winVTInput is set when the console accepted ENABLE_VIRTUAL_TERMINAL_INPUT:
// it then encodes Alt and special keys as escape sequences itself.
var winVTInput bool

func winHandle(f *os.File) windows.Handle { return windows.Handle(f.Fd()) }

func isTTY(f *os.File) bool {
	var m uint32
	return windows.GetConsoleMode(winHandle(f), &m) == nil
}

func termWidth() int {
	if w := uiWidthOverride.Load(); w > 0 {
		return int(w)
	}
	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(winHandle(os.Stdout), &info); err != nil {
		return 80
	}
	if w := int(info.Window.Right-info.Window.Left) + 1; w > 0 {
		return w
	}
	return 80
}

// uiWidthInvalidate: the Windows width is not cached.
func uiWidthInvalidate() {}

// termHeight is the console window height in rows.
func termHeight() int {
	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(winHandle(os.Stdout), &info); err != nil {
		return 24
	}
	if h := int(info.Window.Bottom-info.Window.Top) + 1; h > 0 {
		return h
	}
	return 24
}

var termVTState struct {
	once sync.Once
	ok   bool
}

// termVT turns on VT processing for stdout once and reports whether the
// console accepted it. Output that is not a console (pipe, file) counts as VT,
// as on Unix: whoever reads it decides.
func termVT() bool {
	termVTState.once.Do(func() {
		h := winHandle(os.Stdout)
		var m uint32
		if windows.GetConsoleMode(h, &m) != nil {
			termVTState.ok = true
			return
		}
		if m&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
			termVTState.ok = true
			return
		}
		termVTState.ok = windows.SetConsoleMode(h, m|windows.ENABLE_PROCESSED_OUTPUT|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
	})
	return termVTState.ok
}

// uiTerm remembers the console modes saved by enterCbreak so the editor can
// hand the console to a child process ($EDITOR) and take it back afterwards.
var uiTerm struct {
	mu sync.Mutex
	st winTermState
}

type winTermState struct {
	in, out         windows.Handle
	oldIn, rawIn    uint32
	oldOut, rawOut  uint32
	saved, outSaved bool
}

// enterCbreak puts the console into the raw-ish mode the editor needs and
// returns the function that restores the original modes.
func enterCbreak(fd int) (func(), error) {
	in := windows.Handle(fd)
	var oldIn uint32
	if err := windows.GetConsoleMode(in, &oldIn); err != nil {
		return func() {}, err
	}
	raw := oldIn &^ (windows.ENABLE_ECHO_INPUT | windows.ENABLE_LINE_INPUT | windows.ENABLE_PROCESSED_INPUT | winEnableMouseInput)
	if windows.SetConsoleMode(in, raw|windows.ENABLE_VIRTUAL_TERMINAL_INPUT) == nil {
		raw |= windows.ENABLE_VIRTUAL_TERMINAL_INPUT
		winVTInput = true
	} else if err := windows.SetConsoleMode(in, raw); err != nil { // old console: no VT input, keys are translated below
		return func() {}, err
	}
	out := winHandle(os.Stdout)
	var oldOut uint32
	outSaved := windows.GetConsoleMode(out, &oldOut) == nil
	rawOut := oldOut
	if outSaved {
		_ = termVT()
		_ = windows.GetConsoleMode(out, &rawOut)
	}
	uiTerm.mu.Lock()
	uiTerm.st = winTermState{in: in, out: out, oldIn: oldIn, rawIn: raw, oldOut: oldOut, rawOut: rawOut, saved: true, outSaved: outSaved}
	uiTerm.mu.Unlock()
	vt := termVT()
	if vt {
		os.Stdout.WriteString("\x1b[?2004h") // bracketed paste (Windows Terminal)
	}
	return func() {
		if vt {
			os.Stdout.WriteString("\x1b[?2004l")
		}
		_ = windows.SetConsoleMode(in, oldIn)
		if outSaved {
			_ = windows.SetConsoleMode(out, oldOut)
		}
	}, nil
}

// uiCooked runs f with the console modes restored, then puts raw mode back.
func uiCooked(f func()) {
	uiTerm.mu.Lock()
	t := uiTerm.st
	uiTerm.mu.Unlock()
	if !t.saved {
		f()
		return
	}
	vt := termVT()
	if vt {
		os.Stdout.WriteString("\x1b[?2004l")
	}
	_ = windows.SetConsoleMode(t.in, t.oldIn)
	if t.outSaved {
		_ = windows.SetConsoleMode(t.out, t.oldOut)
	}
	defer func() {
		_ = windows.SetConsoleMode(t.in, t.rawIn)
		if t.outSaved {
			_ = windows.SetConsoleMode(t.out, t.rawOut)
		}
		if vt {
			os.Stdout.WriteString("\x1b[?2004h")
		}
	}()
	f()
}

// ---------- console input records ----------

const (
	winKeyEvent = 0x0001

	winRightAlt  = 0x0001
	winLeftAlt   = 0x0002
	winRightCtrl = 0x0004
	winLeftCtrl  = 0x0008
	winShift     = 0x0010

	vkTab    = 0x09
	vkPrior  = 0x21
	vkNext   = 0x22
	vkEnd    = 0x23
	vkHome   = 0x24
	vkLeft   = 0x25
	vkUp     = 0x26
	vkRight  = 0x27
	vkDown   = 0x28
	vkInsert = 0x2D
	vkDelete = 0x2E
)

// winInputRecord is INPUT_RECORD with the KEY_EVENT_RECORD arm of the union
// spelled out (the union is 16 bytes; other event kinds are skipped).
type winInputRecord struct {
	EventType uint16
	_         uint16
	KeyDown   int32
	Repeat    uint16
	VKey      uint16
	VScan     uint16
	Char      uint16
	CtrlState uint32
}

var (
	winKernel32          = windows.NewLazySystemDLL("kernel32.dll")
	procReadConsoleInput = winKernel32.NewProc("ReadConsoleInputW")
)

func winReadInput(h windows.Handle, recs []winInputRecord) (int, error) {
	var n uint32
	r, _, err := procReadConsoleInput.Call(uintptr(h), uintptr(unsafe.Pointer(&recs[0])), uintptr(len(recs)), uintptr(unsafe.Pointer(&n)))
	if r == 0 {
		return 0, err
	}
	return int(n), nil
}

// winKeyBytes turns one key-down record into the bytes a VT terminal would
// send. hi carries a pending UTF-16 high surrogate between records.
func winKeyBytes(r winInputRecord, hi *uint16) []byte {
	if r.EventType != winKeyEvent || r.KeyDown == 0 {
		return nil
	}
	ctrl := r.CtrlState&(winLeftCtrl|winRightCtrl) != 0
	alt := r.CtrlState&(winLeftAlt|winRightAlt) != 0 && !ctrl // AltGr = Ctrl+Alt: a plain character
	var one []byte
	if c := r.Char; c != 0 {
		switch {
		case utf16.IsSurrogate(rune(c)) && c < 0xDC00:
			*hi = c
			return nil
		case utf16.IsSurrogate(rune(c)):
			if *hi == 0 {
				return nil
			}
			one = []byte(string(utf16.DecodeRune(rune(*hi), rune(c))))
			*hi = 0
		case c == '\t' && r.VKey == vkTab && r.CtrlState&winShift != 0:
			one = []byte("\x1b[Z")
		default:
			one = []byte(string(rune(c)))
			if alt && c != 0x1b && !winVTInput {
				one = append([]byte{0x1b}, one...)
			}
		}
	} else {
		seq := map[uint16]string{vkUp: "A", vkDown: "B", vkRight: "C", vkLeft: "D", vkHome: "H", vkEnd: "F"}[r.VKey]
		switch {
		case seq != "" && ctrl && (r.VKey == vkLeft || r.VKey == vkRight):
			one = []byte("\x1b[1;5" + seq)
		case seq != "":
			one = []byte("\x1b[" + seq)
		case r.VKey == vkDelete:
			one = []byte("\x1b[3~")
		case r.VKey == vkInsert:
			one = []byte("\x1b[2~")
		case r.VKey == vkPrior:
			one = []byte("\x1b[5~")
		case r.VKey == vkNext:
			one = []byte("\x1b[6~")
		default:
			return nil // modifiers alone, function keys
		}
	}
	n := int(r.Repeat)
	if n < 1 {
		n = 1
	}
	if n == 1 {
		return one
	}
	out := make([]byte, 0, len(one)*n)
	for i := 0; i < n; i++ {
		out = append(out, one...)
	}
	return out
}

// readFile waits on the console handle with a timeout so that Pause can take
// the console away between reads, and reads whole input records. Non-consoles
// (pipes, files) are read plainly.
func (kr *KeyReader) readFile(f *os.File) {
	if !isTTY(f) {
		kr.readPlain(f)
		return
	}
	h := winHandle(f)
	recs := make([]winInputRecord, 128)
	var hi uint16
	for {
		kr.gate.Lock()
		ev, err := windows.WaitForSingleObject(h, 200)
		if err != nil {
			kr.gate.Unlock()
			close(kr.bytes)
			return
		}
		if ev == uint32(windows.WAIT_TIMEOUT) {
			kr.gate.Unlock()
			continue
		}
		n, rerr := winReadInput(h, recs)
		kr.gate.Unlock()
		if rerr != nil {
			close(kr.bytes)
			return
		}
		for i := 0; i < n; i++ {
			for _, b := range winKeyBytes(recs[i], &hi) {
				kr.bytes <- b
			}
		}
	}
}

// uiWinchNotify: Windows has no SIGWINCH, so a watcher polls the console
// width and posts a resize event when it changes.
func uiWinchNotify(c chan os.Signal) {
	safeGo(func() {
		w := termWidth()
		for {
			time.Sleep(250 * time.Millisecond)
			if nw := termWidth(); nw != w {
				w = nw
				select {
				case c <- winResize{}:
				default:
				}
			}
		}
	})
}

// winResize is the synthetic os.Signal posted on a console resize.
type winResize struct{}

func (winResize) String() string { return "resize" }
func (winResize) Signal()        {}
