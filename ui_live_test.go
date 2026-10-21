package main

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// uiLiveRig is a TermUI writing into a virtual terminal of w x h.
type uiLiveRig struct {
	u  *TermUI
	vt *vtScreen
	mu *sync.Mutex
}

func uiNewLiveRig(t *testing.T, w, h int, live, reflow bool) *uiLiveRig {
	t.Helper()
	vt := newVT(t, w, h)
	vt.reflow = reflow
	var mu sync.Mutex
	oldW, oldF := uiWrite, uiForceLive
	uiWrite = func(s string) { mu.Lock(); vt.feed(s); mu.Unlock() }
	uiForceLive = live
	uiWidthOverride.Store(int32(w))
	t.Cleanup(func() { uiWrite, uiForceLive = oldW, oldF; uiWidthOverride.Store(0) })
	t.Setenv("JAZZ_HOME", t.TempDir())
	u := NewTermUI(&Config{}, nil)
	u.T = Theme{color: true, c256: true}
	u.md.t = u.T
	u.lv.on = live
	t.Cleanup(func() { u.StopThinking(); u.Flush() }) // runs before uiWrite is restored
	return &uiLiveRig{u: u, vt: vt, mu: &mu}
}

func (r *uiLiveRig) frame() {
	r.u.mu.Lock()
	r.u.liveRender(true)
	r.u.mu.Unlock()
}

func (r *uiLiveRig) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.vt.All()
}

func (r *uiLiveRig) screen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.vt.Screen()
}

func (r *uiLiveRig) resize(w, h int) {
	r.mu.Lock()
	r.vt.resize(w, h)
	r.mu.Unlock()
	uiWidthOverride.Store(int32(w))
	r.u.liveResize()
}

// uiStreamTurn plays a turn: spinner, streamed answer, end.
func uiStreamTurn(r *uiLiveRig, toks []string, during func(i int)) {
	r.u.Thinking("pensando")
	r.u.Phase("kilo/poolside/laguna-s-2.1:free", "conectando")
	r.u.Phase("", "aguardando 1º token")
	for i, tk := range toks {
		r.u.StreamText(tk)
		if during != nil {
			during(i)
		}
	}
	r.u.StopThinking()
	r.u.mu.Lock()
	r.u.endText()
	r.u.mu.Unlock()
	r.u.Notice("fim")
}

// The transcript after a live-rendered turn is exactly what direct writes
// produce: frames never duplicate, reorder or lose text.
func TestUILiveTranscriptMatchesDirect(t *testing.T) {
	toks := uiBenchTokens(uiBenchAnswer(), 600)
	direct := uiNewLiveRig(t, 86, 27, false, false)
	uiStreamTurn(direct, toks, nil)
	want := direct.all()

	live := uiNewLiveRig(t, 86, 27, true, false)
	uiStreamTurn(live, toks, func(i int) {
		if i%7 == 0 {
			live.frame()
		}
	})
	got := live.all()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("live transcript differs\n--- direct\n%s\n--- live\n%s", strings.Join(want, "\n"), live.vt.Dump())
	}
	if live.vt.hidden {
		t.Error("cursor left hidden after the turn")
	}
	if len(live.vt.badEscapes) > 0 {
		t.Errorf("unexpected sequences: %v", live.vt.badEscapes)
	}
	if live.u.lv.writes >= len(toks)/2 {
		t.Errorf("%d writes for %d tokens: frames are not coalesced", live.u.lv.writes, len(toks))
	}
}

// While streaming, the status line sits under the text with the model and
// the phase, and moves down as lines complete; it never stays behind.
func TestUILiveStatusUnderStream(t *testing.T) {
	r := uiNewLiveRig(t, 86, 27, true, false)
	r.u.Thinking("pensando")
	r.u.Phase("kilo/poolside/laguna-s-2.1:free", "aguardando 1º token")
	sc := strings.Join(r.screen(), "\n")
	if !strings.Contains(sc, "laguna-s-2.1") || !strings.Contains(sc, "aguardando 1º token") || !strings.Contains(sc, "esc pra interromper") {
		t.Fatalf("status before the first token:\n%s", sc)
	}
	r.u.StreamText("Primeira linha da resposta.\nSegunda linha ainda sendo")
	r.frame()
	all := r.all()
	last := all[len(all)-1]
	if !strings.Contains(last, "gerando") || !strings.Contains(all[len(all)-2], "Segunda linha ainda") {
		t.Fatalf("status not under the tail:\n%s", r.vt.Dump())
	}
	r.u.mu.Lock()
	r.u.typed = "tô digitando 🎷"
	r.u.liveRender(true)
	r.u.mu.Unlock()
	all = r.all()
	if !strings.Contains(all[len(all)-1], "tô digitando 🎷") {
		t.Fatalf("typed text not echoed during the stream:\n%s", r.vt.Dump())
	}
	r.u.StreamText(" escrita.\n")
	r.u.StopThinking()
	r.u.Flush()
	for _, l := range r.all() {
		if strings.Contains(l, "esc pra interromper") || strings.Contains(l, "gerando") || strings.Contains(l, "digitando") {
			t.Fatalf("status left in the transcript:\n%s", r.vt.Dump())
		}
	}
	if r.vt.hidden {
		t.Error("cursor hidden")
	}
}

// A resize in the middle of the stream (86 -> 80 -> 86) leaves no partial
// status lines behind, with and without terminal reflow.
func TestUILiveResizeMidStream(t *testing.T) {
	for _, reflow := range []bool{false, true} {
		r := uiNewLiveRig(t, 86, 27, true, reflow)
		toks := uiChunks(uiBenchAnswer(), 4)
		uiStreamTurn(r, toks, func(i int) {
			switch i {
			case 100:
				r.frame()
				r.resize(80, 24)
			case 600:
				r.frame()
				r.resize(86, 27)
			}
			if i%5 == 0 {
				r.frame()
			}
		})
		for _, l := range r.all() {
			if strings.Contains(l, "esc pra interromper") || strings.Contains(l, "aguardando") || strings.Contains(l, "gerando") {
				t.Fatalf("reflow=%v: status left behind:\n%s", reflow, r.vt.Dump())
			}
		}
		a := r.all()
		if !strings.Contains(strings.Join(a, "\n"), "Texto final com negrito") || a[len(a)-1] != "  · fim" {
			t.Fatalf("reflow=%v: transcript damaged:\n%s", reflow, r.vt.Dump())
		}
	}
}

// The status line shows within one frame of Thinking (the 300 ms promise).
func TestUILiveStatusImmediate(t *testing.T) {
	r := uiNewLiveRig(t, 86, 27, true, false)
	t0 := time.Now()
	r.u.Thinking("preparando")
	if d := time.Since(t0); d > 300*time.Millisecond {
		t.Fatalf("Thinking took %s", d)
	}
	if sc := strings.Join(r.screen(), "\n"); !strings.Contains(sc, "Preparando") {
		t.Fatalf("no status right after Thinking:\n%s", sc)
	}
	r.u.StopThinking()
}

// uiChunks splits s into chunks of n runes.
func uiChunks(s string, n int) []string {
	r := []rune(s)
	var out []string
	for i := 0; i < len(r); i += n {
		out = append(out, string(r[i:min(i+n, len(r))]))
	}
	return out
}

// Paced like a fast model (1000 tokens at 2 ms each), the stream costs a
// bounded number of frames: ≤ 20 fps, ≤ 5 fps once the message is long.
func TestUILiveFrameRate(t *testing.T) {
	if testing.Short() {
		t.Skip("2 s")
	}
	r := uiNewLiveRig(t, 86, 27, true, false)
	toks := uiBenchTokens(uiBenchAnswer(), 1000)
	t0 := time.Now()
	r.u.Thinking("pensando")
	for _, tk := range toks {
		r.u.StreamText(tk)
		time.Sleep(2 * time.Millisecond)
	}
	r.u.StopThinking()
	r.u.Flush()
	el := time.Since(t0)
	frames := r.u.lv.writes
	t.Logf("%d tokens in %s: %d frames (%.1f fps; one write per chunk would be %d)", len(toks), el.Round(time.Millisecond), frames, float64(frames)/el.Seconds(), len(toks))
	if fps := float64(frames) / el.Seconds(); fps > 22 {
		t.Fatalf("%.1f fps", fps)
	}
}
