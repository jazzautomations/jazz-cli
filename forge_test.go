package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// fgTestHome isolates the forge: data dir, ledger and overlay in temp dirs,
// no audit records. Seed pins the keyed split so tests are deterministic.
func fgTestHome(t *testing.T) fgOpts {
	t.Helper()
	home := t.TempDir()
	t.Setenv("JAZZ_HOME", home)
	t.Setenv("JAZZ_AUDIT", "0")
	p := filepath.Join(home, "signals.jsonl")
	old := sigPath
	sigPath = func() string { return p }
	t.Cleanup(func() { sigPath = old })
	return fgOpts{Overlay: nuOverlayPath(), Seed: 0x6a617a7a}
}

var fgSessN atomic.Int64

// fgCorrect records ELIGIBLE corrections (etapa B provenance): one call =
// one session of menu picks at a terminal, position >= 2, verified success.
// fgCorrectSess pins the session id; fgBad writes a signal as given.
func fgCorrect(t *testing.T, intent string, texts ...string) {
	t.Helper()
	fgCorrectSess(t, fmt.Sprintf("s%d", fgSessN.Add(1)), intent, texts...)
}

func fgCorrectSess(t *testing.T, sess, intent string, texts ...string) {
	t.Helper()
	for _, s := range texts {
		fgBad(t, Signal{Kind: "correction", Class: "intent", Text: s, Chosen: intent, Verified: true,
			Extra: map[string]string{"how": "menu", "src": "tty", "pos": "3", "sess": sess, "status": "succeeded"}})
	}
}

func fgBad(t *testing.T, s Signal) {
	t.Helper()
	if err := sigAppend(s); err != nil {
		t.Fatal(err)
	}
}

func fgDropReasons(rep *fgReport) string {
	var all []string
	for _, d := range rep.Dropped {
		all = append(all, d.Reason)
	}
	return strings.Join(all, " | ")
}

// A real miss: "lista as funções mais compridas" has no rule and the
// shipped classifier is not sure enough to act (0.66 < 0.7). The owner's
// corrections teach it — but only with etapa-B provenance: menu picks at a
// terminal, in >= 2 sessions, each verified. The cluster's medoid is gated
// alone (no bundle), the paraphrases are its directed test.
func TestForgeAcceptsCorrectionThatFixesMiss(t *testing.T) {
	o := fgTestHome(t)
	target := "lista as funções mais compridas"
	base := nuFreshModel(t)
	w := nuWorldFromDef(base.data.World)
	if in := base.Parse(target, w); fgActs(in) && in.Name == "large_funcs" {
		t.Fatalf("pré-condição: a gramática embarcada não deve agir em %q (%s %.2f)", target, in.Name, in.Confidence)
	}
	fgCorrect(t, "large_funcs", target, "quais as funções mais compridas") // sessão A
	fgCorrect(t, "large_funcs", "me diz as funções mais compridas")        // sessão B — ≥2
	fgCorrect(t, "large_funcs", "cadê as funções mais compridas")          // sessão C: regra decide, cai
	o.DryRun = true
	rep, err := fgRun(o)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Accept || rep.Wrote {
		t.Fatalf("dry-run deve aceitar e não gravar: %+v", rep)
	}
	if _, err := os.Stat(o.Overlay); !os.IsNotExist(err) {
		t.Fatal("--dry-run gravou o overlay")
	}
	dropped := fgDropReasons(rep)
	if !strings.Contains(dropped, "não muda a leitura") {
		t.Errorf("leitura decidida por regra deve cair: %s", dropped)
	}
	if len(rep.Added) != 1 || rep.Added[0].Intent != "large_funcs" || rep.Added[0].Sessions < 2 {
		t.Fatalf("um candidato só (o medoide), ≥2 sessões: %+v", rep.Added)
	}

	o.DryRun = false
	rep, err = fgRun(o)
	if err != nil || !rep.Accept || !rep.Wrote {
		t.Fatalf("forja real: %v %+v", err, rep)
	}
	ov, err := nuReadOverlay(o.Overlay)
	if err != nil || ov == nil || len(ov.Provenance) != 1 || ov.BaseSHA256 != nuEmbeddedSHA() {
		t.Fatalf("overlay gravado: %v %+v", err, ov)
	}
	m := nuFreshModel(t)
	nuApplyOverlay(m, ov)
	if in := m.Parse(target, w); !(fgActs(in) && in.Name == "large_funcs") {
		t.Errorf("com o overlay o cérebro deve agir em %q: %s %.2f", target, in.Name, in.Confidence)
	}

	// convergente: cada run promove o próximo medoide do cluster (1 por
	// candidato); esgotado, rejeita sem tocar o overlay.
	for i := 0; i < 4; i++ {
		before, _ := os.ReadFile(o.Overlay)
		rep, err = fgRun(o)
		if err != nil {
			t.Fatal(err)
		}
		if !rep.Accept {
			after, _ := os.ReadFile(o.Overlay)
			if string(before) != string(after) {
				t.Fatal("rodada rejeitada mexeu no overlay")
			}
			return
		}
	}
	t.Error("o cluster devia esgotar em poucas rodadas")
}

// BLOCKER 1 — the proven attack (review): near-duplicate corrections across
// sessions moved status requests onto mutating intents at act confidence.
// Every one is dropped before the gate: the forge never learns mutation.
func TestForgeRejectsMutatingIntents(t *testing.T) {
	o := fgTestHome(t)
	fgCorrect(t, "git_push", "push status")
	fgCorrect(t, "git_push", "push status agora")
	fgCorrect(t, "git_commit", "commit status")
	fgCorrect(t, "git_commit", "commit status aqui")
	fgCorrect(t, "service_restart", "logs do nginx e reinicia")
	fgCorrect(t, "service_restart", "logs do nginx e reinicia já")
	fgCorrect(t, "delete_file", "xqz lista os arquivos")
	fgCorrect(t, "delete_file", "xqz lista os arquivos pf")
	rep, err := fgRun(o)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Accept || rep.Wrote {
		t.Fatalf("nada mutante pode entrar: %+v", rep)
	}
	reasons := fgDropReasons(rep)
	for _, d := range rep.Dropped {
		if !strings.Contains(d.Reason, "mutante") {
			t.Errorf("toda correção mutante cai por mutante, veio %q (%s)", d.Reason, reasons)
		}
	}
	if _, err := os.Stat(o.Overlay); !os.IsNotExist(err) {
		t.Error("recusa gravou o overlay")
	}
	// e nem por arquivo editado à mão: o loader também bloqueia mutantes.
	m := nuFreshModel(t)
	ov := &nuOverlay{Version: 1, Examples: map[string][]string{"git_push": {"push status"}}}
	if n, sk := nuApplyOverlay(m, ov); n != 0 || sk["mutating"] != 1 {
		t.Errorf("overlay manual de mutante tem que ser ignorado: n=%d %v", n, sk)
	}
}

// BLOCKER — provenance (review finding 4): Enter/default confirms,
// --ask-stdin answers, picks of the top item, failures and session-less
// signals are not evidence — the grammar must not train on its own guesses.
func TestForgeRejectsBadProvenance(t *testing.T) {
	o := fgTestHome(t)
	good := map[string]string{"how": "menu", "src": "tty", "pos": "3", "sess": "x", "status": "succeeded"}
	mk := func(mut func(map[string]string)) Signal {
		ex := map[string]string{}
		for k, v := range good {
			ex[k] = v
		}
		mut(ex)
		return Signal{Kind: "correction", Class: "intent", Text: "lista as funções mais compridas", Chosen: "large_funcs", Verified: true, Extra: ex}
	}
	fgBad(t, mk(func(e map[string]string) { e["how"] = "confirm" })) // Enter do confirm
	fgBad(t, mk(func(e map[string]string) { delete(e, "src") }))     // --ask-stdin (sem TTY)
	fgBad(t, mk(func(e map[string]string) { e["pos"] = "1" }))       // pick no item 1
	fgBad(t, mk(func(e map[string]string) { delete(e, "sess") }))    // sem sessão
	s := mk(func(e map[string]string) {})
	s.Verified = false // a ação falhou
	fgBad(t, s)
	rep, err := fgRun(o)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Accept || rep.Wrote {
		t.Fatalf("nenhuma dessas é evidência: %+v", rep)
	}
	if len(rep.Dropped) != 5 {
		t.Fatalf("5 drops de proveniência: %+v", rep.Dropped)
	}
}

// menu_skip: the owner saw the reading at a menu and rejected it — a later
// correction claiming it is a contradiction, and the cluster leaves the
// queue (spec item 6).
func TestForgeMenuSkipContradicts(t *testing.T) {
	o := fgTestHome(t)
	fgBad(t, Signal{Kind: "menu_skip", Class: "intent", Text: "lista as funções mais compridas", Chosen: "large_funcs",
		Extra: map[string]string{"src": "tty", "sess": "s0"}})
	fgCorrect(t, "large_funcs", "lista as funções mais compridas")
	fgCorrect(t, "large_funcs", "lista as funções mais compridas")
	rep, err := fgRun(o)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Accept {
		t.Fatalf("o dono já rejeitou essa leitura no menu: %+v", rep)
	}
	if !strings.Contains(fgDropReasons(rep), "rejeitou essa leitura") {
		t.Errorf("queda por contradição de menu: %s", fgDropReasons(rep))
	}
}

// The bundle attack (review finding 2): a poisoned candidate riding with
// good ones. With per-candidate gating each is scored alone — the poison
// dies on its own false-success, the good corrections still pass.
func TestForgeBundlePoisonDiesAlone(t *testing.T) {
	o := fgTestHome(t)
	// poção: o texto é negativo explícito de open_file
	fgCorrect(t, "open_file", "mostra os logs do nginx agora")
	fgCorrect(t, "open_file", "mostra os logs do nginx aqui")
	// boas: o miss real de sempre, 2 sessões
	fgCorrect(t, "large_funcs", "lista as funções mais compridas")
	fgCorrect(t, "large_funcs", "quais as funções mais compridas")
	rep, err := fgRun(o)
	if err != nil {
		t.Fatal(err)
	}
	var openFile, large int
	for _, a := range rep.Added {
		if a.Intent == "open_file" {
			openFile++
		}
		if a.Intent == "large_funcs" {
			large++
		}
	}
	if openFile != 0 {
		t.Fatalf("a poção morreu sozinha, não de carona: %+v", rep.Added)
	}
	if large != 1 {
		t.Fatalf("a boa passa por conta própria: %+v", rep.Added)
	}
}

// A poisoned label: the grammar says explicitly "mostra os logs do nginx"
// is NOT open_file. The exact negative drops up front; the variants would
// make the negative itself land on open_file — false success, the gate
// blocks, nothing is written.
func TestForgeRejectsPoisonedCorrection(t *testing.T) {
	o := fgTestHome(t)
	fgCorrect(t, "open_file", "mostra os logs do nginx", "mostra os logs do nginx agora")
	fgCorrect(t, "open_file", "mostra os logs do nginx aqui", "mostra os logs do nginx inteiros")
	rep, err := fgRun(o)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Accept || rep.Wrote {
		t.Fatalf("rótulo envenenado não pode entrar: %+v", rep)
	}
	reasons := fgDropReasons(rep)
	if !strings.Contains(reasons, "negativo") {
		t.Errorf("o negativo exato deve cair antes do portão: %s", reasons)
	}
	// as variantes morrem no portão (falso sucesso novo) ou no teste
	// dirigido (a paráfrase não classifica na candidata) — nunca entram.
	if !strings.Contains(reasons, "false_success") && !strings.Contains(reasons, "paráfrase") && !strings.Contains(reasons, "rejeitado") {
		t.Errorf("a recusa do cluster deve ser pelo portão ou pelo teste dirigido: %s", reasons)
	}
	if _, err := os.Stat(o.Overlay); !os.IsNotExist(err) {
		t.Error("recusa gravou o overlay")
	}
}

// Anti-trigger (spec item 3): a word the intent does not own may not carry
// the decision. The proven attack phrase dies even earlier than the gate —
// "xqz lista os arquivos" is decided by the list_files rule no matter what
// the classifier learns, and "xqz arquivo" never reaches act confidence
// under the full model either. What survives neither gate (unit test below)
// can promote.
func TestForgeRejectsTriggerWord(t *testing.T) {
	o := fgTestHome(t)
	fgCorrect(t, "open_file", "xqz lista os arquivos")
	fgCorrect(t, "open_file", "xqz lista os arquivos")
	fgCorrect(t, "open_file", "xqz arquivo")
	fgCorrect(t, "open_file", "xqz arquivo")
	rep, err := fgRun(o)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Accept {
		t.Fatalf("palavra-gatilho não pode entrar: %+v", rep)
	}
	if !strings.Contains(fgDropReasons(rep), "não muda a leitura") {
		t.Errorf("as frases-gatilho caem na aprendizagem: %s", fgDropReasons(rep))
	}
}

// The anti-trigger check itself: all-novel words leave nothing after the
// mask; a medoid that reads right only with the new word is a trigger.
func TestForgeAntiTriggerMask(t *testing.T) {
	lx, _ := nuFS.ReadFile("nlu/lexicon.json")
	ds, _ := nuFS.ReadFile("nlu/intents.json")
	m := fgModel(lx, ds, nil, nil, nil)
	w := nuWorldFromDef(m.data.World)
	support := fgSupportWords(m, fgLexicon(lx), "open_file", nil)
	if d, why := fgAntiTrigger("xqz blub zorp", "open_file", support, m, m, w, nil); !strings.Contains(why, "palavra nova") || len(d) != 3 {
		t.Errorf("só-palavra-nova: d=%v why=%q", d, why)
	}
	if _, why := fgAntiTrigger("xqz arquivo", "open_file", support, m, m, w, nil); !strings.Contains(why, "palavra nova") {
		t.Errorf("máscara sem a intenção: %q", why)
	}
	// benigno: a palavra nova sobra e a leitura continua sem ela.
	if _, why := fgAntiTrigger("xqz mostra arquivo", "open_file", support, m, m, w, nil); why != "" {
		t.Errorf("medoide benigno devia passar: %q", why)
	}
}

// Labels that are not intents, oversized texts, contradictory labels and a
// lone session never reach the gate.
func TestForgeDropsBadLabelsAndNeedsData(t *testing.T) {
	o := fgTestHome(t)
	fgCorrect(t, "go test ./...", "roda os testes do pacote") // a command, not an intent
	fgCorrect(t, "large_funcs", strings.Repeat("funções compridas ", 20))
	fgCorrect(t, "large_funcs", "quais funções passam do limite")
	fgCorrect(t, "dup_code", "quais funções passam do limite") // same text, other label
	fgCorrectSess(t, "unica", "large_funcs", "lista as funções mais compridas")
	rep, err := fgRun(o)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Accept {
		t.Fatalf("nada elegível: %+v", rep)
	}
	all := fgDropReasons(rep)
	for _, w := range []string{"não é uma intenção", "200 caracteres", "ambíguo", "sessão"} {
		if !strings.Contains(all, w) {
			t.Errorf("descarte sem %q: %s", w, all)
		}
	}
	if rep.Signals != 5 {
		t.Errorf("signals=%d, esperado 5", rep.Signals)
	}
}

// The mutating list lives in code and must cover every intent with a
// high/critical-risk method — a new mutating method without it fails here.
func TestForgeMutatingListCoversRisk(t *testing.T) {
	lib, _ := mhLibrary()
	for name, ms := range lib {
		for _, m := range ms {
			if r := cbParseRisk(m.Risk); r >= cbHigh && !fgMutating[name] {
				t.Errorf("%s tem método de risco %s e falta em fgMutating", name, m.Risk)
			}
		}
	}
}

// The command: usage, exit codes and --json.
func TestForgeCommand(t *testing.T) {
	o := fgTestHome(t)
	if cmdForge([]string{"--nope"}, &strings.Builder{}) != 2 {
		t.Error("flag desconhecida = uso (2)")
	}
	var b strings.Builder
	if code := cmdForge([]string{"--json"}, &b); code != 1 {
		t.Errorf("ledger vazio = nada a forjar (1), veio %d", code)
	}
	var rep fgReport
	if err := json.Unmarshal([]byte(b.String()), &rep); err != nil || rep.Accept || rep.Overlay != o.Overlay {
		t.Errorf("--json: %v %+v\n%s", err, rep, b.String())
	}
	fgCorrect(t, "large_funcs", "lista as funções mais compridas")
	fgCorrect(t, "large_funcs", "quais as funções mais compridas")
	b.Reset()
	if code := cmdForge([]string{"--dry-run"}, &b); code != 0 {
		t.Errorf("dry-run aceito = 0, veio %d\n%s", code, b.String())
	}
	for _, w := range []string{"forja: 2 correção(ões)", "portão:", "dry-run: nada gravado"} {
		if !strings.Contains(b.String(), w) {
			t.Errorf("relatório sem %q:\n%s", w, b.String())
		}
	}
	if _, err := os.Stat(o.Overlay); !os.IsNotExist(err) {
		t.Error("--dry-run gravou")
	}
}

// forge --list/--add/--forget/--revert: layer 1 (aliases) and the undo of
// layer 2.
func TestForgeListForgetRevert(t *testing.T) {
	o := fgTestHome(t)
	var b strings.Builder
	if code := cmdForge([]string{"--add", "fuça os logs", "service_logs"}, &b); code != 0 {
		t.Fatalf("--add: %d", code)
	}
	b.Reset()
	if code := cmdForge([]string{"--list"}, &b); code != 0 || !strings.Contains(b.String(), "fuça os logs") {
		t.Fatalf("--list mostra o apelido: %d\n%s", code, b.String())
	}
	// forge real, depois --revert desfaz o lote
	fgCorrect(t, "large_funcs", "lista as funções mais compridas")
	fgCorrect(t, "large_funcs", "quais as funções mais compridas")
	rep, err := fgRun(o)
	if err != nil || !rep.Accept || !rep.Wrote {
		t.Fatalf("forja: %v %+v", err, rep)
	}
	b.Reset()
	if code := fgRevert(fgOpts{Overlay: o.Overlay}, &b); code != 0 {
		t.Fatalf("--revert: %d\n%s", code, b.String())
	}
	ov, _ := nuReadOverlay(o.Overlay)
	if ov == nil || len(ov.Provenance) != 0 {
		t.Errorf("revert tirou o lote: %+v", ov)
	}
	b.Reset()
	if code := cmdForge([]string{"--forget", "fuça os logs"}, &b); code != 0 || !strings.Contains(b.String(), "esquecido") {
		t.Fatalf("--forget: %d\n%s", code, b.String())
	}
}

// Menu rotation (spec item 6): positions 2..k rotate by session seed — the
// forge reads position as evidence, so no intent may hide in a fixed slot.
// The hypothesis keeps item 1 (the safest stays the safest).
func TestForgeMenuRotation(t *testing.T) {
	in := Intent{Name: "git_status", Confidence: 0.4, Alt: []string{"git_log", "git_branch"}}
	mi := olMenuIn{Text: "os commits aí"}
	base := brMissActions(in, mi)
	var brain []string
	for _, a := range base {
		if a.Kind == "brain" {
			brain = append(brain, a.Intent)
		}
	}
	if len(brain) < 3 || brain[0] != "git_status" {
		t.Fatalf("a hipótese fica no topo: %v", brain)
	}
	rotated, seen := 0, map[string]bool{}
	for i := 0; i < 24; i++ {
		mi.RotSeed = fmt.Sprintf("sess-%d", i)
		var tail []string
		for _, a := range brMissActions(in, mi) {
			if a.Kind == "brain" {
				tail = append(tail, a.Intent)
			}
		}
		if tail[0] != "git_status" {
			t.Fatalf("seed %d tirou a hipótese do topo: %v", i, tail)
		}
		key := strings.Join(tail[1:], ",")
		if !seen[key] {
			seen[key] = true
		}
		if tail[1] != "git_log" {
			rotated++
		}
	}
	if rotated == 0 {
		t.Error("nenhuma sessão girou as posições 2..k — a rotação está quebrada")
	}
	if len(seen) < 2 {
		t.Errorf("só uma ordem em 24 sementes: %v", seen)
	}
}

// The [l] option: a named, non-typo reading offers "Sim, e lembrar que é
// isso" as a 4th way out; a typo-only reading does not (remembering a typo
// is cementing noise).
func TestForgeConfirmRememberOption(t *testing.T) {
	env := &brEnv{}
	opts := brConfirmOptions(env, Intent{Name: "git_status", Confidence: 0.9}, cbLow)
	if len(opts) != 4 || !strings.Contains(opts[3], "lembrar") || !strings.Contains(opts[3], brAskKeySep+"l"+brAskKeySep) {
		t.Fatalf("leitura nomeada ganha a 4ª opção [l]: %v", opts)
	}
	opts = brConfirmOptions(env, Intent{Name: "git_status", Confidence: 0.9, TypoOnly: true}, cbLow)
	if len(opts) != 3 {
		t.Fatalf("typo-only não lembra: %v", opts)
	}
	opts = brConfirmOptions(env, Intent{}, cbLow)
	if len(opts) != 3 {
		t.Fatalf("leitura vazia não lembra: %v", opts)
	}
}

// Secrets never reach the ledger: the request text and every
// request-derived extra are redacted; machine tokens (pos, how, sess) stay.
// (The fake key is built at runtime: no secret-shaped literal lands in the
// repo for the scanner to trip on.)
func TestSignalRedaction(t *testing.T) {
	fk := "API_" + "KEY=" + "abcdef" + "gh1234"
	in := Intent{Name: "open_file", Confidence: 0.9, Evidence: []string{"slot: " + fk}}
	s := sigOf("miss", "roda isso com "+fk, in, map[string]string{
		"why":  fk,
		"pos":  "3",
		"sess": "s1",
	})
	blob, _ := json.Marshal(s)
	if strings.Contains(string(blob), "abcdef") {
		t.Errorf("segredo vazou no sinal: %s", blob)
	}
	if s.Extra["pos"] != "3" || s.Extra["sess"] != "s1" {
		t.Errorf("tokens de máquina ficam: %v", s.Extra)
	}
}
