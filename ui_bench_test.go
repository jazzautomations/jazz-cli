package main

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// uiBenchAnswer is a realistic streamed answer: prose, a list, bold/code
// spans and two fenced code blocks.
func uiBenchAnswer() string {
	var b strings.Builder
	b.WriteString("## Resumo da mudança\n\nO **parser** agora aceita `--low-power` e mantém a saída estável. Detalhes:\n\n")
	for i := 0; i < 6; i++ {
		b.WriteString("- item com acentuação ção ã é e um `trecho` de código e texto comprido o bastante pra quebrar a linha no terminal de 86 colunas\n")
	}
	b.WriteString("\n```go\n")
	for i := 0; i < 30; i++ {
		b.WriteString("func exemplo(x int) int { return x*2 + 1 } // linha de código\n")
	}
	b.WriteString("```\n\nTexto final com **negrito** e mais prosa pra completar a resposta do modelo.\n")
	return b.String()
}

// uiBenchTokens splits s into n chunks of roughly equal size (SSE tokens).
func uiBenchTokens(s string, n int) []string {
	r := []rune(s)
	for len(r) < n*4 {
		r = append(r, r...)
	}
	r = r[:n*4]
	out := make([]string, 0, n)
	for i := 0; i < len(r); i += 4 {
		out = append(out, string(r[i:min(i+4, len(r))]))
	}
	return out
}

// uiBenchStream streams 2000 tokens (one answer) through the TermUI into
// /dev/null: the CPU, allocation and write-syscall cost of the token path.
// live=false is the direct path (one write per chunk, what every terminal
// got before the live region); live=true buffers and writes frames.
func uiBenchStream(b *testing.B, live bool) {
	toks := uiBenchTokens(uiBenchAnswer(), 2000)
	null, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	oldW, oldF := uiWrite, uiForceLive
	writes := 0
	uiWrite = func(s string) { writes++; null.WriteString(s) }
	uiForceLive = live
	uiWidthOverride.Store(86)
	defer func() { uiWrite, uiForceLive = oldW, oldF; uiWidthOverride.Store(0); null.Close() }()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		u := NewTermUI(&Config{}, nil)
		u.T = Theme{color: true, c256: true}
		u.md.t = u.T
		u.Thinking("pensando")
		for _, t := range toks {
			u.StreamText(t)
		}
		u.StopThinking()
		u.mu.Lock()
		u.endText()
		u.liveCommit()
		u.mu.Unlock()
	}
	b.ReportMetric(float64(writes)/float64(b.N), "writes/op")
}

func BenchmarkUIStream2000Tokens(b *testing.B)     { uiBenchStream(b, false) }
func BenchmarkUIStream2000TokensLive(b *testing.B) { uiBenchStream(b, true) }

// BenchmarkUIEditorKeystroke is one keystroke in the boxed input: the whole
// frame (box, footer) is rebuilt and written.
func BenchmarkUIEditorKeystroke(b *testing.B) {
	null, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	oldW := uiWrite
	uiWrite = func(s string) { null.WriteString(s) }
	defer func() { uiWrite = oldW; null.Close() }()
	u := NewTermUI(&Config{}, nil)
	u.T = Theme{color: true, c256: true}
	e := &Editor{ui: u}
	e.Footer = func() string { return u.Footer("kilo/poolside/laguna-s-2.1:free", 12000, 128000, 0, 0) }
	buf := []rune("explica o que faz a função parseArgs em main.go e sugere testes 🎷 ação")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.drawBox(buf, len(buf))
	}
}

// BenchmarkSSEParse2000 parses 2000 OpenAI-style SSE chunks (the network
// side of the token path, for scale).
func BenchmarkSSEParse2000(b *testing.B) {
	var sb strings.Builder
	for i := 0; i < 2000; i++ {
		sb.WriteString(`data: {"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"pala"}}]}` + "\n\n")
	}
	raw := sb.String()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = readSSE(strings.NewReader(raw), func(bool) {}, func(_, data string) error {
			var c oaChunk
			_ = json.Unmarshal([]byte(data), &c)
			return nil
		})
	}
}

// uiRedrawRate runs the live region for d with the spinner on (idle wait for
// the model, or a stream of tps tokens per second when tps > 0) and returns
// the bytes written per second that are not answer text (the redraw cost).
func uiRedrawRate(t testing.TB, d time.Duration, tps int) (overhead, total float64) {
	var mu sync.Mutex
	n := 0
	oldW, oldF := uiWrite, uiForceLive
	uiWrite = func(s string) { mu.Lock(); n += len(s); mu.Unlock() }
	uiForceLive = true
	uiWidthOverride.Store(86)
	defer func() { uiWrite, uiForceLive = oldW, oldF; uiWidthOverride.Store(0) }()
	u := NewTermUI(&Config{}, nil)
	u.T = Theme{color: true, c256: true}
	u.md.t = u.T
	u.Thinking("pensando")
	text := 0
	t0 := time.Now()
	if tps > 0 {
		toks := uiBenchTokens(uiBenchAnswer(), int(float64(tps)*d.Seconds()))
		every := time.Second / time.Duration(tps)
		for _, tk := range toks {
			u.StreamText(tk)
			text += len(tk)
			time.Sleep(every)
		}
	} else {
		time.Sleep(d)
	}
	el := time.Since(t0).Seconds()
	u.StopThinking()
	u.mu.Lock()
	u.endText()
	u.liveCommit()
	u.mu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	// the rendered answer is longer than its source (colour codes, wrapping):
	// count it once as written by the direct path
	direct := 0
	if tps > 0 {
		m := mdRenderer{t: u.T, lineStart: true, indent: "  "}
		m.begin(2, 85)
		direct = len(m.Feed(strings.Join(uiBenchTokens(uiBenchAnswer(), int(float64(tps)*d.Seconds())), ""))) + len(m.Flush())
	}
	return float64(n-direct) / el, float64(n) / el
}

// uiRedrawBudget is the most redraw bytes per second the live region may
// write: the spinner and status line on a 2005 laptop over SSH (a 56k modem
// moves ~7 KB/s; this keeps the status line under a tenth of that).
const (
	uiRedrawBudgetIdle   = 600  // B/s waiting for the model
	uiRedrawBudgetStream = 2500 // B/s on top of the answer text at 30 tokens/s
)

// TestUIRedrawBudget measures the redraw bytes per second of the status
// line while waiting and while streaming, and keeps them under budget.
func TestUIRedrawBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("timed")
	}
	idle, _ := uiRedrawRate(t, 2*time.Second, 0)
	stream, total := uiRedrawRate(t, 2*time.Second, 30)
	t.Logf("redraw: esperando %.0f B/s (limite %d) · streaming 30 tok/s: %.0f B/s além do texto (limite %d), %.0f B/s no total", idle, uiRedrawBudgetIdle, stream, uiRedrawBudgetStream, total)
	if idle > uiRedrawBudgetIdle {
		t.Errorf("idle redraw %.0f B/s > %d", idle, uiRedrawBudgetIdle)
	}
	if stream > uiRedrawBudgetStream {
		t.Errorf("stream redraw %.0f B/s > %d", stream, uiRedrawBudgetStream)
	}
}

// BenchmarkUIRedrawIdle reports the idle status-line cost as B/s.
func BenchmarkUIRedrawIdle(b *testing.B) {
	for i := 0; i < b.N; i++ {
		idle, _ := uiRedrawRate(b, time.Second, 0)
		b.ReportMetric(idle, "B/s")
	}
}
