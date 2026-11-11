package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func cvBig(tag string, n int) string { return tag + "\n" + strings.Repeat("x", n) }

func TestCvMaskKeepsRecentAndSmall(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "arruma o teste"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "1", Name: "bash", Args: `{"command":"go test ./..."}`}}},
		{Role: "tool", ToolCallID: "1", ToolName: "bash", Content: cvBig("FAIL pkg", 5000) + "\n[… Full output saved to /tmp/spill/out-1.txt — use rg on it.]"},
		{Role: "assistant", Content: cvBig("vou ler", 3000), ToolCalls: []ToolCall{{ID: "2", Name: "read", Args: `{"path":"a.go"}`}}},
		{Role: "tool", ToolCallID: "2", ToolName: "read", Content: "curto"},
		{Role: "tool", ToolCallID: "3", ToolName: "read", Content: cvBig("r3", 4000)},
		{Role: "tool", ToolCallID: "4", ToolName: "read", Content: cvBig("r4", 4000)},
		{Role: "tool", ToolCallID: "5", ToolName: "read", Content: cvBig("r5", 4000)},
	}
	orig := msgs[2].Content
	out, st := cvMask(msgs, cvPolicy{Keep: 3, MinBytes: 1000})
	if st.Masked != 1 {
		t.Fatalf("masked = %d, quer 1 (só o resultado antigo e grande)", st.Masked)
	}
	if msgs[2].Content != orig {
		t.Fatal("cvMask alterou a entrada")
	}
	stub := out[2].Content
	for _, want := range []string{"bash result elided to save context", "FAIL pkg", "/tmp/spill/out-1.txt", "5075 bytes"} {
		if !strings.Contains(stub, want) {
			t.Errorf("aviso sem %q: %s", want, stub)
		}
	}
	if len(stub) > 400 {
		t.Errorf("aviso com %d bytes", len(stub))
	}
	if out[2].ToolCallID != "1" || out[2].Role != "tool" {
		t.Error("o aviso tem que continuar sendo o resultado da mesma chamada")
	}
	for _, i := range []int{0, 1, 3, 4, 5, 6, 7} {
		if out[i].Content != msgs[i].Content {
			t.Errorf("mensagem %d mudou: fala do agente, resultado curto e os 3 recentes ficam literais", i)
		}
	}
	if st.After >= st.Before || st.Before-st.After < 4500 {
		t.Errorf("economia errada: %d → %d", st.Before, st.After)
	}
	// aplicar de novo não mexe no aviso (a visão é estável entre chamadas)
	again, st2 := cvMask(out, cvPolicy{Keep: 3, MinBytes: 100})
	if again[2].Content != stub {
		t.Errorf("aviso reescrito na segunda passada (masked=%d)", st2.Masked)
	}
}

func TestCvMaskNothingToDo(t *testing.T) {
	msgs := []Message{{Role: "user", Content: "oi"}, {Role: "tool", Content: cvBig("a", 9000)}}
	out, st := cvMask(msgs, cvDefault())
	if st.Masked != 0 || &out[0] != &msgs[0] {
		t.Fatal("sem nada antigo, a mesma lista tem que voltar")
	}
}

func TestEcActionAndSame(t *testing.T) {
	a := ecAction([]ToolCall{{Name: "bash", Args: `{"command":"cd x && go test ./... -run Foo"}`}})
	b := ecAction([]ToolCall{{Name: "bash", Args: `{"command":"go test ./pkg"}`}})
	if a.Target != "go test" || b.Target != "go test" {
		t.Fatalf("alvo de comando: %q, %q", a.Target, b.Target)
	}
	if n, tg, ar := ecSame(a, b); !n || !tg || ar {
		t.Fatalf("mesma ferramenta e alvo, argumentos diferentes: %v %v %v", n, tg, ar)
	}
	r1 := ecAction([]ToolCall{{Name: "read", Args: `{"path":"a.go","limit":10}`}})
	r2 := ecAction([]ToolCall{{Name: "read", Args: `{ "limit": 10, "path": "a.go" }`}})
	if _, _, ar := ecSame(r1, r2); !ar {
		t.Fatalf("ordem de chave não é diferença: %q × %q", r1.Args, r2.Args)
	}
	if ecAction(nil).Name != "(text)" {
		t.Fatal("resposta sem chamada é (text)")
	}
}

// A recorded session is replayed with a stub model: the scan finds the
// decision the policy changes, the view really is smaller, and a model that
// changes its action under the view is counted as a disagreement.
func TestEvalContextReplay(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	var lines []string
	id := 0
	add := func(e sessEntry) {
		id++
		prev := ""
		if id > 1 {
			prev = "e" + itoa(id-1)
		}
		e.ID, e.ParentID = "e"+itoa(id), prev
		b, _ := json.Marshal(e)
		lines = append(lines, string(b))
	}
	add(sessEntry{T: "meta", V: 2, Model: "x/y", System: "sys"})
	add(sessEntry{T: "msg", Msg: &Message{Role: "user", Content: "arruma"}})
	for i := 1; i <= 5; i++ {
		c := "c" + itoa(i)
		add(sessEntry{T: "msg", Msg: &Message{Role: "assistant", ToolCalls: []ToolCall{{ID: c, Name: "read", Args: `{"path":"f` + itoa(i) + `.go"}`}}}})
		add(sessEntry{T: "msg", Msg: &Message{Role: "tool", ToolCallID: c, ToolName: "read", Content: cvBig("arquivo "+itoa(i), 3000)}})
	}
	add(sessEntry{T: "msg", Msg: &Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "c6", Name: "edit", Args: `{"path":"f1.go"}`}}}})
	file := filepath.Join(proj, "s.jsonl")
	if err := os.WriteFile(file, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pts := ecScan(file, cvDefault())
	if len(pts) != 2 { // the 5th read (1 old result) and the edit (2 old results)
		t.Fatalf("pontos = %d, quer 2: %+v", len(pts), pts)
	}
	old := ecAsk
	defer func() { ecAsk = old }()
	calls := 0
	ecAsk = func(ctx context.Context, cfg *Config, ref, system string, msgs []Message) (*ChatResponse, error) {
		calls++
		if system != "sys" {
			t.Errorf("system = %q", system)
		}
		for _, m := range msgs {
			if strings.HasPrefix(m.Content, cvStubMark) { // the model lost the file: it reads again
				return &ChatResponse{ToolCalls: []ToolCall{{Name: "read", Args: `{"path":"f1.go"}`}}, Usage: Usage{In: 10}}, nil
			}
		}
		return &ChatResponse{ToolCalls: []ToolCall{{Name: "edit", Args: `{"path":"f1.go"}`}}, Usage: Usage{In: 30}}, nil
	}
	row := ecReplayPoint(nil, "x/y", pts[1], ecOpts{pol: cvDefault()})
	if row.Infra != "" || calls != 3 {
		t.Fatalf("infra=%q calls=%d", row.Infra, calls)
	}
	if row.Masked != 2 || row.After >= row.Before {
		t.Fatalf("visão: %+v", row)
	}
	s := ecSummarize([]ecRow{row, {Infra: "429"}})
	if s.Valid != 1 || s.Infra != 1 || s.SelfName != 1 || s.ViewName != 0 || s.RecFull != 1 || s.RecView != 0 {
		t.Fatalf("resumo: %+v", s)
	}
	if !strings.Contains(s.text(), "0/1") {
		t.Errorf("texto: %s", s.text())
	}
}

func TestCvMaskStaleReads(t *testing.T) {
	rd := func(id, path string) Message {
		return Message{Role: "assistant", ToolCalls: []ToolCall{{ID: id, Name: "read", Args: `{"path":"` + path + `"}`}}}
	}
	res := func(id, tag string) Message {
		return Message{Role: "tool", ToolCallID: id, ToolName: "read", Content: cvBig(tag, 3000)}
	}
	msgs := []Message{
		rd("1", "a.go"), res("1", "a v1"), // 1: read again at 5 → stale
		rd("2", "b.go"), res("2", "b v1"), // 3: edited at 6, never read again
		rd("3", "a.go"), res("3", "a v2"), // 5: newest read of a.go → stays
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "4", Name: "edit", Args: `{"path":"b.go"}`}}},
		{Role: "tool", ToolCallID: "4", ToolName: "edit", Content: "ok"},
		rd("5", "c.go"), res("5", "c v1"), // 9: only read of c.go → stays
	}
	out, st := cvMask(msgs, cvPolicy{Keep: 0, MinBytes: 1000, Reads: "stale"})
	if st.Masked != 1 || !strings.Contains(out[1].Content, "read again later") {
		t.Fatalf("stale: masked=%d %q", st.Masked, out[1].Content[:80])
	}
	if out[3].Content != msgs[3].Content || out[5].Content != msgs[5].Content || out[9].Content != msgs[9].Content {
		t.Fatal("stale: leitura única ou mais nova não pode virar aviso")
	}
	out, st = cvMask(msgs, cvPolicy{Keep: 0, MinBytes: 1000, Reads: "edited"})
	if st.Masked != 2 || !strings.Contains(out[3].Content, "changed after this read") {
		t.Fatalf("edited: masked=%d %q", st.Masked, out[3].Content[:80])
	}
	// uma leitura parcial não torna a leitura inteira redundante
	part := []Message{
		rd("1", "a.go"), res("1", "inteiro"),
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "2", Name: "read", Args: `{"path":"a.go","offset":10,"limit":5}`}}},
		res("2", "pedaço"),
	}
	if _, st := cvMask(part, cvPolicy{MinBytes: 1000, Reads: "stale"}); st.Masked != 0 {
		t.Fatalf("leitura parcial apagou a inteira (masked=%d)", st.Masked)
	}
}

func TestCvApplyFollowsSetting(t *testing.T) {
	msgs := []Message{
		{Role: "tool", ToolName: "bash", Content: cvBig("velho", 4000)},
		{Role: "tool", ToolName: "bash", Content: "a"}, {Role: "tool", ToolName: "bash", Content: "b"}, {Role: "tool", ToolName: "bash", Content: "c"},
	}
	prev := opSetSettings(opDefaultSettings())
	defer opSetSettings(prev)
	if out := cvApply(msgs); out[0].Content != msgs[0].Content {
		t.Fatal("desligado por padrão: o histórico vai inteiro")
	}
	s := opDefaultSettings()
	s.CtxView = "tier"
	opSetSettings(s)
	if out := cvApply(msgs); !strings.HasPrefix(out[0].Content, cvStubMark) {
		t.Fatal("tier: resultado antigo e grande vira aviso")
	}
	got := opLoadSettings(filepath.Join(t.TempDir(), "nao-existe.json"), func(k string) string {
		if k == "JAZZ_CONTEXT_VIEW" {
			return "edited"
		}
		return ""
	})
	if got.CtxView != "edited" {
		t.Fatalf("JAZZ_CONTEXT_VIEW=edited → %q", got.CtxView)
	}
	if bad := opLoadSettings("", func(string) string { return "qualquer" }); bad.CtxView != "off" {
		t.Fatalf("valor inválido tem que ficar off, veio %q", bad.CtxView)
	}
}

func TestEvalContextStopsOnQuota(t *testing.T) {
	old := ecAsk
	defer func() { ecAsk = old }()
	ecAsk = func(ctx context.Context, cfg *Config, ref, system string, msgs []Message) (*ChatResponse, error) {
		return nil, &APIError{Status: 429, Body: "PROMOTION_MODEL_LIMIT_REACHED", Quota: true, Daily: true}
	}
	dir := t.TempDir()
	e := func(id, parent string, m *Message) string {
		b, _ := json.Marshal(sessEntry{T: "msg", ID: id, ParentID: parent, Msg: m})
		return string(b)
	}
	meta, _ := json.Marshal(sessEntry{T: "meta", ID: "m", V: 2, System: "sys"})
	lines := []string{string(meta),
		e("a", "m", &Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "1", Name: "bash", Args: `{"command":"ls"}`}}}),
		e("b", "a", &Message{Role: "tool", ToolCallID: "1", ToolName: "bash", Content: cvBig("x", 3000)}),
		e("c", "b", &Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "2", Name: "bash", Args: `{"command":"pwd"}`}}}),
	}
	file := filepath.Join(dir, "s.jsonl")
	if err := os.WriteFile(file, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pts := ecScan(file, cvPolicy{Keep: 0, MinBytes: 1000})
	if len(pts) != 1 {
		t.Fatalf("pontos = %d", len(pts))
	}
	row := ecReplayPoint(nil, "x/y", pts[0], ecOpts{pol: cvPolicy{Keep: 0, MinBytes: 1000}})
	if !row.quota || row.Infra == "" {
		t.Fatalf("cota esgotada tem que marcar a linha: %+v", row)
	}
}

// Without a token count from the provider the estimate has to follow what is
// sent, or the view would save tokens and compaction would still fire early.
func TestEstTokensFollowsView(t *testing.T) {
	a := &Agent{System: "s"}
	for i := 0; i < 6; i++ {
		a.Msgs = append(a.Msgs, Message{Role: "tool", ToolName: "bash", Content: cvBig("r", 35000)})
	}
	prev := opSetSettings(opDefaultSettings())
	defer opSetSettings(prev)
	full := a.estTokens()
	s := opDefaultSettings()
	s.CtxView = "tier"
	opSetSettings(s)
	view := a.estTokens()
	if full < 55000 || view > full*6/10 || view < 25000 {
		t.Fatalf("inteiro %d, visão %d: 3 dos 6 resultados viram aviso", full, view)
	}
	a.lastPrompt, a.countedIdx = 40000, len(a.Msgs) // the provider counted the view already
	if got := a.estTokens(); got != 40000 {
		t.Fatalf("com contagem do provedor: %d", got)
	}
}

func TestEvalContextResumeKeepsValidRows(t *testing.T) {
	alt := []ecAlt{{Name: "a"}, {Name: "b"}}
	rows := []ecRow{
		{File: "p/s.jsonl@e1", Entry: "e1", Full: ecAct{Name: "bash"}, Others: alt},
		{File: "p/s.jsonl@e2", Entry: "e2", Infra: "HTTP 429"},
		{File: "p/s.jsonl@e3", Entry: "e3", Full: ecAct{Name: "read"}}, // measured without --compare
	}
	b, _ := json.Marshal(map[string]any{"rows": rows})
	f := filepath.Join(t.TempDir(), "r.json")
	if err := os.WriteFile(f, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ecLoadRows(f, true); len(got) != 1 || got["p/s.jsonl@e1"].Full.Name != "bash" {
		t.Fatalf("com --compare só vale a linha completa: %v", got)
	}
	if got := ecLoadRows(f, false); len(got) != 2 {
		t.Fatalf("sem --compare valem as duas medidas: %v", got)
	}
	if k := ecRowKey(ecPoint{File: "/x/p/s.jsonl", Entry: "e1"}); k != "p/s.jsonl@e1" {
		t.Fatalf("chave %q", k)
	}
	if len(ecLoadRows("", true)) != 0 || len(ecLoadRows(filepath.Join(t.TempDir(), "nada.json"), true)) != 0 {
		t.Fatal("sem arquivo não há linha")
	}
}
