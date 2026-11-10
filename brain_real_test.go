package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func brRealWorld() *nuWorld {
	return nuWorldFromDef(nuWorldDef{
		Files: []string{"README.md", "go.mod", "config.json", "data/pedidos.csv", "logs/app.log", "cmd/api/main.go",
			"internal/parser/duration.go", "internal/store/store.go", "web/handler.go", "api/handler.go"},
		Dirs: []string{"data", "logs", "cmd", "internal", "docs"},
	})
}

// Uma frase feita lê o pedido inteiro e resolve cada operando no projeto.
func TestPhraseReadings(t *testing.T) {
	w := brRealWorld()
	for _, c := range []struct {
		text, intent string
		slots        map[string]string
	}{
		{"mostra as 5 primeiras linhas do logs/app.log", "line_slice", map[string]string{"file": "logs/app.log", "start": "1", "end": "5"}},
		{"first 3 lines of app.log", "line_slice", map[string]string{"file": "logs/app.log", "end": "3"}},
		{"as 2 últimas linhas do app.log", "line_slice", map[string]string{"file": "logs/app.log", "tail": "2"}},
		{"converte data/pedidos.csv pra json", "csv_to_json", map[string]string{"target": "data/pedidos.csv"}},
		{"onde a função Soma é usada?", "symbol_refs", map[string]string{"symbol": "Soma"}},
		{"quem chama FormatDuration", "symbol_refs", map[string]string{"symbol": "FormatDuration"}},
		{"where is ParsePrice used", "symbol_refs", map[string]string{"symbol": "ParsePrice"}},
		{"acha a palavra timeout nos logs", "text_grep", map[string]string{"pattern": "timeout", "file": "logs/app.log"}},
		{"procura porta no config.json", "text_grep", map[string]string{"pattern": "porta", "file": "config.json"}},
		{"mostra o esquema do config.json", "json_schema_infer", map[string]string{"target": "config.json"}},
		{"soma a coluna total do pedidos.csv", "csv_sum", map[string]string{"col": "total", "file": "data/pedidos.csv"}},
		{"que horas são?", "date_show", nil},
		{"list all go files", "list_files", map[string]string{"ext": "go"}},
		{"lista os arquivos python", "list_files", map[string]string{"ext": "py"}},
		{"lista os arquivos go e conta as linhas", "wordcount_stats", map[string]string{"ext": "go", "what": "lines"}},
		{"cria uma branch chamada teste", "git_branch", map[string]string{"name": "teste", "op": "create"}},
		{"git checkout -b fix/preco", "git_branch", map[string]string{"name": "fix/preco", "op": "create"}},
		{"guarda minhas mudanças no stash", "git_stash", map[string]string{"op": "push"}},
		{"git stash", "git_stash", map[string]string{"op": "push"}},
		{"desfaz as mudanças do main.go", "undo", map[string]string{"file": "cmd/api/main.go"}},
		{"mostra o API_TOKEN", "env_get", map[string]string{"name": "API_TOKEN"}},
		{"mostra os erros do logs/app.log", "log_errors_recent", map[string]string{"file": "logs/app.log"}},
	} {
		in, ok := nuPhraseParse(c.text, w)
		if !ok || in.Name != c.intent {
			t.Errorf("%q: frase não leu como %s (ok=%v, veio %s)", c.text, c.intent, ok, in.Name)
			continue
		}
		for k, v := range c.slots {
			if in.Slots[k] != v {
				t.Errorf("%q: slot %s=%q, esperado %q (slots %v)", c.text, k, in.Slots[k], v, in.Slots)
			}
		}
		if !in.Phrase || in.Confidence < brLevel0Min {
			t.Errorf("%q: frase feita tem de vir marcada e confiante: %+v", c.text, in)
		}
	}
}

// Uma frase nunca adivinha: operando que não resolve = a frase não se aplica.
func TestPhraseNeverGuesses(t *testing.T) {
	w := brRealWorld()
	for _, text := range []string{
		"mostra as 5 primeiras linhas do naoexiste.log", // arquivo não existe
		"desfaz as mudanças do handler.go",              // dois handler.go: ambíguo
		"desfaz as mudanças",                            // sem arquivo: é o /undo, não o git
		"onde isso é usado",                             // pronome não é símbolo
		"cria uma branch nova",                          // "nova" não é nome
		"converte o README.md pra json",                 // não é csv
		"mostra o TODO",                                 // não é variável de ambiente
		"mostra o README",                               // idem
		"procura timeout no projeto",                    // projeto inteiro é do atalho de busca
		"soma a coluna total",                           // sem arquivo
		"lista os arquivos",                             // sem extensão: a gramática decide
	} {
		if in, ok := nuPhraseParse(text, w); ok {
			t.Errorf("%q: frase feita adivinhou %s %v", text, in.Name, in.Slots)
		}
	}
}

// Nome de arquivo com erro de digitação: só conserta com UM candidato, e diz.
func TestPhraseRepairsFileTypo(t *testing.T) {
	w := brRealWorld()
	in, ok := nuPhraseRepair("abre o reamde", w)
	if !ok || in.Name != "open_file" || in.Slots["target"] != "README.md" {
		t.Fatalf("abre o reamde: %v %+v", ok, in)
	}
	if len(in.Evidence) == 0 || !strings.Contains(in.Evidence[0], "README.md") {
		t.Errorf("o conserto tem de ser dito: %v", in.Evidence)
	}
	if in.Confidence >= brLevel0Min {
		t.Errorf("conserto de digitação não pode valer como leitura confiante: %.2f", in.Confidence)
	}
	if in, ok := nuPhraseRepair("abre o handlr.go", w); ok {
		t.Errorf("dois candidatos (web/api handler.go) não é conserto: %+v", in)
	}
	if in, ok := nuPhraseRepair("apaga o reamde", w); ok {
		t.Errorf("conserto de digitação nunca sustenta escrita: %+v", in)
	}
}

// "limpa o projeto" não é `docker image prune -af` nem lint (regressão da suíte
// real: a regra de prune era a palavra "limpa" sozinha).
func TestLimpaIsNotPruneNorLint(t *testing.T) {
	m, w := nuTestWorld(t)
	for _, text := range []string{"limpa o projeto", "limpa a tela", "limpa o cache", "clean the project", "limpa isso"} {
		if in := m.Parse(text, w); in.Name == "container_prune" && in.Understood() {
			t.Errorf("%q lido como container_prune %.2f", text, in.Confidence)
		}
		if fm, ok := fpIntents("").Match(text, fpProject{Lint: "go vet ./...", Test: "go test ./...", Build: "go build ./...", Git: true}, true); ok {
			t.Errorf("%q casou o atalho %s (%s)", text, fm.Intent, fm.Cmd)
		}
	}
	for _, text := range []string{"limpa imagens docker não usadas", "docker image prune", "limpa o docker"} {
		if in := m.Parse(text, w); in.Name != "container_prune" {
			t.Errorf("%q devia continuar container_prune, veio %s", text, in.Name)
		}
	}
}

// Um método que muda coisa e sai com exit ≠ 0 não é selado por "o comando respondeu".
func TestCmdRanNeedsExitZeroWhenMutating(t *testing.T) {
	cx := &brCtx{vars: map[string]any{"c": brCmdRes{"docker image prune -af", 1, "cannot connect"}}, task: &TaskState{}}
	high := mhMethod{Name: "x.write", Risk: "high", Verify: []mhCheck{{Check: "cmd_ran", Arg: "$c"}}}
	if ok, _, why := brVerify(cx, high); ok {
		t.Errorf("escrita com exit 1 selada: %s", why)
	}
	low := mhMethod{Name: "x.read", Risk: "low", Verify: []mhCheck{{Check: "cmd_ran", Arg: "$c"}}}
	if ok, _, why := brVerify(cx, low); !ok {
		t.Errorf("leitura informativa com exit 1 tem de continuar valendo: %s", why)
	}
}

// Escrita negada não cai no método de leitura irmão com selo verde.
func TestDeniedWriteNeverFallsBackToRead(t *testing.T) {
	env, rec := brHonestRepo(t)
	env.Run = func(ctx context.Context, cmd string) (string, int, bool) {
		rec.cmds = append(rec.cmds, cmd)
		if strings.HasPrefix(cmd, "git status") {
			return " M a.go\n", 0, false
		}
		return "", 0, false
	}
	env.Permit = func(ctx context.Context, cmd string) (bool, string) {
		if strings.HasPrefix(cmd, "git stash push") {
			return false, "negado no teste"
		}
		return true, ""
	}
	r := brRun(context.Background(), env, "guarda minhas mudanças no stash")
	if r.Status == tkSucceeded {
		t.Fatalf("stash negado selado como feito: %s", r.Text)
	}
	for _, c := range rec.cmds {
		if c == "git stash list" {
			t.Errorf("depois da escrita negada rodou a leitura irmã: %v", rec.cmds)
		}
	}
}

func brRealFiles(t *testing.T) *brEnv {
	t.Helper()
	root := t.TempDir()
	for p, c := range map[string]string{
		".git/HEAD":        "ref: refs/heads/main\n",
		"go.mod":           "module x\n\ngo 1.22\n",
		".env":             "PORT=8080\nAPI_TOKEN=abc123segredo\nDB_URL=postgres://u:senha@h/db\n",
		"data/pedidos.csv": "id,nome,total\n1,ana,120\n2,bia,80\n3,caio,310\n4,dan,\n",
		"logs/app.log":     "10:00 INFO start\n10:01 ERROR db timeout\n10:02 WARN retry\n10:03 ERROR panic: nil\n",
		"cmd/api/main.go":  "package main\n\nimport (\n\t\"fmt\"\n\t\"net/http\"\n)\n\n// TODO: porta\nfunc main() {\n\thttp.HandleFunc(\"/health\", nil)\n\tfmt.Println()\n}\n",
	} {
		abs := filepath.Join(root, filepath.FromSlash(p))
		os.MkdirAll(filepath.Dir(abs), 0o755)
		if err := os.WriteFile(abs, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env, _ := brTestEnv(t, root, map[string]brFakeOut{}, 0)
	env.Ask = nil
	return env
}

func TestRealCaps(t *testing.T) {
	t.Setenv("PORT", "")
	t.Setenv("API_TOKEN", "")
	for _, c := range []struct {
		text       string
		want, deny []string
	}{
		{"soma a coluna total do pedidos.csv", []string{"510", "3 linha(s)", "1 sem número"}, nil},
		{"qual o valor de PORT", []string{"PORT=8080", ".env"}, nil},
		{"mostra o API_TOKEN", []string{".env", "valor oculto"}, []string{"abc123segredo"}},
		{"mostra os erros do log", []string{"db timeout", "panic: nil", "2 linha(s) de erro"}, []string{"INFO start", "WARN retry"}},
		{"explica o que o main.go faz", []string{"cmd/api/main.go", "package main", "net/http", "/health", "TODO", "precisa de um modelo"}, nil},
	} {
		env := brRealFiles(t)
		r := brRun(context.Background(), env, c.text)
		if r.Status != tkSucceeded {
			t.Errorf("%q: status %s (%s %.2f): %s", c.text, r.Status, r.Intent.Name, r.Intent.Confidence, r.Text)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(r.Text, w) {
				t.Errorf("%q: falta %q em:\n%s", c.text, w, r.Text)
			}
		}
		for _, d := range c.deny {
			if strings.Contains(r.Text, d) {
				t.Errorf("%q: não devia ter %q em:\n%s", c.text, d, r.Text)
			}
		}
	}
	// credencial dentro de URL também fica oculta
	env := brRealFiles(t)
	t.Setenv("DB_URL", "")
	if r := brRun(context.Background(), env, "mostra o DB_URL"); strings.Contains(r.Text, "senha") {
		t.Errorf("URL com credencial impressa: %s", r.Text)
	}
}

// Depois de um passo que falha, só leitura independente continua; nada que
// muda coisa roda.
func TestMultiContinuesOnlyWithReads(t *testing.T) {
	env, _ := brHonestRepo(t)
	read := Intent{Name: "git_log", Confidence: 0.97}
	write := Intent{Name: "git_commit", Confidence: 0.97, Slots: map[string]string{"msg": "x"}}
	if !brRestAreReads(env, []Intent{read}, []bool{false}) {
		t.Error("leitura independente devia poder continuar")
	}
	if brRestAreReads(env, []Intent{write}, []bool{false}) {
		t.Error("commit não pode rodar depois de um passo que falhou")
	}
	if brRestAreReads(env, []Intent{read}, []bool{true}) {
		t.Error("passo encadeado no anterior não continua")
	}
	if brRestAreReads(env, nil, nil) {
		t.Error("sem resto não há o que continuar")
	}
}

func TestFmtFixAndCheckOnly(t *testing.T) {
	if fpFmtFix("gofmt -l .") != "gofmt -w ." || fpFmtFix("npm run format") != "" {
		t.Error("fpFmtFix")
	}
	for text, check := range map[string]bool{"formata o código": false, "format the code": false, "roda o gofmt": false,
		"confere a formatação": true, "verifica se tá formatado": true, "check formatting": true} {
		if got := brFmtCheckOnly.MatchString(text); got != check {
			t.Errorf("%q: só conferir=%v, esperado %v", text, got, check)
		}
	}
}

// A suíte real inteira, com a lista de falhas conhecidas (a mesma do CI).
func TestEvalRealGate(t *testing.T) {
	if testing.Short() || runtime.GOOS != "linux" {
		t.Skip("suíte real: linux, sem -short")
	}
	for _, bin := range []string{"git", "sh", "go", "python3"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip("suíte real precisa de " + bin)
		}
	}
	var out strings.Builder
	rep, err := eoRunSuite("real", nil, false, &out)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Summary.FalseSuccess != 0 {
		t.Errorf("falso sucesso na suíte real: %d\n%s", rep.Summary.FalseSuccess, out.String())
	}
	known, err := eoGateList("evals/realworld/known-failing.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range eoGateCheck(rep, known) {
		t.Error(m)
	}
}
