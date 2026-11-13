package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const anSummary = "Corrigi os dois bugs em calc.py: media([]) dava ZeroDivisionError e agora devolve 0; mediana com tamanho par agora tira a média dos dois do meio. Rodei python3 -m unittest: 4 testes OK."

func TestAcMergeAnswer(t *testing.T) {
	long := strings.Repeat("resposta completa nova. ", 10)
	cases := []struct{ kept, reply, want string }{
		{"", "só a resposta", "só a resposta"},
		{anSummary, "", anSummary},
		{anSummary, "Ok.", anSummary},
		{anSummary, "nada a mudar", anSummary},
		{anSummary, anSummary, anSummary},
		{anSummary, long, strings.TrimSpace(long)},
		{anSummary, "Item 1 é falso positivo: os testes existentes já cobrem.", anSummary + "\n\n" + acGateFollowLabel + "\nItem 1 é falso positivo: os testes existentes já cobrem."},
	}
	for i, c := range cases {
		if got := acMergeAnswer(c.kept, c.reply); got != c.want {
			t.Errorf("%d: got %q, want %q", i, got, c.want)
		}
	}
}

// A turn reopened by a gate: stdout keeps the real answer; the next turn of
// the same -p run (follow-up) prints only its own answer.
func TestHeadlessUIKeepAnswer(t *testing.T) {
	var out bytes.Buffer
	h := &HeadlessUI{outw: &out, errw: io.Discard}
	h.StreamText(anSummary)
	h.KeepAnswer()
	h.StreamText("Lista de tarefas atualizada.")
	h.Finish(RunResult{Status: StopDone})
	got := out.String()
	if !strings.Contains(got, anSummary) || !strings.Contains(got, "Lista de tarefas atualizada.") || !strings.Contains(got, acGateFollowLabel) {
		t.Fatalf("stdout lost the answer:\n%s", got)
	}

	out.Reset()
	h = &HeadlessUI{outw: &out, errw: io.Discard}
	h.StreamText("primeira")
	h.KeepAnswer()
	h.StreamText("ok")
	h.TurnEnd(TurnStats{})
	h.StreamText("segunda resposta")
	h.Finish(RunResult{Status: StopDone})
	if got := strings.TrimSpace(out.String()); got != "segunda resposta" {
		t.Errorf("kept answer leaked into the next turn: %q", got)
	}
}

// The todo nudge after the final answer (QA cgit): -p used to print only the
// reply about the todo list. Text and json sinks keep the summary.
func TestHeadlessTodoNudgeKeepsSummary(t *testing.T) {
	todo := fakeCall{ID: "t1", Name: "todo", Args: jsonArgs(map[string]any{"todos": []any{
		map[string]any{"content": "instalar python3-pytest com apt", "status": "pending"},
	}})}
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			h := newHarness(t, seq(
				fakeResp{Calls: []fakeCall{todo}},
				fakeResp{Text: anSummary},
				fakeResp{Text: "Não dá para instalar sem sudo; deixei o item aberto."},
			), nil)
			var out bytes.Buffer
			var sink HeadlessSink
			if format == "json" {
				sink = NewJSONUI(&out, false)
			} else {
				sink = &HeadlessUI{outw: &out, errw: io.Discard}
			}
			h.ag.ui = sink
			stop, err := h.ag.Run(context.Background(), "corrige calc.py")
			if err != nil || stop != StopDone {
				t.Fatalf("stop=%s err=%v", stop, err)
			}
			if n := len(h.fs.requests()); n != 3 {
				t.Fatalf("requests = %d, want 3 (todo nudge reopened once)", n)
			}
			nudge := h.fs.requests()[2]
			if !nudge.contains("user", "itens abertos") || !nudge.contains("user", "complete final answer") {
				t.Errorf("todo nudge must ask for a complete final answer")
			}
			sink.Finish(RunResult{Status: StopDone})
			got := out.String()
			if format == "json" {
				var obj map[string]any
				if err := json.Unmarshal(out.Bytes(), &obj); err != nil {
					t.Fatal(err)
				}
				got, _ = obj["text"].(string)
			}
			if !strings.Contains(got, anSummary) || !strings.Contains(got, "sem sudo") {
				t.Errorf("final answer lost after the todo nudge:\n%s", got)
			}
		})
	}
}

func TestAcRecapImperative(t *testing.T) {
	tool := []Message{{Role: "user", Content: "corrige"}, {Role: "tool", Content: "ok"}}
	for _, in := range []string{
		"me fala o que vc corrigiu",
		"resume o que mudou",
		"lista o que você fez",
		"resume pra mim em 2 linhas quais bugs vc arrumou no calc",
		"explica as alterações que você fez",
		"tell me what you changed",
		"o que você fez?",
	} {
		if !acRecap(in, tool) {
			t.Errorf("recap not recognized: %q", in)
		}
		if acRecap(in, nil) {
			t.Errorf("recap without earlier tool work: %q", in)
		}
	}
	for _, in := range []string{
		"corrige o que vc fez de errado e me fala",
		"faz o que vc sugeriu",
		"cria um teste",
		"fix what you changed and tell me",
		"me fala a hora",
	} {
		if acRecap(in, tool) {
			t.Errorf("work request taken as recap: %q", in)
		}
	}
}

// An imperative recap after earlier tool work is not a fake action: no nudge.
func TestRecapImperativeNoFakeNudge(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{bashCall("b1", "echo ok")}},
		fakeResp{Text: "pronto"},
		fakeResp{Text: "Corrigi a divisão por zero em media() e testei com unittest."},
	), func(c *Config) { c.Mode = "yolo" })
	if err := h.run(t, "roda echo ok"); err != nil {
		t.Fatal(err)
	}
	stop, err := h.ag.Run(context.Background(), "me fala o que vc corrigiu")
	if err != nil || stop != StopDone {
		t.Fatalf("stop=%s err=%v", stop, err)
	}
	if n := len(h.fs.requests()); n != 3 {
		t.Errorf("requests = %d, want 3 (no fake-action nudge)", n)
	}
	if countMsg(h.ag.Msgs, fakeActionNudge) != 0 {
		t.Error("fake-action nudge fired on a recap")
	}
}

func TestHeadlessTrustWarningOnlyWhenBashGated(t *testing.T) {
	pfTestHome(t)
	root := t.TempDir()
	var w bytes.Buffer
	pfTrustGateBash(root, true, false, false, nil, &w)
	if strings.Contains(w.String(), "pasta não confiável") {
		t.Errorf("--yolo/--allow-tools: no bash warning, got %q", w.String())
	}
	w.Reset()
	pfTrustGateBash(root, true, false, true, nil, &w)
	if !strings.Contains(w.String(), "pasta não confiável") {
		t.Errorf("default -p must still warn, got %q", w.String())
	}
	cfg := &Config{Mode: "auto"}
	if !pfHeadlessBashGated(cfg, pfOpts{}) {
		t.Error("auto without rules is gated")
	}
	if pfHeadlessBashGated(cfg, pfOpts{allowTools: []string{"Bash(make:*)"}}) {
		t.Error("--allow-tools: not gated")
	}
	cfg.ModeOverride = "yolo"
	if pfHeadlessBashGated(cfg, pfOpts{}) {
		t.Error("--yolo: not gated")
	}
}

func TestDebugFatalFileOnlyWhenFatal(t *testing.T) {
	pfTestHome(t)
	pfResetDebug(t)
	path, err := pfDebugStart()
	if err != nil {
		t.Fatal(err)
	}
	fatal := strings.TrimSuffix(path, ".log") + ".fatal.txt"
	if _, err := os.Stat(fatal); err != nil {
		t.Fatalf("crash output file must exist while running: %v", err)
	}
	pfDebugEnd()
	if _, err := os.Stat(fatal); !os.IsNotExist(err) {
		t.Errorf("empty .fatal.txt left after a normal exit")
	}
	d := pfLogDir()
	dead := filepath.Join(d, "20000101-000000.000-999999999.fatal.txt")
	crashed := filepath.Join(d, "20000101-000001.000-999999998.fatal.txt")
	live := filepath.Join(d, "20000101-000002.000-"+itoa(os.Getpid())+".fatal.txt")
	os.WriteFile(dead, nil, 0o600)
	os.WriteFile(crashed, []byte("fatal error: concurrent map writes"), 0o600)
	os.WriteFile(live, nil, 0o600)
	pfPruneLogs(d)
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Error("empty .fatal.txt of a dead run must be pruned")
	}
	if _, err := os.Stat(crashed); err != nil {
		t.Error("a .fatal.txt with a trace must be kept")
	}
	if _, err := os.Stat(live); err != nil {
		t.Error("a live run's .fatal.txt must be kept")
	}
}
