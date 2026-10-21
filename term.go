package main

import (
	"io"
	"os"
	"sync"
	"time"
	"unicode/utf8"
)

type KeyCode int

const (
	KRune KeyCode = iota
	KEnter
	KAltEnter
	KBackspace
	KDelete
	KLeft
	KRight
	KUp
	KDown
	KHome
	KEnd
	KWordLeft
	KWordRight
	KTab
	KEsc
	KCtrlA
	KCtrlC
	KCtrlD
	KCtrlE
	KCtrlK
	KCtrlL
	KCtrlU
	KCtrlW
	KPaste
	KUnknown
	// appended after KUnknown so existing values stay stable
	KShiftTab
	KCtrlT
	KCtrlR
	KCtrlG
	KCtrlO
)

type Key struct {
	Code KeyCode
	R    rune
	Text string // for KPaste
}

// KeyReader turns raw stdin bytes into keys. One goroutine for the whole program.
type KeyReader struct {
	Keys  chan Key
	bytes chan byte

	pend    byte // one-byte pushback used by escape() (parser goroutine only)
	hasPend bool

	// gate is held by the stdin reader while it polls/reads; Pause takes it so a
	// child process ($EDITOR) can own the terminal without us stealing bytes.
	gate sync.Mutex
}

func NewKeyReader() *KeyReader {
	kr := uiNewKeyReader()
	safeGo(func() { kr.readFile(os.Stdin) })
	safeGo(kr.parse)
	return kr
}

func uiNewKeyReader() *KeyReader {
	return &KeyReader{Keys: make(chan Key, 512), bytes: make(chan byte, 8192)}
}

// uiKeyReaderFrom parses keys from r (tests, non-TTY input).
func uiKeyReaderFrom(r io.Reader) *KeyReader {
	kr := uiNewKeyReader()
	safeGo(func() { kr.readPlain(r) })
	safeGo(kr.parse)
	return kr
}

func (kr *KeyReader) readPlain(r io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		for i := 0; i < n; i++ {
			kr.bytes <- buf[i]
		}
		if err != nil {
			close(kr.bytes)
			return
		}
	}
}

// Pause stops reading stdin until the returned resume func is called.
func (kr *KeyReader) Pause() (resume func()) {
	kr.gate.Lock()
	return kr.gate.Unlock
}

func (kr *KeyReader) next(timeout time.Duration) (byte, bool) {
	if kr.hasPend {
		kr.hasPend = false
		return kr.pend, true
	}
	if timeout <= 0 {
		b, ok := <-kr.bytes
		return b, ok
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case b, ok := <-kr.bytes:
		return b, ok
	case <-t.C:
		return 0, false
	}
}

func (kr *KeyReader) unread(b byte) { kr.pend, kr.hasPend = b, true }

func (kr *KeyReader) parse() {
	defer close(kr.Keys)
	for {
		b, ok := kr.next(0)
		if !ok {
			kr.Keys <- Key{Code: KCtrlD}
			return
		}
		switch {
		case b == 0x1b:
			kr.Keys <- kr.escape()
		case b == '\r' || b == '\n':
			kr.Keys <- Key{Code: KEnter}
		case b == 0x7f || b == 0x08:
			kr.Keys <- Key{Code: KBackspace}
		case b == '\t':
			kr.Keys <- Key{Code: KTab}
		case b == 0x01:
			kr.Keys <- Key{Code: KCtrlA}
		case b == 0x03:
			kr.Keys <- Key{Code: KCtrlC}
		case b == 0x04:
			kr.Keys <- Key{Code: KCtrlD}
		case b == 0x05:
			kr.Keys <- Key{Code: KCtrlE}
		case b == 0x07:
			kr.Keys <- Key{Code: KCtrlG}
		case b == 0x0b:
			kr.Keys <- Key{Code: KCtrlK}
		case b == 0x0c:
			kr.Keys <- Key{Code: KCtrlL}
		case b == 0x12:
			kr.Keys <- Key{Code: KCtrlR}
		case b == 0x14:
			kr.Keys <- Key{Code: KCtrlT}
		case b == 0x0f:
			kr.Keys <- Key{Code: KCtrlO}
		case b == 0x15:
			kr.Keys <- Key{Code: KCtrlU}
		case b == 0x17:
			kr.Keys <- Key{Code: KCtrlW}
		case b == 0x02:
			kr.Keys <- Key{Code: KLeft}
		case b == 0x06:
			kr.Keys <- Key{Code: KRight}
		case b == 0x10:
			kr.Keys <- Key{Code: KUp}
		case b == 0x0e:
			kr.Keys <- Key{Code: KDown}
		case b < 0x20:
			// ignore other control bytes
		default:
			kr.Keys <- Key{Code: KRune, R: kr.utf8(b)}
		}
	}
}

func (kr *KeyReader) utf8(b byte) rune {
	n := 0
	switch {
	case b&0xE0 == 0xC0:
		n = 1
	case b&0xF0 == 0xE0:
		n = 2
	case b&0xF8 == 0xF0:
		n = 3
	default:
		if b >= 0x80 {
			return utf8.RuneError
		}
		return rune(b)
	}
	seq := []byte{b}
	for i := 0; i < n; i++ {
		c, ok := kr.next(50 * time.Millisecond)
		if !ok {
			break
		}
		if c&0xC0 != 0x80 { // not a continuation byte: keep it for the next key
			kr.unread(c)
			break
		}
		seq = append(seq, c)
	}
	r, _ := utf8.DecodeRune(seq)
	return r
}

// escape parses what follows an ESC byte. A lone ESC, ESC ESC, or ESC followed
// by a byte we do not map yields KEsc and the following byte is pushed back,
// so it is parsed as its own key (bug 38).
func (kr *KeyReader) escape() Key {
	b, ok := kr.next(30 * time.Millisecond)
	if !ok {
		return Key{Code: KEsc}
	}
	switch b {
	case '\r', '\n':
		return Key{Code: KAltEnter}
	case 'b':
		return Key{Code: KWordLeft}
	case 'f':
		return Key{Code: KWordRight}
	case 0x7f:
		return Key{Code: KCtrlW}
	case 'O':
		c, ok := kr.next(30 * time.Millisecond)
		if !ok {
			return Key{Code: KUnknown}
		}
		switch c {
		case 'H':
			return Key{Code: KHome}
		case 'F':
			return Key{Code: KEnd}
		case 'A':
			return Key{Code: KUp}
		case 'B':
			return Key{Code: KDown}
		case 'C':
			return Key{Code: KRight}
		case 'D':
			return Key{Code: KLeft}
		}
		return Key{Code: KUnknown}
	case '[':
		var params []byte
		for {
			c, ok := kr.next(30 * time.Millisecond)
			if !ok {
				return Key{Code: KUnknown}
			}
			if c >= 0x40 && c <= 0x7e {
				p := string(params)
				switch c {
				case 'A':
					return Key{Code: KUp}
				case 'B':
					return Key{Code: KDown}
				case 'C':
					if p == "1;5" || p == "1;3" {
						return Key{Code: KWordRight}
					}
					return Key{Code: KRight}
				case 'D':
					if p == "1;5" || p == "1;3" {
						return Key{Code: KWordLeft}
					}
					return Key{Code: KLeft}
				case 'H':
					return Key{Code: KHome}
				case 'F':
					return Key{Code: KEnd}
				case 'Z':
					return Key{Code: KShiftTab}
				case '~':
					switch p {
					case "1", "7":
						return Key{Code: KHome}
					case "4", "8":
						return Key{Code: KEnd}
					case "3":
						return Key{Code: KDelete}
					case "200":
						return kr.paste()
					}
				}
				return Key{Code: KUnknown}
			}
			if len(params) > 16 { // garbage, not a key sequence
				return Key{Code: KUnknown}
			}
			params = append(params, c)
		}
	}
	kr.unread(b)
	return Key{Code: KEsc}
}

// paste reads until ESC[201~ (bracketed paste end).
func (kr *KeyReader) paste() Key {
	var buf []byte
	end := []byte("\x1b[201~")
	for {
		c, ok := kr.next(2 * time.Second)
		if !ok {
			break
		}
		buf = append(buf, c)
		if len(buf) >= len(end) && string(buf[len(buf)-len(end):]) == string(end) {
			buf = buf[:len(buf)-len(end)]
			break
		}
	}
	return Key{Code: KPaste, Text: string(buf)}
}
