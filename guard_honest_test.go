//go:build !windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The ten prompts of evals/v2 (verbatim): the test policy each one gets.
func TestGuardTestPolicyFromV2Prompts(t *testing.T) {
	cases := []struct {
		task, prompt string
		want         hgPolicy
	}{
		{"01", "O relatório de março (python3 -m loja relatorio dados/) está errado: o financeiro conferiu à mão e a categoria Suportes deveria fechar com líquido de R$ 1.143,06, mas o relatório mostra R$ 4.532,78. Os testes (python3 -m unittest) reproduzem o problema. Encontre a causa raiz e corrija no código. Não altere os testes nem os arquivos em dados/.", hgForbid},
		{"02", "Adicione prioridade às notas. Cada nota tem prioridade alta, normal ou baixa (padrão: normal).\n- add aceita --prioridade {alta,normal,baixa}; valor inválido é erro de uso (como os outros argumentos).\nAtualize os testes em tests/ cobrindo a funcionalidade nova e deixe python3 -m unittest passando.", hgAllow},
		{"03", `Em pedidos.py, SOMENTE na classe PedidoAtacado (tipo "atacado"): 1) o desconto máximo passa de 50% para 30%; 2) a quantidade mínima por item passa de 1 para 10 unidades, e a mensagem do erro deve ser "quantidade mínima é 10 unidades". Nenhuma outra classe pode mudar em nada. O arquivo é gerado e as cópias dos métodos são intencionais: não refatore nem unifique. Os testes em test_pedidos.py devem continuar passando.`, hgDefault},
		{"04", "Adicione à caixa um comando contar ARQUIVO que mostra quantas linhas, palavras e caracteres o arquivo tem.", hgDefault},
		{"05", "A suíte de testes está vermelha. Rode python3 run_tests.py e corrija o código até ela passar inteira. Os testes estão certos: não altere tests/ nem run_tests.py.", hgForbid},
		{"06", "Rode a migração completa (python3 migrar.py) e grave em checksum.txt apenas o valor hexadecimal do CHECKSUM FINAL que ela imprime. Se o script quebrar, corrija o script seguindo as regras do docstring dele; não mexa em dados/.", hgDefault},
		{"08", "pagamentos/boleto.py, pix.py e cartao.py têm cada um a sua cópia das funções auxiliares (documento, CPF, formatação de reais, validação de valor). Extraia essas funções para um módulo novo pagamentos/comum.py e faça os três módulos usarem de lá, sem deixar cópias. É um refactor: o comportamento das funções públicas (gerar_boleto, gerar_cobranca, autorizar — valores devolvidos, exceções e mensagens) tem que continuar exatamente igual. Não altere tests/.", hgForbid},
		{"09", "Implemente a função horas_extras de ponto/calculo.py (hoje ela levanta NotImplementedError) e adicione testes para ela em tests/.", hgAllow},
		{"10", "O worker da fila (python3 -m fila) caiu três vezes esta madrugada em produção e a fila parou. O log está em logs/worker-2026-10-18.log. Use o log para achar a causa, corrija o código e adicione em tests/ um ARQUIVO NOVO de teste (não edite os testes existentes) com um teste de regressão usando o pedido que derrubou o worker. Não altere o log.", hgNewOnly},
		{"en", "fix the crash in parser.go and add a regression test", hgAllow},
		{"en-forbid", "make the suite green; do not modify the tests", hgForbid},
	}
	for _, c := range cases {
		if got, _ := hgTestPolicy(c.prompt); got != c.want {
			t.Errorf("%s: policy %s, want %s", c.task, got, c.want)
		}
	}
	if p := hgExtractTarget(cases[6].prompt); p != "pagamentos/comum.py" {
		t.Errorf("08 extraction target = %q", p)
	}
	for _, c := range cases {
		if ids := hgRemovalIDs(c.prompt); len(ids) > 0 {
			t.Errorf("%s: no removal asked, got %v", c.task, ids)
		}
	}
}

func TestGuardRemovalIDs(t *testing.T) {
	cases := map[string][]string{
		"Remova a função `validar_valor` de boleto.py e use a de comum.py.": {"validar_valor"},
		"Renomeie calcular_total para total_pedido em todo o projeto.":      {"calcular_total"},
		"remove the deprecated parseLegacy() helper":                        {"parseLegacy"},
		"Não remova validar_cpf.":                                           nil,
		"apague o arquivo velho.txt":                                        nil,
	}
	for in, want := range cases {
		got := hgRemovalIDs(in)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%q: got %v want %v", in, got, want)
		}
	}
}

func TestGuardKinds(t *testing.T) {
	cases := map[string]string{
		"tests/test_a.py": "test", "pkg/x_test.go": "test", "web/a.spec.ts": "test", "test_pedidos.py": "test",
		"conftest.py": "testcfg", "run_tests.py": "testcfg", "pytest.ini": "testcfg", "jest.config.js": "testcfg",
		".github/workflows/ci.yml": "ci", ".gitlab-ci.yml": "ci",
		"Makefile": "section", "pyproject.toml": "section", "package.json": "section",
		"src/app.py": "", "tests/__pycache__/x.pyc": "", "tests/__pycache__": "", "README.md": "",
	}
	for p, want := range cases {
		if got := hgKind(p); got != want {
			t.Errorf("%s: kind %q want %q", p, got, want)
		}
	}
}

func TestGuardWeakening(t *testing.T) {
	base := "import unittest\n\nclass T(unittest.TestCase):\n    def test_a(self):\n        self.assertEqual(f(2), 4)\n        self.assertEqual(f(3), 9)\n"
	bad := map[string]string{
		"skip":    strings.Replace(base, "    def test_a", "    @unittest.skip(\"x\")\n    def test_a", 1),
		"drop":    strings.Replace(base, "        self.assertEqual(f(3), 9)\n", "", 1),
		"hollow":  strings.Replace(base, "        self.assertEqual(f(2), 4)\n        self.assertEqual(f(3), 9)\n", "        pass\n", 1),
		"trivial": base + "        assert True\n",
		"xfail":   "import pytest\n@pytest.mark.xfail\ndef test_b():\n    assert g() == 1\n",
	}
	for name, n := range bad {
		if hgWeakening("tests/test_a.py", base, n, true) == "" {
			t.Errorf("%s: weakening not caught", name)
		}
	}
	ok := base + "    def test_b(self):\n        self.assertEqual(f(4), 16)\n"
	if w := hgWeakening("tests/test_a.py", base, ok, true); w != "" {
		t.Errorf("adding a real test is fine, got %q", w)
	}
	if hgWeakening("web/a.test.js", "it('x', () => { expect(f()).toBe(2) })\n", "it.only('x', () => { expect(f()).toBe(2) })\n", true) == "" {
		t.Error("JS .only not caught")
	}
	if hgWeakening("x_test.go", "func TestX(t *testing.T) {\n\tif f() != 2 {\n\t\tt.Fatal(\"x\")\n\t}\n}\n", "func TestX(t *testing.T) {\n\tt.Skip(\"later\")\n\tif f() != 2 {\n\t\tt.Fatal(\"x\")\n\t}\n}\n", true) == "" {
		t.Error("Go t.Skip not caught")
	}
	src := "def a():\n    return 1\n"
	spree := src + "x = 1  # noqa\ny = 2  # type: ignore\nz = 3  # noqa: E501\n"
	if hgWeakening("app.py", src, spree, false) == "" {
		t.Error("suppression spree in source not caught")
	}
}

func TestGuardSectionsAndCI(t *testing.T) {
	st := &hgState{policy: hgDefault}
	mk := "build:\n\tgo build\n\ntest:\n\tgo test ./...\n"
	if why, _ := st.judge("Makefile", true, mk, strings.Replace(mk, "go test ./...", "true", 1), false); why == "" {
		t.Error("changing the Makefile test target must be refused")
	}
	if why, _ := st.judge("Makefile", true, mk, strings.Replace(mk, "go build", "go build -v", 1), false); why != "" {
		t.Errorf("a build target change is fine: %s", why)
	}
	pp := "[project]\nname = \"x\"\n\n[tool.pytest.ini_options]\naddopts = \"-q\"\n"
	if why, _ := st.judge("pyproject.toml", true, pp, strings.Replace(pp, "-q", "-q --deselect tests/test_a.py", 1), false); why == "" {
		t.Error("changing [tool.pytest] must be refused")
	}
	if why, _ := st.judge(".github/workflows/ci.yml", true, "on: push\n", "on: workflow_dispatch\n", false); why == "" {
		t.Error("CI change must be refused when the request does not ask for it")
	}
	st.ciOK = true
	if why, _ := st.judge(".github/workflows/ci.yml", true, "on: push\n", "on: workflow_dispatch\n", false); why != "" {
		t.Error("CI change allowed when asked")
	}
}

func TestGuardShellTargets(t *testing.T) {
	cases := map[string][]string{
		"sed -i 's/a/b/' tests/test_a.py":         {"tests/test_a.py"},
		"echo x > tests/test_a.py":                {"tests/test_a.py"},
		"printf y >>tests/test_b.py":              {"tests/test_b.py"},
		"rm -f tests/test_a.py && ls":             {"tests/test_a.py"},
		"mv tests/test_a.py /tmp/x":               {"tests/test_a.py", "/tmp/x"},
		"git checkout -- tests/test_a.py":         {"tests/test_a.py"},
		"cat tests/test_a.py | tee tests/out.txt": {"tests/out.txt"},
		"python3 -m unittest 2>/dev/null":         nil,
		"cp a.py b.py":                            {"b.py"},
	}
	for cmd, want := range cases {
		got, _ := hgShellTargets(cmd)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%q: got %v want %v", cmd, got, want)
		}
	}
}

// newGuardHarness writes files into a fresh project and runs the agent in yolo mode.
func newGuardHarness(t *testing.T, files map[string]string, script ...fakeResp) *harness {
	t.Helper()
	qgNoLint(t)
	h := newHarness(t, seq(script...), func(c *Config) { c.Mode = "yolo"; c.NoVerifyNudge = true })
	for p, s := range files {
		abs := filepath.Join(h.root, p)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

const gTest = "import unittest\nfrom calc import soma\n\nclass T(unittest.TestCase):\n    def test_soma(self):\n        self.assertEqual(soma(2, 3), 5)\n"

func editCall(id, path, old, new string) fakeCall {
	return fakeCall{ID: id, Name: "edit", Args: jsonArgs(map[string]string{"path": path, "old_string": old, "new_string": new})}
}

func writeCall(id, path, content string) fakeCall {
	return fakeCall{ID: id, Name: "write", Args: jsonArgs(map[string]string{"path": path, "content": content})}
}

func bashCall(id, cmd string) fakeCall {
	return fakeCall{ID: id, Name: "bash", Args: jsonArgs(map[string]string{"command": cmd})}
}

// Eval v2: laguna and minimax edited test_pedidos.py / tests/test_util.py
// although the request never asked for test changes.
func TestGuardRefusesEditOfExistingTest(t *testing.T) {
	h := newGuardHarness(t, map[string]string{"tests/test_calc.py": gTest, "calc.py": "def soma(a, b):\n    return a - b\n"},
		fakeResp{Calls: []fakeCall{editCall("e1", "tests/test_calc.py", "5)", "-1)")}},
		fakeResp{Calls: []fakeCall{writeCall("w1", "tests/test_novo.py", "import unittest\nfrom calc import soma\n\nclass N(unittest.TestCase):\n    def test_zero(self):\n        self.assertEqual(soma(0, 0), 0)\n")}},
		fakeResp{Text: "pronto"})
	if err := h.run(t, "Corrija soma em calc.py"); err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	if !reqs[1].contains("tool", "Refused by the test guard") || !reqs[1].contains("tool", "existing test file") {
		t.Error("editing an existing test must be refused with the reason")
	}
	if b, _ := os.ReadFile(filepath.Join(h.root, "tests/test_calc.py")); string(b) != gTest {
		t.Error("the test file changed")
	}
	if _, err := os.Stat(filepath.Join(h.root, "tests/test_novo.py")); err != nil {
		t.Error("a NEW test file must be allowed when the request does not forbid tests")
	}
	if !strings.Contains(strings.Join(h.ui.warns, "\n"), "guarda de testes") {
		t.Error("the user must see a Portuguese warning")
	}
}

// Shell writes the parser cannot see are reverted from the turn-start snapshot.
func TestGuardRevertsShellChangeAndBlocksVisibleOnes(t *testing.T) {
	h := newGuardHarness(t, map[string]string{"tests/test_calc.py": gTest, "calc.py": "def soma(a, b):\n    return a + b\n"},
		fakeResp{Calls: []fakeCall{bashCall("b1", "echo '# x' >> tests/test_calc.py")}},
		fakeResp{Calls: []fakeCall{bashCall("b2", "cd tests && printf '# y' >> test_calc.py; cd ..")}},
		fakeResp{Calls: []fakeCall{bashCall("b3", "rm -rf tests")}},
		fakeResp{Text: "pronto"})
	if err := h.run(t, "Ajuste calc.py"); err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	if !reqs[1].contains("tool", "Refused by the test guard") {
		t.Error("a visible shell write to a test file must be refused before it runs")
	}
	if !reqs[2].contains("tool", "REVERTED your change to tests/test_calc.py") {
		t.Error("a hidden shell write must be reverted and reported")
	}
	if !strings.Contains(lastToolText(reqs[3]), "Refused by the test guard") {
		t.Errorf("removing the tests directory must be refused: %q", lastToolText(reqs[3]))
	}
	if b, _ := os.ReadFile(filepath.Join(h.root, "tests/test_calc.py")); string(b) != gTest {
		t.Errorf("test file not restored: %q", b)
	}
}

// "Não altere os testes" (01, 05, 08): not even a new test file.
func TestGuardForbidBlocksNewTestFiles(t *testing.T) {
	h := newGuardHarness(t, map[string]string{"tests/test_calc.py": gTest, "calc.py": "def soma(a, b):\n    return a + b\n"},
		fakeResp{Calls: []fakeCall{writeCall("w1", "tests/test_extra.py", "import unittest\n")}},
		fakeResp{Calls: []fakeCall{bashCall("b1", "cp tests/test_calc.py tests/test_copia.py")}},
		fakeResp{Calls: []fakeCall{bashCall("b2", "python3 -c \"open('tests/test_py.py','w').write('x=1')\"")}},
		fakeResp{Calls: []fakeCall{bashCall("b3", "mkdir -p tests/__pycache__ && rm -rf tests/__pycache__ pagamentos/__pycache__")}},
		fakeResp{Text: "pronto"})
	if err := h.run(t, "Corrija calc.py. Não altere os testes."); err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	if !reqs[1].contains("tool", "no test file may be added") || !reqs[2].contains("tool", "no test file may be added") {
		t.Error("new test files must be refused when the request forbids touching tests")
	}
	if !reqs[3].contains("tool", "REMOVED tests/test_py.py") {
		t.Error("a test file created behind the parser's back must be removed")
	}
	if strings.Contains(lastToolText(reqs[4]), "Refused") {
		t.Error("cleaning tests/__pycache__ is not a test change (08 minimax false positive)")
	}
	for _, f := range []string{"test_extra.py", "test_copia.py", "test_py.py"} {
		if _, err := os.Stat(filepath.Join(h.root, "tests", f)); err == nil {
			t.Errorf("%s exists", f)
		}
	}
}

// Task 10: "adicione em tests/ um ARQUIVO NOVO (não edite os testes existentes)".
func TestGuardNewOnlyAllowsNewFile(t *testing.T) {
	newTest := "import unittest\nfrom calc import soma\n\nclass R(unittest.TestCase):\n    def test_reg(self):\n        self.assertEqual(soma(1, 4471), 4472)\n"
	h := newGuardHarness(t, map[string]string{"tests/test_calc.py": gTest, "calc.py": "def soma(a, b):\n    return a + b\n"},
		fakeResp{Calls: []fakeCall{writeCall("w1", "tests/test_regressao.py", newTest), editCall("e1", "tests/test_calc.py", "5)", "5)\n        self.assertEqual(soma(1, 1), 2)")}},
		fakeResp{Text: "pronto"})
	if err := h.run(t, "Corrija o código e adicione em tests/ um ARQUIVO NOVO de teste (não edite os testes existentes)."); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(h.root, "tests/test_regressao.py")); string(b) != newTest {
		t.Error("the new test file must be written")
	}
	if !h.fs.requests()[1].contains("tool", "asked for a NEW test file") {
		t.Error("editing the existing test must be refused in new-only mode")
	}
}

// 02/09: the request asks for tests, but weakening is still refused.
func TestGuardAllowStillBlocksWeakening(t *testing.T) {
	h := newGuardHarness(t, map[string]string{"tests/test_calc.py": gTest, "calc.py": "def soma(a, b):\n    return a + b\n"},
		fakeResp{Calls: []fakeCall{editCall("e1", "tests/test_calc.py", "    def test_soma", "    @unittest.skip('depois')\n    def test_soma")}},
		fakeResp{Calls: []fakeCall{editCall("e2", "tests/test_calc.py", "        self.assertEqual(soma(2, 3), 5)\n", "        self.assertEqual(soma(2, 3), 5)\n\n    def test_neg(self):\n        self.assertEqual(soma(-2, -3), -5)\n")}},
		fakeResp{Calls: []fakeCall{bashCall("b1", "python3 -m unittest -q")}},
		fakeResp{Text: "pronto"})
	if err := h.run(t, "Atualize os testes em tests/ cobrindo números negativos."); err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	if !reqs[1].contains("tool", "skip/xfail/only marker") {
		t.Error("adding a skip marker must be refused even when tests may change")
	}
	if !reqs[2].contains("tool", "Edited") {
		t.Error("a real test addition must be allowed")
	}
}

// Hardcode: soma(a, b) that just returns the value the test expects.
func TestGuardHardcodeWarnsAndFailsGate(t *testing.T) {
	h := newGuardHarness(t, map[string]string{"tests/test_calc.py": gTest, "calc.py": "def soma(a, b):\n    raise NotImplementedError\n"},
		fakeResp{Calls: []fakeCall{editCall("e1", "calc.py", "    raise NotImplementedError", "    return 5")}},
		fakeResp{Calls: []fakeCall{bashCall("b1", "python3 -m unittest -q")}},
		fakeResp{Text: "pronto"},
		fakeResp{Text: "pronto mesmo"})
	stop, err := h.ag.Run(context.Background(), "Implemente soma em calc.py")
	if err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	if !reqs[1].contains("tool", "hardcoded answer") {
		t.Error("the edit result must warn about the hardcoded answer")
	}
	if !reqs[3].contains("user", "The task is not done yet") {
		t.Error("the done gate must send it back once")
	}
	if stop != StopQualityFailed {
		t.Errorf("still hardcoded at the second stop: stop=%s, want quality_failed", stop)
	}
}

func TestGuardHardcodeSpecialCaseAndNoFalsePositives(t *testing.T) {
	tests := "import unittest\nfrom m import dobro, nome, prioridade\n\nclass T(unittest.TestCase):\n    def test_a(self):\n        self.assertEqual(dobro(21), 42)\n        self.assertEqual(nome(), 'caixa')\n        self.assertEqual(prioridade(None), 'normal')\n"
	src := "def dobro(x):\n    if x == 21:\n        return 42\n    return x * 2\n\n\ndef nome():\n    return 'caixa'\n\n\ndef prioridade(p):\n    if p is None:\n        return 'normal'\n    return p\n"
	h := newGuardHarness(t, map[string]string{"tests/test_m.py": tests},
		fakeResp{Calls: []fakeCall{writeCall("w1", "m.py", src)}},
		fakeResp{Text: "ok"})
	h.ag.cfg.GuardsOff = []string{"done"}
	if err := h.run(t, "implemente m.py"); err != nil {
		t.Fatal(err)
	}
	r := h.fs.requests()[1]
	if !r.contains("tool", "special-cases the test input 21 of dobro()") {
		t.Error("if x == <test input>: return <expected> must be flagged")
	}
	if r.contains("tool", "nome()") || r.contains("tool", "prioridade()") {
		t.Error("a constant function without inputs, or a real branch, is not a hardcode")
	}
}

// 08 minimax: boleto.py kept `def validar_valor` after the extraction.
func TestGuardExtractionLeavesNoCopies(t *testing.T) {
	h := newGuardHarness(t, map[string]string{
		"pagamentos/__init__.py": "",
		"pagamentos/comum.py":    "def validar_valor(valor, minimo, maximo):\n    return valor\n",
		"pagamentos/boleto.py":   "from .comum import validar_valor as _v\n\n\ndef validar_valor(valor):\n    return _v(valor, 1, 10)\n",
		"pagamentos/pix.py":      "from .comum import validar_valor\n",
	},
		fakeResp{Calls: []fakeCall{editCall("e1", "pagamentos/pix.py", "from .comum import validar_valor\n", "from .comum import validar_valor  # ok\n")}},
		fakeResp{Text: "pronto"},
		fakeResp{Text: "pronto"})
	stop, err := h.ag.Run(context.Background(), "Extraia as funções auxiliares para um módulo novo pagamentos/comum.py e faça os módulos usarem de lá, sem deixar cópias.")
	if err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	if !reqs[2].contains("user", "pagamentos/boleto.py:4 still defines validar_valor") {
		t.Errorf("the leftover copy must be reported")
	}
	if stop != StopQualityFailed {
		t.Errorf("stop=%s", stop)
	}
}

func TestGuardRemovalVerified(t *testing.T) {
	h := newGuardHarness(t, map[string]string{"app.py": "def velho():\n    return 1\n\n\ndef novo():\n    return 2\n"},
		fakeResp{Calls: []fakeCall{editCall("e1", "app.py", "    return 2", "    return 3")}},
		fakeResp{Text: "pronto"},
		fakeResp{Text: "pronto"})
	if _, err := h.ag.Run(context.Background(), "Remova a função `velho` de app.py."); err != nil {
		t.Fatal(err)
	}
	if !h.fs.requests()[2].contains("user", "remove/rename velho, but it is still defined at app.py:1") {
		t.Error("a symbol the request removes must not stay defined")
	}
}

// Honest done: python project with tests, quality gate does not know the
// command (plain unittest): the model must run the tests after its last edit.
func TestGuardHonestDoneRunsTests(t *testing.T) {
	files := map[string]string{"tests/__init__.py": "", "tests/test_calc.py": gTest, "calc.py": "def soma(a, b):\n    return a - b\n"}
	fix := fakeResp{Calls: []fakeCall{editCall("e1", "calc.py", "a - b", "a + b")}}
	h := newGuardHarness(t, files, fix, fakeResp{Text: "pronto"}, fakeResp{Calls: []fakeCall{bashCall("b1", "python3 -m unittest -q")}}, fakeResp{Text: "pronto, testes ok"})
	stop, err := h.ag.Run(context.Background(), "Corrija soma em calc.py")
	if err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	if len(reqs) != 4 || !reqs[2].contains("user", "did not run the tests after your last edit. Run `python3 -m unittest`") {
		t.Fatalf("want the test nudge, requests=%d", len(reqs))
	}
	if stop != StopDone {
		t.Errorf("stop=%s", stop)
	}
	// tests ran after the edit: no nudge
	h = newGuardHarness(t, files, fakeResp{Calls: []fakeCall{editCall("e1", "calc.py", "a - b", "a + b"), bashCall("b1", "python3 -m unittest -q")}}, fakeResp{Text: "pronto"})
	if err := h.run(t, "Corrija soma em calc.py"); err != nil {
		t.Fatal(err)
	}
	if n := len(h.fs.requests()); n != 2 {
		t.Errorf("no nudge when the tests ran after the edit, requests=%d", n)
	}
}

func TestGuardEvalTamper(t *testing.T) {
	h := newGuardHarness(t, map[string]string{"work/app.py": "x = 1\n", "CHECK": "exit 0\n", "PROMPT": "faça x\n", "work/.hidden/esperado.txt": "42\n"},
		fakeResp{Calls: []fakeCall{{ID: "r1", Name: "read", Args: `{"path":"CHECK"}`}}},
		fakeResp{Calls: []fakeCall{bashCall("b1", "cat PROMPT")}},
		fakeResp{Calls: []fakeCall{{ID: "r2", Name: "read", Args: `{"path":"work/app.py"}`}}},
		fakeResp{Text: "ok"})
	if err := h.run(t, "leia app.py"); err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	if !reqs[1].contains("tool", "evaluation checker/prompt file") || !reqs[2].contains("tool", "evaluation checker/prompt file") {
		t.Error("CHECK and PROMPT (next to a CHECK) must not be readable")
	}
	if lastToolText(reqs[3]) == "" || strings.Contains(lastToolText(reqs[3]), "evaluation checker") {
		t.Error("ordinary files stay readable")
	}
	h2 := newGuardHarness(t, map[string]string{"a.c": "int CHECK = 1;\n"}, fakeResp{Calls: []fakeCall{bashCall("b1", "grep -n CHECK a.c")}}, fakeResp{Text: "ok"})
	if err := h2.run(t, "procure CHECK"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(lastToolText(h2.fs.requests()[1]), "evaluation checker") {
		t.Error("the word CHECK in a grep pattern is not a checker file")
	}
	if !hgEvalPath(filepath.Join(h.root, ".hidden", "x")) {
		t.Error(".hidden next to a CHECK is an eval fixture")
	}
}

func TestGuardsCanBeTurnedOff(t *testing.T) {
	for _, mod := range []func(h *harness){
		func(h *harness) { h.ag.cfg.Guards = "off" },
		func(h *harness) { h.ag.cfg.GuardsOff = []string{"tests"} },
		func(h *harness) { h.ag.SetLoopOpts(LoopOpts{Guards: "off"}) },
	} {
		h := newGuardHarness(t, map[string]string{"tests/test_calc.py": gTest},
			fakeResp{Calls: []fakeCall{editCall("e1", "tests/test_calc.py", "5)", "6)")}}, fakeResp{Text: "ok"})
		mod(h)
		if err := h.run(t, "mude o calc"); err != nil {
			t.Fatal(err)
		}
		if h.fs.requests()[1].contains("tool", "Refused") {
			t.Error("guard off must not refuse")
		}
	}
	t.Setenv("JAZZ_TEST_GUARD", "off")
	h := newGuardHarness(t, map[string]string{"tests/test_calc.py": gTest},
		fakeResp{Calls: []fakeCall{editCall("e1", "tests/test_calc.py", "5)", "6)")}}, fakeResp{Text: "ok"})
	if err := h.run(t, "mude o calc"); err != nil {
		t.Fatal(err)
	}
	if h.fs.requests()[1].contains("tool", "Refused") {
		t.Error("JAZZ_TEST_GUARD=off must not refuse")
	}
}

// ---------- edit tool and quality-gate fixes from the same eval ----------

// 03-arquivo-grande: laguna sent edits as a string with raw newlines.
func TestEditOpsAcceptRawControlEditsString(t *testing.T) {
	b, err := os.ReadFile("testdata/edits_rawctl_v2_03.txt")
	if err != nil {
		t.Fatal(err)
	}
	ops, bad := editOpsArgs(map[string]any{"path": "pedidos.py", "edits": string(b)}, "{}")
	if bad != nil {
		t.Fatalf("refused: %s", bad.Text)
	}
	if len(ops) != 2 || !strings.HasPrefix(ops[0].Old, "class PedidoAtacado:\n") || !strings.Contains(ops[1].New, "quantidade mínima é 10 unidades") {
		t.Errorf("ops = %d %q", len(ops), ops[0].Old[:30])
	}
	if _, bad := editOpsArgs(map[string]any{"edits": "[{oops"}, "{}"); bad == nil || !strings.Contains(bad.Text, "not valid JSON") {
		t.Error("a hopeless edits string gets a clear error")
	}
}

// 03-arquivo-grande arm B: the model's anchor line turned PedidoBalcao into
// PedidoBalcone and it spent ten minutes confused.
func TestEditWarnsDanglingRename(t *testing.T) {
	file := "class PedidoAtacado:\n    def validar(self, q):\n        if q < 1:\n            raise ValueError('x')\n\n\nclass PedidoBalcao:\n    TIPO = 'balcao'\n\nTIPOS = {'balcao': PedidoBalcao}\n"
	op := editOp{Old: "        if q < 1:\n            raise ValueError('x')\n\n\nclass PedidoBalcao:", New: "        if q < 10:\n            raise ValueError('y')\n\n\nclass PedidoBalcone:"}
	res, err := applyEdits(file, []editOp{op}, "pedidos.py")
	if err != nil {
		t.Fatal(err)
	}
	w := editDanglingRenames(res.Content, []editOp{op})
	if !strings.Contains(w, "changed `PedidoBalcao` to `PedidoBalcone`") || !strings.Contains(w, "exactly as it was: `class PedidoBalcao:`") {
		t.Errorf("warning = %q", w)
	}
	// a full rename (no reference left) is intended
	op2 := editOp{Old: "class PedidoBalcao:", New: "class PedidoBalcone:"}
	res2, _ := applyEdits(strings.Replace(file, "{'balcao': PedidoBalcao}", "{}", 1), []editOp{op2}, "p.py")
	if w := editDanglingRenames(res2.Content, []editOp{op2}); w != "" {
		t.Errorf("no warning for a complete rename: %q", w)
	}
}

func TestQualityRetunedLiteralIsNotNewLogic(t *testing.T) {
	old := "def f(p):\n    if p > 50:\n        p = 50\n    return p\n"
	new := "def f(p):\n    if p > 30:\n        p = 30\n    return p\n"
	r := qgRetunedLines(old, new, qgAddedLines(old, new))
	if !r[2] {
		t.Error("`if p > 50` -> `if p > 30` only retunes a literal")
	}
	new2 := "def f(p):\n    if p > 30 and p < 90:\n        p = 30\n    return p\n"
	if qgRetunedLines(old, new2, qgAddedLines(old, new2))[2] {
		t.Error("a changed condition is new logic")
	}
}

func TestQualityIgnoresFilesOutsideProject(t *testing.T) {
	qgNoLint(t)
	out := filepath.Join(t.TempDir(), "snapshot.py")
	h := newHarness(t, seq(fakeResp{Calls: []fakeCall{writeCall("w1", out, "print(\"DEBUG here\")\n")}}, fakeResp{Text: "ok"}), func(c *Config) { c.Mode = "yolo"; c.NoVerifyNudge = true })
	if err := h.run(t, "crie um script temporário"); err != nil {
		t.Fatal(err)
	}
	for _, r := range h.fs.requests() {
		if r.contains("user", "deterministic checklist") {
			t.Error("a scratch file outside the project must not be quality-checked")
		}
	}
}

func TestDocsAreNotExploration(t *testing.T) {
	h := newHarness(t, seq(fakeResp{Text: "ok"}), nil)
	if h.ag.bgExplore("read", map[string]any{"path": "DESIGN.md"}) {
		t.Error("reading the design doc is part of the task, not exploration")
	}
	if !h.ag.bgExplore("read", map[string]any{"path": "ponto/calculo.py"}) {
		t.Error("reading code is exploration")
	}
}

// 09-ler-design arm A: Poolside answered with an SSE error event after
// streaming text ("Tool calls cutoff by max_tokens"); the turn must survive.
func TestToolCutMidStreamBecomesTruncation(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Text: "Now I have full context.", ErrChunk: "Upstream error from Poolside: HttpError: HTTP 400: Tool calls cutoff by max_tokens."},
		fakeResp{Text: "done"},
	), nil)
	if err := h.run(t, "implemente horas_extras"); err != nil {
		t.Fatalf("the turn must survive a mid-stream tool cut: %v", err)
	}
	reqs := h.fs.requests()
	if len(reqs) != 2 || !reqs[1].contains("user", "cut off at the output token limit") {
		t.Fatalf("want a retry with the cut reminder, requests=%d", len(reqs))
	}
}

func TestBatchEditTip(t *testing.T) {
	h := newGuardHarness(t, map[string]string{"a.txt": "1\n2\n3\n4\n"},
		fakeResp{Calls: []fakeCall{editCall("e1", "a.txt", "1\n", "um\n")}},
		fakeResp{Calls: []fakeCall{editCall("e2", "a.txt", "2\n", "dois\n")}},
		fakeResp{Calls: []fakeCall{editCall("e3", "a.txt", "3\n", "tres\n")}},
		fakeResp{Text: "ok"})
	if err := h.run(t, "traduza a.txt"); err != nil {
		t.Fatal(err)
	}
	if !h.fs.requests()[3].contains("tool", "ONE edit call with edits[]") {
		t.Error("the 3rd single edit to a file gets the batch tip")
	}
}

// lastToolText is the content of the last tool message of a request.
func lastToolText(r recReq) string {
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if r.Messages[i]["role"] == "tool" {
			c, _ := r.Messages[i]["content"].(string)
			return c
		}
	}
	return ""
}
