package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// niFixture writes files (path -> content) into a temp repo, old by default.
func niFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	niResetTouched()
	root := t.TempDir()
	old := time.Now().Add(-72 * time.Hour)
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(p, old, old)
	}
	return root
}

func niWant(t *testing.T, root, prompt string, want ...string) string {
	t.Helper()
	n := niNote(root, prompt)
	for _, w := range want {
		if !strings.Contains(n, w) {
			t.Errorf("%q\n  note: %s\n  missing: %s", prompt, n, w)
		}
	}
	return n
}

func niNone(t *testing.T, root, prompt string) {
	t.Helper()
	if n := niNote(root, prompt); n != "" {
		t.Errorf("%q should add nothing, got: %s", prompt, n)
	}
}

// Context beats raw edit distance (>= 8 cases, PT with typos).
func TestNameContextBeatsEditDistance(t *testing.T) {
	// 1. the sentence is about tests: parser_test.go, not the closer parse.go
	r := niFixture(t, map[string]string{"parse.go": "", "parser_test.go": "", "parser.go": ""})
	niWant(t, r, "roda os testes do parsr", "`parser_test.go`")
	// 1b. no test in the sentence: the neighbour-key slip (parse.go) wins and the reason names the cue
	niWant(t, r, "conserta o arquivo parsr", "`parse.go`", "depois de 'arquivo'")

	// 2. a file read earlier in this session breaks the tie (conf.go vs config.go)
	r = niFixture(t, map[string]string{"conf.go": "", "config.go": ""})
	n := niWant(t, r, "edita o confg.go", "`conf.go` ou `config.go`", "empate")
	_ = n
	niTouch(filepath.Join(r, "config.go"))
	niWant(t, r, "edita o confg.go", "`confg.go` → `config.go`", "nesta sessão")

	// 3. the extension typed decides (mian.go: main.go, not mina.py)
	r = niFixture(t, map[string]string{"main.go": "", "mina.py": ""})
	niWant(t, r, "olha o mian.go", "`main.go`")

	// 4. a neighbouring key beats an arbitrary letter (parsee -> parser, not parsel)
	r = niFixture(t, map[string]string{"parser.go": "", "parsel.go": ""})
	niWant(t, r, "abre o parsee.go", "`parser.go`")

	// 5. "função" asks for a symbol, not the file with a similar name
	r = niFixture(t, map[string]string{"calcular_frete.go": "package x\n", "pricing.go": "package x\n\nfunc calcularFrete(p int) int { return p }\n"})
	niWant(t, r, "arruma a função calcularFrte", "calcularFrete (pricing.go)", "símbolo")
	n = niNote(r, "arruma a função calcularFrte")
	if strings.Contains(n, "`calcular_frete.go`") {
		t.Errorf("function cue must not offer the file: %s", n)
	}
	// 5b. without the cue both readings are listed
	niWant(t, r, "arruma o calcularFrte", "calcular_frete.go", "calcularFrete")

	// 6. the repo language breaks a tie (server.go vs server.js in a Go repo)
	r = niFixture(t, map[string]string{"server.go": "", "server.js": "", "a.go": "", "b.go": "", "c.go": ""})
	niWant(t, r, "abre o arquivo sever", "`server.go`", "linguagem principal")

	// 7. code over docs for a test name: tests/test_calculo.py, not docs/test_calculo.md
	r = niFixture(t, map[string]string{"tests/test_calculo.py": "", "docs/test_calculo.md": ""})
	niWant(t, r, "roda test_calculo", "`tests/test_calculo.py`")

	// 8. the directory the user typed decides (src/handler.go, not lib/handler.go)
	r = niFixture(t, map[string]string{"src/handler.go": "", "lib/handler.go": ""})
	niWant(t, r, "olha src/handlr.go", "`src/handler.go`")

	// 9. a file edited today wins an otherwise equal pair
	r = niFixture(t, map[string]string{"utils.go": "", "utilz.go": ""})
	now := time.Now()
	_ = os.Chtimes(filepath.Join(r, "utilz.go"), now, now)
	niWant(t, r, "mexe no utilx.go", "`utilz.go`", "modificado hoje")

	// 10. tests/ directory typo
	r = niFixture(t, map[string]string{"tests/test_calculo.py": "", "src/calculo.py": ""})
	niWant(t, r, "roda o teste teest/test_calculo.py", "`tests/test_calculo.py`")
}

func TestNameSeparatorsCaseAndExtension(t *testing.T) {
	r := niFixture(t, map[string]string{"tests/test_calculo.py": "", "src/user_service.go": ""})
	niWant(t, r, "olha o Test-Calculo.py", "`tests/test_calculo.py`")
	niWant(t, r, "roda o test_calculo", "`tests/test_calculo.py`") // missing extension and directory
	niWant(t, r, "ve o userService.go", "`src/user_service.go`")   // camel vs snake
}

func TestNameNotForced(t *testing.T) {
	r := niFixture(t, map[string]string{"parser.go": "package x\n\nfunc ParseConfig() {}\n", "config.go": ""})
	niNone(t, r, "o parsr está lento hoje")                  // a plain word is not a name
	niNone(t, r, "melhora o fluxo de pagamento do checkout") // a feature, not a file: let the model search
	niNone(t, r, "corrige o xyzzy.go")                       // nothing close
	niNone(t, r, "abre o parser.go")                         // exists as written
	niNone(t, r, "abre o arquivo parser.go e ve ParseConfig")
	niNone(t, r, "roda os testes")
	niNone(t, r, "abre o terminal")
	niNone(t, r, "ve https://exemplo.com/parsr.html")
	niNone(t, r, "isso e/ou aquilo")
}

func TestNameOpenVerbPlainWord(t *testing.T) {
	r := niFixture(t, map[string]string{"config.go": "", "main.go": ""})
	niWant(t, r, "abre o config", "`config` → `config.go`", "junto de 'abre'")
}

func TestNameReasonIsExplained(t *testing.T) {
	r := niFixture(t, map[string]string{"parser.go": "", "lexer.py": ""})
	n := niWant(t, r, "conserta o parsr.go", "`parsr.go` → `parser.go`", "junto de 'conserta'", "único .go com esse radical")
	if !strings.HasPrefix(n, "nomes aproximados") {
		t.Errorf("note prefix: %s", n)
	}
	// verb typos still count as the verb
	niWant(t, r, "concerta o parsr.go", "junto de 'concerta'")
}

func TestNameTieListsBoth(t *testing.T) {
	r := niFixture(t, map[string]string{"parser.go": "", "parsel.go": ""})
	niWant(t, r, "abre o parsex.go", "`parsel.go`", "`parser.go`", "empate")
}

func TestNameInjectIsUserSideAndKeepsText(t *testing.T) {
	r := niFixture(t, map[string]string{"parser.go": ""})
	a := &Agent{tools: &Toolbox{Root: r}}
	inj := a.niInject("conserta o parsr.go")
	if !strings.HasPrefix(inj, "\n\n<system-reminder>\nnomes aproximados") || !strings.HasSuffix(inj, "</system-reminder>") {
		t.Errorf("inject: %q", inj)
	}
	if (&Agent{sub: true, tools: &Toolbox{Root: r}}).niInject("conserta o parsr.go") != "" {
		t.Error("subagents get no note")
	}
}

func TestNameIndexCapAndTiming(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 300; i++ {
		d := filepath.Join(root, "d"+string(rune('a'+i%20)))
		_ = os.MkdirAll(d, 0o755)
		_ = os.WriteFile(filepath.Join(d, "f"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+".go"), nil, 0o644)
	}
	_ = os.MkdirAll(filepath.Join(root, "node_modules", "x"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "node_modules", "x", "skip.js"), nil, 0o644)
	ix := niBuild(root)
	if _, ok := ix.rels["node_modules/x/skip.js"]; ok {
		t.Error("node_modules must be skipped")
	}
	if len(ix.files) == 0 || ix.buildMS < 0 {
		t.Error("empty index")
	}
}

func TestNameToolErrorSuggestions(t *testing.T) {
	r := niFixture(t, map[string]string{"src/parser.go": "package p\n", "src/lexer.go": "package p\n"})
	tb := NewToolbox(r)
	out := tb.read(nil, map[string]any{"path": "src/parsr.go"}, "")
	if !out.IsErr || !strings.Contains(out.Text, "não existe; parecido: src/parser.go") {
		t.Errorf("read: %s", out.Text)
	}
	out = tb.edit(map[string]any{"path": "src/lexr.go", "old_string": "a", "new_string": "b"}, "")
	if !out.IsErr || !strings.Contains(out.Text, "parecido: src/lexer.go") {
		t.Errorf("edit: %s", out.Text)
	}
	// nothing close: the old message stays
	out = tb.read(nil, map[string]any{"path": "zzzz/qqqq.go"}, "")
	if strings.Contains(out.Text, "parecido") {
		t.Errorf("no suggestion expected: %s", out.Text)
	}
	// write creating a near-twin says so
	out = tb.write(map[string]any{"path": "src/parsre.go", "content": "package p\n"}, "")
	if out.IsErr || !strings.Contains(out.Text, "já existe parecido: src/parser.go") {
		t.Errorf("write: %v %s", out.IsErr, out.Text)
	}
	out = tb.write(map[string]any{"path": "src/brandnew.go", "content": "package p\n"}, "")
	if strings.Contains(out.Text, "parecido") {
		t.Errorf("write of an unrelated name: %s", out.Text)
	}
}

func TestNameEmptySearchHint(t *testing.T) {
	r := niFixture(t, map[string]string{"a.go": "package p\n\nfunc ParseConfig() {}\n"})
	h := niEmptySearchHint(r, "where", "rg -n -S -w -- ParseConfg . | head -n 80", "", 1)
	if !strings.Contains(h, "ParseConfig (a.go)") {
		t.Errorf("hint: %q", h)
	}
	if niEmptySearchHint(r, "where", "rg -- ParseConfg .", "x", 1) != "" || niEmptySearchHint(r, "test", "go test", "", 1) != "" {
		t.Error("only an empty symbol search gets a hint")
	}
}

// Timing on this repo itself (run with -v to see it).
func TestNameIndexTimingOnRepo(t *testing.T) {
	t0 := time.Now()
	ix := niBuild(".")
	files := time.Since(t0)
	t1 := time.Now()
	n := len(ix.symbols())
	syms := time.Since(t1)
	t2 := time.Now()
	note := niNote(".", "conserta o parsr.go e roda os testes do agnt_test.go")
	q := time.Since(t2)
	t.Logf("files=%d index=%s | symbols=%d in %s | note (cached index) %s | %q", len(ix.files), files, n, syms, q, note)
	if files > 3*time.Second {
		t.Errorf("index build too slow: %s", files)
	}
}
