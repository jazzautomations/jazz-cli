package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// sigTestHome aponta o ledger para um signals.jsonl dentro de um t.TempDir().
func sigTestHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := sigPath
	sigPath = func() string { return filepath.Join(dir, "signals.jsonl") }
	t.Cleanup(func() { sigPath = old })
	return dir
}

// TestSigAppendRead: três appends voltam na ordem, com Ts preenchido e campos
// opcionais preservados no round-trip.
func TestSigAppendRead(t *testing.T) {
	sigTestHome(t)
	in := []Signal{
		{Kind: "miss", Class: "edit", Text: "troca o tema pra escuro"},
		{Kind: "correction", Class: "cmd", Text: "roda os teste", Chosen: "go test ./...", Verified: true, Model: "local/qwen", RepoSig: "go:jazzcli"},
		{Kind: "unknown_intent", Class: "nav", Text: "abre o painel", Extra: map[string]string{"raw": "abre painel"}},
	}
	for _, s := range in {
		if err := sigAppend(s); err != nil {
			t.Fatalf("sigAppend: %v", err)
		}
	}
	got, err := sigRead(nil)
	if err != nil {
		t.Fatalf("sigRead: %v", err)
	}
	if len(got) != len(in) {
		t.Fatalf("lidos %d sinais, esperava %d", len(got), len(in))
	}
	for i, s := range got {
		if s.Kind != in[i].Kind || s.Class != in[i].Class || s.Text != in[i].Text {
			t.Errorf("sinal %d diverge: %+v", i, s)
		}
		if s.Ts <= 0 {
			t.Errorf("sinal %d sem Ts preenchido: %+v", i, s)
		}
	}
	if g := got[1]; g.Chosen != "go test ./..." || !g.Verified || g.Model != "local/qwen" || g.RepoSig != "go:jazzcli" {
		t.Errorf("campos opcionais perdidos no round-trip: %+v", g)
	}
	if got[2].Extra["raw"] != "abre painel" {
		t.Errorf("Extra perdido no round-trip: %+v", got[2])
	}
}

// TestSigConcurrentAppend: N goroutines appendando em paralelo — todas as
// linhas presentes, todas JSON válido, nenhuma entrelaçada/corrompida.
func TestSigConcurrentAppend(t *testing.T) {
	dir := sigTestHome(t)
	const N = 32
	var wg sync.WaitGroup
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- sigAppend(Signal{
				Ts:    int64(1000 + i),
				Kind:  "miss",
				Class: "edit",
				Text:  fmt.Sprintf("pedido %02d", i),
			})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("sigAppend concorrente: %v", err)
		}
	}
	b, err := os.ReadFile(filepath.Join(dir, "signals.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != N {
		t.Fatalf("%d linhas no arquivo, esperava %d", len(lines), N)
	}
	seen := map[string]bool{}
	for i, ln := range lines {
		var s Signal
		if err := json.Unmarshal([]byte(ln), &s); err != nil {
			t.Fatalf("linha %d corrompida (%v): %q", i, err, ln)
		}
		seen[s.Text] = true
	}
	for i := 0; i < N; i++ {
		if txt := fmt.Sprintf("pedido %02d", i); !seen[txt] {
			t.Errorf("sinal %q ausente", txt)
		}
	}
	got, err := sigRead(nil)
	if err != nil || len(got) != N {
		t.Fatalf("sigRead: err=%v, %d sinais", err, len(got))
	}
}

// TestSigReadSkipsInvalid: linhas lixo ou vazias no meio do arquivo são
// puladas, sem abortar a leitura dos sinais bons.
func TestSigReadSkipsInvalid(t *testing.T) {
	dir := sigTestHome(t)
	g1, _ := json.Marshal(Signal{Ts: 1, Kind: "miss", Class: "a", Text: "um"})
	g2, _ := json.Marshal(Signal{Ts: 2, Kind: "correction", Class: "b", Text: "dois"})
	g3, _ := json.Marshal(Signal{Ts: 3, Kind: "miss", Class: "a", Text: "três"})
	content := string(g1) + "\n" +
		"isto nao e json\n" +
		"[1,2,3]\n" +
		string(g2) + "\n" +
		"\n" + // linha vazia também é pulada
		string(g3) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "signals.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := sigRead(nil)
	if err != nil {
		t.Fatalf("sigRead: %v", err)
	}
	if len(got) != 3 || got[0].Text != "um" || got[1].Text != "dois" || got[2].Text != "três" {
		t.Fatalf("sinais lidos: %+v", got)
	}
}

// TestSigByClass filtra pela classe da tarefa.
func TestSigByClass(t *testing.T) {
	sigTestHome(t)
	for _, s := range []Signal{
		{Kind: "miss", Class: "edit", Text: "a"},
		{Kind: "miss", Class: "cmd", Text: "b"},
		{Kind: "correction", Class: "edit", Text: "c"},
	} {
		if err := sigAppend(s); err != nil {
			t.Fatal(err)
		}
	}
	got, err := sigByClass("edit")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Text != "a" || got[1].Text != "c" {
		t.Fatalf("sigByClass(edit): %+v", got)
	}
	if got, err = sigByClass("nada"); err != nil || len(got) != 0 {
		t.Fatalf("classe inexistente: %v %+v", err, got)
	}
}

// TestSigReadSince filtra por timestamp (Ts >= corte).
func TestSigReadSince(t *testing.T) {
	sigTestHome(t)
	for _, s := range []Signal{
		{Kind: "miss", Class: "a", Text: "velho", Ts: 100},
		{Kind: "miss", Class: "a", Text: "meio", Ts: 150},
		{Kind: "miss", Class: "a", Text: "novo", Ts: 200},
	} {
		if err := sigAppend(s); err != nil {
			t.Fatal(err)
		}
	}
	got, err := sigReadSince(150)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Text != "meio" || got[1].Text != "novo" {
		t.Fatalf("sigReadSince(150): %+v", got)
	}
}

// TestSigCount agrupa por Kind; ledger inexistente conta zero, sem erro.
func TestSigCount(t *testing.T) {
	sigTestHome(t)
	if c, err := sigCount(); err != nil || len(c) != 0 {
		t.Fatalf("ledger ausente: %v %v", c, err)
	}
	for _, k := range []string{"miss", "miss", "correction", "cmd_fixed", "miss"} {
		if err := sigAppend(Signal{Kind: k, Class: "x", Text: "p"}); err != nil {
			t.Fatal(err)
		}
	}
	c, err := sigCount()
	if err != nil {
		t.Fatal(err)
	}
	if c["miss"] != 3 || c["correction"] != 1 || c["cmd_fixed"] != 1 || len(c) != 3 {
		t.Fatalf("contagem por kind: %v", c)
	}
}

// The cap: past sigMaxBytes the ledger rotates to signals.jsonl.1 inside the
// lock — the live file starts over small and no unbounded decode ever
// happens (etapa B).
func TestSigRotation(t *testing.T) {
	p := filepath.Join(sigTestHome(t), "signals.jsonl")
	big := Signal{Kind: "miss", Class: "x", Text: "p", Extra: map[string]string{"pad": strings.Repeat("x", sigMaxBytes)}}
	if err := sigAppend(big); err != nil {
		t.Fatal(err)
	}
	if err := sigAppend(Signal{Kind: "miss", Class: "x", Text: "depois do giro"}); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil || st.Size() > 1<<20 {
		t.Fatalf("ledger girou e ficou pequeno: %v %v", st, err)
	}
	if _, err := os.Stat(p + ".1"); err != nil {
		t.Errorf("o backup .1 existe: %v", err)
	}
	sigs, err := sigRead(nil)
	if err != nil || len(sigs) != 1 || sigs[0].Text != "depois do giro" {
		t.Fatalf("só o sinal novo está no ledger vivo: %v %v", sigs, err)
	}
	// um terceiro giro descarta o .1 antigo sem erro
	if err := sigAppend(big); err != nil {
		t.Fatal(err)
	}
	if err := sigAppend(Signal{Kind: "miss", Class: "x", Text: "segundo giro"}); err != nil {
		t.Fatal(err)
	}
	if sigs, _ := sigRead(nil); len(sigs) != 1 || sigs[0].Text != "segundo giro" {
		t.Fatalf("segundo giro também sobrevive: %v", sigs)
	}
}
