//go:build !windows

package main

// nlu_vacuous_test.go — invariante do Jev-audit FALSE_SUCCESS: num pedido
// que espera resultado, um produtor que acha ZERO itens ("0 match(es)",
// "0 occurrence(s)") nunca pode terminar em `succeeded` — sem evidência
// de achado não há verde. O verificador `nonempty` (methods.go) é a
// cerca: conjunto vazio, conjunto parcial (truncated) ou padrão com
// metacaractere de shell solto ("& calcule", nb-0310) viram `unverified`.
//
// Os intents aqui são construídos na mão: a leitura NLU->slots é território
// de nlu.go; o que este teste prova é que, DADO um intent de busca, a saída
// vacuosa do produtor nunca vira sucesso.

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// ---------- nível de check: brVerify(nonempty) ----------

func TestVacuousNonemptyCheck(t *testing.T) {
	newCtx := func() *brCtx {
		return &brCtx{env: &brEnv{Root: t.TempDir()}, task: &TaskState{Slots: map[string]string{}}, vars: map[string]any{}}
	}
	chk := func() mhMethod { return mhMethod{Verify: []mhCheck{{Check: "nonempty", Arg: "$res"}}} }

	// file_find: lista files vazia é o "0 match(es)" do corpus — nunca verde
	cx := newCtx()
	cx.vars["res"] = map[string]any{"ok": true, "data": map[string]any{"files": []any{}, "count": 0.0, "truncated": false}}
	ok, unavailable, why := brVerify(cx, chk())
	if ok || !unavailable {
		t.Fatalf("lista vazia: ok=%v unavailable=%v (queria unverified)", ok, unavailable)
	}
	if !strings.Contains(why, "conjunto vazio") {
		t.Errorf("razão sem 'conjunto vazio': %q", why)
	}

	// o mesmo produtor com itens passa e deixa prova
	cx = newCtx()
	cx.vars["res"] = map[string]any{"ok": true, "data": map[string]any{"files": []any{
		map[string]any{"path": "a.go"}}, "count": 1.0}}
	if ok, _, why := brVerify(cx, chk()); !ok {
		t.Fatalf("1 item devia verificar: %q", why)
	}
	if !strings.Contains(cx.out.String(), "conferi: produtor emitiu 1 item(ns)") {
		t.Errorf("proof:\n%s", cx.out.String())
	}

	// text_grep: items é a lista de match(es)
	cx = newCtx()
	cx.vars["res"] = map[string]any{"ok": true, "data": map[string]any{"items": []any{}, "matches": 0.0}}
	if ok, _, _ := brVerify(cx, chk()); ok {
		t.Fatal("0 literal match(es) verificou")
	}
	cx = newCtx()
	cx.vars["res"] = map[string]any{"ok": true, "data": map[string]any{"items": []any{
		map[string]any{"line": 1.0, "match": "x"}}, "matches": 1.0}}
	if ok, _, why := brVerify(cx, chk()); !ok {
		t.Fatalf("grep com match: %q", why)
	}

	// text_count: sem lista — count/lines_matched numérico é a evidência
	cx = newCtx()
	cx.vars["res"] = map[string]any{"ok": true, "data": map[string]any{"count": 0.0, "lines_matched": 0.0}}
	if ok, un, why := brVerify(cx, chk()); ok || !un || !strings.Contains(why, "conjunto vazio") {
		t.Fatalf("0 occurrence(s): ok=%v un=%v why=%q", ok, un, why)
	}
	cx = newCtx()
	cx.vars["res"] = map[string]any{"ok": true, "data": map[string]any{"count": 3.0, "lines_matched": 2.0}}
	if ok, _, why := brVerify(cx, chk()); !ok {
		t.Fatalf("count>0 devia verificar: %q", why)
	}

	// truncated: conjunto parcial não é a resposta pedida
	cx = newCtx()
	cx.vars["res"] = map[string]any{"ok": true, "data": map[string]any{
		"files": []any{map[string]any{"path": "a"}}, "count": 1.0, "truncated": true}}
	if ok, un, why := brVerify(cx, chk()); ok || !un || !strings.Contains(why, "trunc") {
		t.Fatalf("truncated: ok=%v un=%v why=%q", ok, un, why)
	}

	// arg=nil e arg de formato errado falham sem pânico
	cx = newCtx()
	if ok, _, _ := brVerify(cx, chk()); ok {
		t.Fatal("$res ausente verificou")
	}
	cx.vars["res"] = "lixo"
	if ok, _, _ := brVerify(cx, chk()); ok {
		t.Fatal("$res lixo verificou")
	}
}

// ---------- guarda de padrão (nb-0310: "& calculate total caractor") ----------

func TestVacuousNonemptyPatternGuard(t *testing.T) {
	newCtx := func(pat string) *brCtx {
		return &brCtx{env: &brEnv{Root: t.TempDir()},
			task: &TaskState{Slots: map[string]string{"pattern": pat}}, vars: map[string]any{}}
	}
	chk := func() mhMethod {
		return mhMethod{Verify: []mhCheck{{Check: "nonempty", Arg: "$res", Arg2: "$slot.pattern"}}}
	}
	full := map[string]any{"ok": true, "data": map[string]any{"items": []any{
		map[string]any{"line": 1.0, "match": "x && y"}}, "matches": 1.0}}

	// o padrão de leitura quebrada falha MESMO com itens produzidos — a
	// resposta não pode ser afirmada contra o que foi pedido
	for _, pat := range []string{"& calculate total caractor", "erro | grep x", "a && b", "a || b", "|", "&"} {
		cx := newCtx(pat)
		cx.vars["res"] = full
		ok, un, why := brVerify(cx, chk())
		if ok || !un || !strings.Contains(why, "metacaractere") {
			t.Errorf("padrão %q: ok=%v un=%v why=%q — devia flagar", pat, ok, un, why)
		}
	}

	// literal honesto passa: "a|b" sem espaço é padrão, não operador
	cx := newCtx("a|b")
	cx.vars["res"] = full
	if ok, _, why := brVerify(cx, chk()); !ok {
		t.Fatalf("literal 'a|b' com match: %q", why)
	}

	// sem arg2 o check não toca no padrão
	cx = newCtx("& garbage")
	cx.vars["res"] = full
	m := mhMethod{Verify: []mhCheck{{Check: "nonempty", Arg: "$res"}}}
	if ok, _, why := brVerify(cx, m); !ok {
		t.Fatalf("sem arg2, padrão não participa: %q", why)
	}
}

// tabela de verdade do detector de operador solto
func TestVacuousShellOpTable(t *testing.T) {
	for pat, want := range map[string]bool{
		"& calculate total caractor": true,
		"erro | grep x":              true,
		"x &":                        true,
		"& x":                        true,
		"a&&b":                       true,
		"a || b":                     true,
		"a|b":                        false,
		"x&y":                        false,
		"error":                      false,
		"C&A":                        false,
	} {
		if got := brHasShellOp(pat); got != want {
			t.Errorf("brHasShellOp(%q)=%v, queria %v", pat, got, want)
		}
	}
}

// ---------- nível de run: intent construído → método real → status ----------

func TestVacuousFileFindEmptyNotSucceeded(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("sem python3")
	}
	root := brFixture(t)
	env, _ := brTestEnv(t, root, nil, -1)

	// "0 match(es)" do corpus (tb-045/en-012/nb-0368): o find rodou, achou
	// nada — e mesmo assim o run fechava verde. Agora é unverified.
	in := Intent{Name: "file_find", Confidence: 0.9,
		Slots: map[string]string{"name": "zzz-no-such-file-xyz"}}
	r := brRunIntent(context.Background(), env, "find files named zzz-no-such-file-xyz", in, brResult{})
	if r.Status == tkSucceeded {
		t.Fatalf("0 match(es) virou sucesso:\n%s", r.Text)
	}
	if r.Status != tkUnverified {
		t.Fatalf("conjunto vazio devia ser unverified, veio %s:\n%s", r.Status, r.Text)
	}
	if !strings.Contains(r.Text, "conjunto vazio") {
		t.Errorf("resposta sem a razão honesta:\n%s", r.Text)
	}
	if r.Method != "file_find.script" {
		t.Errorf("método=%q, queria file_find.script", r.Method)
	}

	// controle positivo: o mesmo método com achado continua verde
	env, _ = brTestEnv(t, root, nil, -1)
	in = Intent{Name: "file_find", Confidence: 0.9,
		Slots: map[string]string{"name": "parser.go"}}
	r = brRunIntent(context.Background(), env, "find files named parser.go", in, brResult{})
	if r.Status != tkSucceeded {
		t.Fatalf("find com achado: %s\n%s", r.Status, r.Text)
	}
	if !strings.Contains(r.Text, "produtor emitiu") {
		t.Errorf("sem prova de conjunto:\n%s", r.Text)
	}
}

func TestVacuousTextGrepEmptyNotSucceeded(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("sem python3")
	}
	root := brFixture(t)
	env, _ := brTestEnv(t, root, nil, -1)

	in := Intent{Name: "text_grep", Confidence: 0.9,
		Slots: map[string]string{"file": "parser.go", "pattern": "zzz-not-in-file"}}
	r := brRunIntent(context.Background(), env, "grep zzz-not-in-file in parser.go", in, brResult{})
	if r.Status == tkSucceeded {
		t.Fatalf("0 literal match(es) virou sucesso:\n%s", r.Text)
	}
	if r.Status != tkUnverified || !strings.Contains(r.Text, "conjunto vazio") {
		t.Fatalf("0 match(es): %s\n%s", r.Status, r.Text)
	}

	// controle: grep que acha continua verde
	env, _ = brTestEnv(t, root, nil, -1)
	in = Intent{Name: "text_grep", Confidence: 0.9,
		Slots: map[string]string{"file": "parser.go", "pattern": "ParseArgs"}}
	r = brRunIntent(context.Background(), env, "grep ParseArgs in parser.go", in, brResult{})
	if r.Status != tkSucceeded || !strings.Contains(r.Text, "literal match") {
		t.Fatalf("grep com achado: %s\n%s", r.Status, r.Text)
	}
}

// nb-0310: o parser emitiu "& calculate total caractor" como padrão — mesmo
// se a leitura quebrada chegar ao método, a verificação recusa o verde.
func TestVacuousGrepGarbagePatternNotSucceeded(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("sem python3")
	}
	root := brFixture(t)
	env, _ := brTestEnv(t, root, nil, -1)
	in := Intent{Name: "text_grep", Confidence: 0.9,
		Slots: map[string]string{"file": "parser.go", "pattern": "& calculate total caractor"}}
	r := brRunIntent(context.Background(), env, "search for '& calculate total caractor' in parser.go", in, brResult{})
	if r.Status == tkSucceeded {
		t.Fatalf("padrão de leitura quebrada virou sucesso:\n%s", r.Text)
	}
	if !strings.Contains(r.Text, "metacaractere") {
		t.Errorf("resposta sem flag de metacaractere:\n%s", r.Text)
	}
}

// nb-0310 na forma "0 occurrence(s)": o contador sem lista também cai na
// mesma cerca — e a guarda de padrão flaga antes de qualquer contagem.
func TestVacuousTextCountEmptyNotSucceeded(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("sem python3")
	}
	root := brFixture(t)
	env, _ := brTestEnv(t, root, nil, -1)
	in := Intent{Name: "text_count", Confidence: 0.9,
		Slots: map[string]string{"file": "parser.go", "pattern": "zzz-not-in-file"}}
	r := brRunIntent(context.Background(), env, "count occurrences of zzz-not-in-file in parser.go", in, brResult{})
	if r.Status == tkSucceeded {
		t.Fatalf("0 occurrence(s) virou sucesso:\n%s", r.Text)
	}
	if r.Status != tkUnverified || !strings.Contains(r.Text, "conjunto vazio") {
		t.Fatalf("0 occurrence(s): %s\n%s", r.Status, r.Text)
	}

	// padrão quebrado também falha aqui ("0 occurrence(s) de lixo" não é verde)
	env, _ = brTestEnv(t, root, nil, -1)
	in = Intent{Name: "text_count", Confidence: 0.9,
		Slots: map[string]string{"file": "parser.go", "pattern": "& calculate total caractor"}}
	r = brRunIntent(context.Background(), env, "count occurrences of '& calculate total caractor'", in, brResult{})
	if r.Status == tkSucceeded || !strings.Contains(r.Text, "metacaractere") {
		t.Fatalf("padrão quebrado no count: %s\n%s", r.Status, r.Text)
	}

	// controle: contagem real continua verde
	env, _ = brTestEnv(t, root, nil, -1)
	in = Intent{Name: "text_count", Confidence: 0.9,
		Slots: map[string]string{"file": "parser.go", "pattern": "ParseArgs"}}
	r = brRunIntent(context.Background(), env, "count occurrences of ParseArgs in parser.go", in, brResult{})
	if r.Status != tkSucceeded || !strings.Contains(r.Text, "occurrence(s)") {
		t.Fatalf("count com achado: %s\n%s", r.Status, r.Text)
	}
}

func TestVacuousPermFindEmptyNotSucceeded(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("sem python3")
	}
	root := brFixture(t)
	env, _ := brTestEnv(t, root, nil, -1)

	// suid num fixture comum não existe: "0 match(es)" honesto ≠ sucesso
	in := Intent{Name: "perm_find", Confidence: 0.9,
		Slots: map[string]string{"suid": "true"}}
	r := brRunIntent(context.Background(), env, "find files with the suid bit", in, brResult{})
	if r.Status == tkSucceeded {
		t.Fatalf("perm_find vazio virou sucesso:\n%s", r.Text)
	}
	if r.Status != tkUnverified || !strings.Contains(r.Text, "conjunto vazio") {
		t.Fatalf("perm_find vazio: %s\n%s", r.Status, r.Text)
	}

	// controle: filtro que acha continua verde (fixture 0644 é user-readable)
	env, _ = brTestEnv(t, root, nil, -1)
	in = Intent{Name: "perm_find", Confidence: 0.9,
		Slots: map[string]string{"uread": "true"}}
	r = brRunIntent(context.Background(), env, "files readable by their owner", in, brResult{})
	if r.Status != tkSucceeded {
		t.Fatalf("perm_find com achado: %s\n%s", r.Status, r.Text)
	}
}

// a listagem filtrada ("list the .log files") é o mesmo produtor: conjunto
// vazio não é verde, conjunto com itens sim.
func TestVacuousListFilesFilteredNotSucceeded(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("sem python3")
	}
	root := brFixture(t)
	env, _ := brTestEnv(t, root, nil, -1)

	in := Intent{Name: "list_files", Confidence: 0.9,
		Slots: map[string]string{"ext": "zzzext"}}
	r := brRunIntent(context.Background(), env, "list the .zzzext files", in, brResult{})
	if r.Status == tkSucceeded {
		t.Fatalf("listagem vazia virou sucesso:\n%s", r.Text)
	}
	if r.Status != tkUnverified || !strings.Contains(r.Text, "conjunto vazio") {
		t.Fatalf("listagem vazia: %s\n%s", r.Status, r.Text)
	}

	env, _ = brTestEnv(t, root, nil, -1)
	in = Intent{Name: "list_files", Confidence: 0.9,
		Slots: map[string]string{"ext": "go"}}
	r = brRunIntent(context.Background(), env, "list the .go files", in, brResult{})
	if r.Status != tkSucceeded || !strings.Contains(r.Text, "parser.go") {
		t.Fatalf("listagem com achado: %s\n%s", r.Status, r.Text)
	}
}
