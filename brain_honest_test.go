package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// brHonestRepo is a temp dir that looks like a git repository with a git binary.
func brHonestRepo(t *testing.T) (*brEnv, *brRec) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n\ngo 1.22\n"), 0o644)
	env, rec := brTestEnv(t, root, map[string]brFakeOut{
		"git stash list": {out: "", exit: 0},
		"git branch":     {out: "* main\n", exit: 0},
		"git log":        {out: "abc1234 primeiro\n", exit: 0},
		"git":            {out: "", exit: 0},
	}, 0)
	env.Ask = nil // headless, como `jazz -p --offline`
	return env, rec
}

// Pergunta nunca dispara intenção que muda coisa. Regressão da sonda de 89
// pedidos: "qual foi o último commit" rodava git add -A + git commit -m 'último'.
func TestBrainQuestionNeverMutates(t *testing.T) {
	for _, q := range []string{
		"qual foi o último commit",
		"quem fez o último commit?",
		"quando foi o commit do parser",
		"what was the last commit",
		"como eu apago esse arquivo notas.txt",
	} {
		env, rec := brHonestRepo(t)
		r := brRun(context.Background(), env, q)
		if r.Status == tkSucceeded && r.Intent.Name != "git_log" && brMutates(r.Intent, brIntentRisk(env, r.Intent)) {
			t.Errorf("%q: pergunta virou ação %s com selo verde", q, r.Intent.Name)
		}
		for _, c := range rec.cmds {
			if strings.Contains(c, "commit") || strings.Contains(c, "git add") || strings.HasPrefix(c, "rm ") {
				t.Errorf("%q: pergunta rodou comando de escrita: %s", q, c)
			}
		}
	}
	// o imperativo continua valendo
	for _, cmd := range []string{"commita com a mensagem 'wip'", "faz um commit com a mensagem \"wip\""} {
		env, _ := brHonestRepo(t)
		if r := brRun(context.Background(), env, cmd); r.MissWhy == "question_mutation" {
			t.Errorf("%q: pedido imperativo tratado como pergunta", cmd)
		}
	}
}

// Pedido de escrita não é respondido pelo método de leitura com selo verde.
func TestBrainWriteRequestNotAnsweredByRead(t *testing.T) {
	for _, q := range []string{
		"guarda minhas mudanças no stash",
		"cria uma branch chamada teste",
		"git stash",
		"stash my changes",
		"create a branch called feature-x",
	} {
		env, rec := brHonestRepo(t)
		r := brRun(context.Background(), env, q)
		if r.Status == tkSucceeded {
			t.Errorf("%q: selado como feito (%s) rodando só leitura: %v", q, r.Intent.Name, rec.cmds)
		}
		if r.Intent.Name == "git_stash" || r.Intent.Name == "git_branch" {
			if r.MissWhy != "write_unsupported" {
				t.Errorf("%q: esperava recusa write_unsupported, veio miss=%v why=%q status=%s", q, r.Miss, r.MissWhy, r.Status)
			}
			if len(rec.cmds) != 0 {
				t.Errorf("%q: não devia rodar nada, rodou %v", q, rec.cmds)
			}
			if !strings.Contains(r.Text, "não fiz nada") {
				t.Errorf("%q: a recusa tem que dizer que nada foi feito: %q", q, r.Text)
			}
		}
	}
	// leitura explícita segue funcionando
	for q, want := range map[string]string{
		"lista as branches":   "git_branch",
		"em que branch eu tô": "git_branch",
		"mostra o stash":      "git_stash",
		"lista os stashes":    "git_stash",
	} {
		env, _ := brHonestRepo(t)
		r := brRun(context.Background(), env, q)
		if r.Intent.Name == want && r.MissWhy == "write_unsupported" {
			t.Errorf("%q: leitura explícita foi recusada como escrita", q)
		}
	}
}

// Trabalho que precisa de modelo não vira um arquivo vazio.
func TestBrainModelWorkIsNotCreate(t *testing.T) {
	env, _ := brHonestRepo(t)
	r := brRun(context.Background(), env, "escreve um teste pra função Soma")
	if r.Status == tkSucceeded {
		t.Errorf("selado como feito: %s %v", r.Intent.Name, r.Intent.Slots)
	}
	if _, err := os.Stat(filepath.Join(env.Root, "Soma")); err == nil {
		t.Error("criou um arquivo chamado Soma")
	}
	// criar arquivo de verdade continua valendo
	env2, _ := brHonestRepo(t)
	if r := brRun(context.Background(), env2, "cria um arquivo notas.txt"); r.MissWhy == "needs_model" || r.MissWhy == "write_unsupported" {
		t.Errorf("criação simples de arquivo foi recusada: %q", r.Text)
	}
}

func TestBrIsQuestion(t *testing.T) {
	for _, q := range []string{"qual foi o último commit", "Quem mexeu aqui", "e quando foi isso", "what changed", "how do I commit", "tem algum TODO", "por que falhou", "pq falhou", "cadê o arquivo"} {
		if !brIsQuestion(q) {
			t.Errorf("devia ser pergunta: %q", q)
		}
	}
	for _, q := range []string{"commita tudo", "apaga o notas.txt", "commita isso pra mim?", "roda os testes", "whatever.txt abre", "isso", "doc.md mostra"} {
		if brIsQuestion(q) {
			t.Errorf("não é pergunta: %q", q)
		}
	}
}

func TestBrNoTestsRan(t *testing.T) {
	none := "ok  \texample.com/a\t0.003s [no tests to run]\nok  \texample.com/b\t0.002s [no tests to run]\n?   \texample.com/c\t[no test files]\n"
	some := "ok  \texample.com/a\t0.003s [no tests to run]\nok  \texample.com/b\t0.002s\n"
	if !brNoTestsRan(none) {
		t.Error("todas as suítes sem teste rodado deviam contar como nada rodou")
	}
	if brNoTestsRan(some) {
		t.Error("uma suíte rodou: não é 'nada rodou'")
	}
	if brNoTestsRan("") || brNoTestsRan("FAIL\texample.com/a\t0.01s\n") {
		t.Error("saída vazia ou falha não é 'nada rodou'")
	}
	if !brNoTestsRan("collected 0 items\n\nno tests ran in 0.01s\n") {
		t.Error("pytest sem teste coletado")
	}
}

func TestBrBareFile(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "internal", "parser"), 0o755)
	os.MkdirAll(filepath.Join(root, "a"), 0o755)
	os.MkdirAll(filepath.Join(root, "b"), 0o755)
	os.WriteFile(filepath.Join(root, "internal", "parser", "duration.go"), []byte("package parser\n"), 0o644)
	os.WriteFile(filepath.Join(root, "a", "dup.txt"), []byte("1"), 0o644)
	os.WriteFile(filepath.Join(root, "b", "dup.txt"), []byte("2"), 0o644)
	os.WriteFile(filepath.Join(root, "README.md"), []byte("x"), 0o644)
	if rel, ok := brBareFile(root, "duration.go"); !ok || filepath.ToSlash(rel) != "internal/parser/duration.go" {
		t.Errorf("nome único devia resolver: %q %v", rel, ok)
	}
	for _, n := range []string{"dup.txt", "nao-existe.go", "README.md", "internal/parser/duration.go", ""} {
		if rel, ok := brBareFile(root, n); ok {
			t.Errorf("%q não devia resolver (ambíguo, inexistente, já na raiz ou com caminho): %q", n, rel)
		}
	}
}
