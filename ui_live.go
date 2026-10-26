package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// The live region: the bottom of the transcript that is still changing, i.e.
// the unfinished last line of a streamed answer plus one status line under it
// (spinner, elapsed time, model, phase, what the user is typing). It is
// redrawn by one render goroutine at a bounded frame rate; the token path only
// appends to a buffer, so a fast stream costs a handful of writes per second
// instead of one write (and one terminal repaint) per token.
//
// Invariant while the region is on screen (lv.rows > 0): the terminal cursor
// is parked at column 0 of the region's first row, and everything from there
// down is ours. Parking at column 0 keeps the redraw correct after a resize,
// whether the terminal rewraps lines (VTE, Terminal.app) or not (xterm): the
// cursor stays at the start of the region's content, and "\r\x1b[J" clears it.
// Any other output first commits the region (writes the pending text for good
// and drops the status line), so the transcript order never changes.

// uiLive is the state of the live region (guarded by TermUI.mu).
type uiLive struct {
	on       bool            // live rendering enabled (stdout is a terminal)
	pend     strings.Builder // rendered stream text not yet on screen
	tail     string          // unfinished last line, drawn inside the region
	sgr      string          // SGR state at the start of tail
	rows     int             // rows the region occupies (0 = nothing drawn)
	status   string          // status line last drawn
	hidden   bool            // we hid the cursor
	last     time.Time       // last frame
	msgBytes int             // bytes streamed in the current message
	lastTok  time.Time       // last streamed chunk (stall display)
	toks     int             // streamed chunks in the current request
	loop     bool            // render goroutine running
	frame    int             // spinner frame
	lastAnim time.Time
	phase    string // conectando | aguardando 1º token | gerando | ...
	model    string // model of the request in flight
	writes   int    // frames written (tests, benchmarks)
	tailRows int    // rows of tail in the last frame (fast paths)
	// token count shown in the status line, refreshed once a second (spinLine)
	tokInit              bool
	tokSec               int
	tokReason, tokStream int
}

// uiGlyphOnly reports whether status lines a and b differ only in their
// first visible rune (the spinner glyph); seq redraws that rune with the
// escapes before it.
func uiGlyphOnly(a, b string) (seq string, ok bool) {
	skip := func(s string) int {
		i := 0
		for i < len(s) && s[i] == 0x1b {
			i = uiEscEnd(s, i)
		}
		return i
	}
	ia, ib := skip(a), skip(b)
	if a[:ia] != b[:ib] || ia >= len(a) || ib >= len(b) {
		return "", false
	}
	_, na := utf8.DecodeRuneInString(a[ia:])
	rb, nb := utf8.DecodeRuneInString(b[ib:])
	if a[ia+na:] != b[ib+nb:] || uiRuneW(rb) != 1 {
		return "", false
	}
	return b[:ib+nb], true
}

// uiForceLive turns live rendering on without a terminal (tests, benchmarks).
var uiForceLive bool

// uiWrite is where every TermUI and Editor byte goes.
var uiWrite = func(s string) { os.Stdout.WriteString(s) }

// frame intervals
func uiFrameEvery(long bool) time.Duration {
	switch {
	case uiLowPower() && long:
		return 250 * time.Millisecond
	case uiLowPower():
		return 100 * time.Millisecond
	case long:
		return 200 * time.Millisecond // ≤ 5 fps for long outputs
	}
	return 50 * time.Millisecond // ≤ 20 fps
}

// uiAnimEvery is the spinner animation period (8 fps; 2 fps in low power).
func uiAnimEvery() time.Duration {
	if uiLowPower() {
		return 500 * time.Millisecond
	}
	return 125 * time.Millisecond
}

// uiLongMsg: past this many bytes in one message, frames slow to 5 fps.
const uiLongMsg = 4096

var (
	uiResizeMu    sync.Mutex
	uiResizeHooks []func()
)

// uiOnResize registers f to run after a terminal resize (SIGWINCH).
func uiOnResize(f func()) {
	uiResizeMu.Lock()
	uiResizeHooks = append(uiResizeHooks, f)
	uiResizeMu.Unlock()
}

func uiResizeNotify() {
	uiResizeMu.Lock()
	hs := append([]func(){}, uiResizeHooks...)
	uiResizeMu.Unlock()
	for _, h := range hs {
		h()
	}
}

// sgrTrack folds the SGR sequences of s into state: a reset clears it.
func sgrTrack(state, s string) string {
	for i := 0; i < len(s); {
		if s[i] != 0x1b {
			i++
			continue
		}
		j := uiEscEnd(s, i)
		seq := s[i:j]
		if len(seq) >= 3 && seq[1] == '[' && seq[len(seq)-1] == 'm' {
			if p := seq[2 : len(seq)-1]; p == "0" || p == "" {
				state = ""
			} else if len(state) < 256 {
				state += seq
			}
		}
		i = j
	}
	return state
}

// uiTrackNL updates col0/nl after s was written (same rules as rawWrite).
func (u *TermUI) uiTrackNL(s string) {
	if s == "" {
		return
	}
	u.col0 = strings.HasSuffix(s, "\n")
	switch t := strings.TrimRight(s, "\n"); {
	case t == "":
		u.nl += len(s)
	case u.col0:
		u.nl = len(s) - len(t)
	default:
		u.nl = 0
	}
}

// liveCommit writes everything pending for good and removes the status line,
// leaving the cursor at the logical end of the output (u.mu held).
func (u *TermUI) liveCommit() {
	lv := &u.lv
	if lv.rows == 0 && lv.pend.Len() == 0 {
		return
	}
	var b strings.Builder
	if lv.rows > 0 {
		b.WriteString("\r\x1b[J\x1b[0m" + lv.sgr + lv.tail)
	}
	p := lv.pend.String()
	b.WriteString(p)
	if lv.hidden {
		b.WriteString("\x1b[?25h")
		lv.hidden = false
	}
	uiWrite(b.String())
	lv.writes++
	if lv.rows > 0 || p != "" {
		u.uiTrackNL(lv.tail + p)
	}
	lv.sgr = sgrTrack(lv.sgr, lv.tail+p)
	lv.tail, lv.rows, lv.status = "", 0, ""
	lv.pend.Reset()
	u.toolFresh = false
}

// liveWanted: a status line belongs on screen.
func (u *TermUI) liveWanted() bool { return u.spinOn }

// liveRender draws one frame (u.mu held). force skips the frame-rate check.
func (u *TermUI) liveRender(force bool) {
	lv := &u.lv
	if !lv.on {
		return
	}
	now := time.Now()
	if !force && now.Sub(lv.last) < uiFrameEvery(lv.msgBytes > uiLongMsg) {
		return
	}
	want := u.liveWanted()
	if lv.rows == 0 && !u.col0 && lv.tail == "" {
		// the output ends mid-line outside the stream: nothing can be drawn
		// under it without changing it; just write the pending text
		if lv.pend.Len() > 0 {
			p := lv.pend.String()
			lv.pend.Reset()
			uiWrite(p)
			lv.writes++
			u.uiTrackNL(p)
			lv.sgr = sgrTrack(lv.sgr, p)
		}
		lv.last = now
		return
	}
	w := termWidth()
	status, qRows := "", 0
	if want {
		var qs string
		qs, qRows = u.qLive(w) // message queue under the status line (queue.go)
		status = u.spinLine(lv.frame) + qs
	}
	if lv.pend.Len() == 0 && status == lv.status && (lv.rows > 0 || status == "") {
		return // nothing changed
	}
	if u.liveFast(status, w) {
		lv.last = now
		return
	}
	p := lv.pend.String()
	lv.pend.Reset()
	full := lv.tail + p
	var b strings.Builder
	if lv.rows > 0 {
		b.WriteString("\r\x1b[J\x1b[0m" + lv.sgr)
	}
	b.WriteString(full)
	tail := full
	if i := strings.LastIndexByte(full, '\n'); i >= 0 {
		done := full[:i+1]
		lv.sgr = sgrTrack(lv.sgr, done)
		u.uiTrackNL(done)
		tail = full[i+1:]
	}
	tw := uiStrW(tail)
	tailRows := 0
	if tail != "" {
		tailRows = max(1, (tw+w-1)/w)
	}
	if tailRows > 2 || (status == "" && tail == "") {
		// a very long unbroken line (a wide fenced line): leave it as written
		if tail != "" {
			u.uiTrackNL(tail)
			lv.sgr = sgrTrack(lv.sgr, tail)
		}
		if lv.hidden {
			b.WriteString("\x1b[?25h")
			lv.hidden = false
		}
		uiWrite(b.String())
		lv.writes++
		lv.tail, lv.rows, lv.status, lv.last = "", 0, "", now
		return
	}
	rows := tailRows
	if status != "" {
		if tail != "" {
			b.WriteString("\x1b[0m\r\n")
		}
		b.WriteString("\x1b[0m" + status + "\x1b[0m\x1b[K")
		rows += 1 + qRows
	}
	if !lv.hidden {
		b.WriteString("\x1b[?25l")
		lv.hidden = true
	}
	b.WriteString("\r")
	if rows > 1 {
		b.WriteString("\x1b[" + itoa(rows-1) + "A")
	}
	uiWrite(b.String())
	lv.writes++
	lv.tail, lv.rows, lv.status, lv.last, lv.tailRows = tail, rows, status, now, tailRows
}

// liveFast draws a frame that changes little without rewriting the region:
// the spinner glyph alone, and/or stream text that stays on the tail's row
// (written at the end of the tail). It reports whether it drew the frame.
// u.mu held; the cursor stays parked at column 0 of the region's first row.
func (u *TermUI) liveFast(status string, w int) bool {
	lv := &u.lv
	if lv.rows == 0 || status == "" || lv.status == "" || lv.tailRows > 1 {
		return false
	}
	glyph := ""
	if status != lv.status {
		seq, ok := uiGlyphOnly(lv.status, status)
		if !ok {
			return false
		}
		glyph = seq
	}
	p := lv.pend.String()
	if p != "" && (lv.tailRows != 1 || strings.ContainsAny(p, "\n\r") || uiStrW(lv.tail)+uiStrW(p) >= w) {
		return false
	}
	var b strings.Builder
	if p != "" {
		b.WriteString("\r\x1b[0m" + sgrTrack(lv.sgr, lv.tail))
		if tw := uiStrW(lv.tail); tw > 0 {
			b.WriteString("\x1b[" + itoa(tw) + "C")
		}
		b.WriteString(p)
		lv.tail += p
		lv.pend.Reset()
	}
	if glyph != "" {
		b.WriteString("\x1b[0m\r")
		if lv.tailRows > 0 {
			b.WriteString("\x1b[" + itoa(lv.tailRows) + "B")
		}
		b.WriteString(glyph + "\x1b[0m\r")
		if lv.tailRows > 0 {
			b.WriteString("\x1b[" + itoa(lv.tailRows) + "A")
		}
		lv.status = status
	} else {
		b.WriteString("\x1b[0m\r")
	}
	uiWrite(b.String())
	lv.writes++
	return true
}

// liveKick makes sure the render goroutine runs (u.mu held).
func (u *TermUI) liveKick() {
	lv := &u.lv
	if !lv.on || lv.loop {
		return
	}
	lv.loop = true
	safeGo(u.liveLoop)
}

func (u *TermUI) liveLoop() {
	tk := time.NewTicker(uiFrameEvery(false))
	defer tk.Stop()
	for range tk.C {
		u.mu.Lock()
		lv := &u.lv
		if u.spinOn && time.Since(lv.lastAnim) >= uiAnimEvery() {
			lv.frame++
			lv.lastAnim = time.Now()
		}
		u.liveRender(false)
		if !u.spinOn && lv.pend.Len() == 0 {
			lv.loop = false
			u.mu.Unlock()
			return
		}
		u.mu.Unlock()
	}
}

// liveResize redraws the region at the new width (SIGWINCH).
func (u *TermUI) liveResize() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.inText {
		u.md.width = termWidth() - 1 // the rest of the answer wraps to the new width
	}
	if u.lv.rows > 0 {
		u.lv.status = "\x00" // force a redraw
		u.liveRender(true)
	}
}

// ---------- request phases (status line) ----------

// uiPhaser is implemented by UIs that show the phase of a model request.
type uiPhaser interface {
	Phase(model, phase string)
}

// Phase shows what the request in flight is doing: "conectando",
// "aguardando 1º token", "gerando", "sem rede?" …
func (u *TermUI) Phase(model, phase string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if model != "" {
		u.lv.model = model
	}
	if phase != u.lv.phase {
		u.lv.phase = phase
		if phase == "conectando" {
			u.lv.toks = 0
		}
		u.liveRender(true)
	}
}

// uiStatusKeeper: the UI keeps its status line while the answer streams
// (the agent then does not stop the spinner on the first token).
type uiStatusKeeper interface{ KeepsStatus() bool }

// KeepsStatus: with live rendering the status line stays under the stream.
func (u *TermUI) KeepsStatus() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.lv.on
}

// uiShortModel is the last path segment of a model ref, without ":free".
func uiShortModel(ref string) string {
	if i := strings.LastIndexByte(ref, '/'); i >= 0 {
		ref = ref[i+1:]
	}
	return strings.TrimSuffix(ref, ":free")
}

// livePhaseInfo is the phase part of the status line (u.mu held).
func (u *TermUI) livePhaseInfo() []string {
	lv := &u.lv
	var info []string
	if lv.model != "" {
		info = append(info, uiShortModel(lv.model))
	}
	ph := lv.phase
	if ph == "gerando" && !lv.lastTok.IsZero() {
		if idle := time.Since(lv.lastTok); idle >= 5*time.Second {
			ph = fmt.Sprintf("parado há %s", idle.Round(time.Second))
		}
	}
	if ph != "" {
		info = append(info, ph)
	}
	return info
}
