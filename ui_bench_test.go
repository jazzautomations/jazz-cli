package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
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
