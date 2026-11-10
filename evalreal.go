package main

// evalreal.go: `jazz eval real` — a sonda de pedidos do dia a dia
// (docs/research/offline-realworld-probe.md) como suíte com veredito por
// pedido. Diferenças para `jazz eval offline`:
//
//   - os pedidos foram escritos sem olhar a gramática, como um dev digita;
//   - roda como `jazz -p --offline --allow-tools bash,edit,write`: escrita é
//     permitida (menos o que a intervenção mínima barra), então um pedido que
//     age errado estraga o projeto de verdade e a suíte vê;
//   - todo pedido que não declara "changes" tem de deixar árvore, HEAD,
//     branches e stash idênticos: leitura nunca muda nada;
//   - "after" confere o estado depois (a branch existe? o arquivo sumiu?), não
//     o texto da resposta.
//
// O projeto (fixture "loja") é uma API Go pequena: 4 commits, branch
// feature/preco, um arquivo modificado, um não rastreado, um teste que falha
// de propósito, .env, CSV, JSON e log.

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed evals/realworld/tasks.json
var erTasksJSON []byte

const erHistorico = "linha de histórico do projeto para o arquivo passar de um kilobyte com folga\n"

func erFiles() map[string]string {
	return map[string]string{
		"go.mod":              "module example.com/loja\n\ngo 1.21\n",
		"Makefile":            "build:\n\tgo build ./...\ntest:\n\tgo test ./...\n",
		"README.md":           "# Loja\n\nAPI de pedidos. Rode com `go run ./cmd/api`.\n\n## Testes\n\n`go test ./...`\n",
		".env":                "PORT=8080\nDB_URL=postgres://localhost/loja\nAPI_TOKEN=abc123segredo\n",
		".env.example":        "PORT=\nDB_URL=\nAPI_TOKEN=\nSENTRY_DSN=\n",
		"config.json":         "{\"porta\": 8080, \"debug\": true, \"db\": {\"host\": \"localhost\", \"pool\": 5}}\n",
		"data/pedidos.csv":    "id,nome,total\n1,ana,120\n2,bia,80\n",
		"docs/arquitetura.md": "# Arquitetura\n\n## API\n\n## Banco\n",
		"docs/historico.md":   "# Histórico\n\n" + strings.Repeat(erHistorico, 24),
		"scripts/build.sh":    "#!/bin/sh\nset -e\ngo build ./...\n",
		"logs/app.log": "2026-11-08 10:00:01 INFO start\n" +
			"2026-11-08 10:00:05 ERROR db timeout host=10.0.0.5\n" +
			"2026-11-08 10:02:11 WARN retry\n" +
			"2026-11-08 10:03:40 ERROR panic: nil pointer em store.go:12\n" +
			"2026-11-08 10:04:00 INFO ok contato ana@exemplo.com\n" +
			"2026-11-08 10:05:00 INFO fila vazia\n" +
			"2026-11-08 10:06:00 INFO shutdown\n",
		"cmd/api/main.go": "package main\n\nimport (\n\t\"fmt\"\n\t\"net/http\"\n\n\t\"example.com/loja/internal/parser\"\n\t\"example.com/loja/internal/store\"\n)\n\n" +
			"// TODO: ler a porta do ambiente\nfunc main() {\n" +
			"\thttp.HandleFunc(\"/health\", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, \"ok\") })\n" +
			"\tfmt.Println(parser.FormatDuration(90), store.Soma(nil))\n" +
			"\thttp.ListenAndServe(\":8080\", nil)\n}\n",
		"internal/parser/duration.go": "package parser\n\nimport \"fmt\"\n\n// FormatDuration formats seconds as text.\nfunc FormatDuration(s int) string {\n" +
			"\t// FIXME: acima de 60 devia sair em minutos\n\treturn fmt.Sprintf(\"%ds\", s)\n}\n\n" +
			"func ParsePrice(s string) (int, error) {\n\tvar v int\n\t_, err := fmt.Sscanf(s, \"R$%d\", &v)\n\treturn v, err\n}\n",
		"internal/parser/duration_test.go": "package parser\n\nimport \"testing\"\n\nfunc TestFormatDuration(t *testing.T) {\n" +
			"\tif got := FormatDuration(90); got != \"1m30s\" {\n\t\tt.Fatalf(\"got %q, want 1m30s\", got)\n\t}\n}\n\n" +
			"func TestParsePrice(t *testing.T) {\n\tif v, err := ParsePrice(\"R$42\"); err != nil || v != 42 {\n\t\tt.Fatalf(\"v=%d err=%v\", v, err)\n\t}\n}\n",
		"internal/store/store.go": "package store\n\ntype Pedido struct {\n\tID    int\n\tTotal int\n}\n\nfunc Soma(ps []Pedido) int {\n\tt := 0\n" +
			"\tfor _, p := range ps {\n\t\tt += p.Total\n\t}\n\treturn t\n}\n",
	}
}

// erFixture builds the "loja" project at root.
func erFixture(root string) error {
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("a suíte real precisa de git")
	}
	write := func(rel, content string, appendTo bool) error {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return err
		}
		flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
		if appendTo {
			flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
		}
		f, err := os.OpenFile(abs, flag, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = f.WriteString(content)
		return err
	}
	for rel, c := range erFiles() {
		if err := write(rel, c, false); err != nil {
			return err
		}
	}
	day := 1
	git := func(args ...string) error {
		c := exec.Command("git", args...)
		c.Dir = root
		date := fmt.Sprintf("2026-11-0%dT12:00:00Z", day)
		c.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date,
			"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
		if out, err := c.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %v %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	commit := func(msg string) error {
		if err := git("add", "-A"); err != nil {
			return err
		}
		err := git("commit", "-q", "-m", msg)
		day++
		return err
	}
	steps := []func() error{
		func() error { return git("init", "-q") },
		func() error { return git("symbolic-ref", "HEAD", "refs/heads/main") },
		func() error { return git("config", "user.name", "Bia Teste") },
		func() error { return git("config", "user.email", "bia@example.invalid") },
		func() error { return git("config", "commit.gpgsign", "false") },
		func() error { return commit("primeiro commit") },
		func() error { return write("data/pedidos.csv", "3,caio,310\n", true) },
		func() error { return commit("dados: pedido do caio") },
		func() error { return write("docs/arquitetura.md", "\n### Migrations\n", true) },
		func() error { return commit("docs: migrations") },
		func() error { return write("internal/store/store.go", "// mudança local\n", true) },
		func() error { return commit("store: comentário") },
		func() error { return git("checkout", "-q", "-b", "feature/preco") },
		func() error { return write("cmd/api/main.go", "// wip\n", true) },
		func() error { return write("notas.txt", "rascunho\n", false) },
	}
	for _, s := range steps {
		if err := s(); err != nil {
			return err
		}
	}
	return nil
}

func erGit(root string, args ...string) string {
	c := exec.Command("git", args...)
	c.Dir = root
	out, _ := c.CombinedOutput()
	return strings.TrimSpace(string(out))
}

// erSnapshot is everything a read request must leave untouched.
func erSnapshot(root string) string {
	return "HEAD " + erGit(root, "rev-parse", "HEAD") +
		"\nramo " + erGit(root, "branch", "--show-current") +
		"\nramos " + strings.Join(strings.Fields(erGit(root, "branch", "--format=%(refname:short)")), ",") +
		"\nstash " + erGit(root, "stash", "list") +
		"\nárvore " + strings.ReplaceAll(erGit(root, "status", "--porcelain", "--untracked-files=all"), "\n", " | ") +
		"\ndiff " + erGit(root, "diff", "--stat")
}

// erSnapDiff names the first snapshot line that changed.
func erSnapDiff(a, b string) string {
	la, lb := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := range la {
		if i < len(lb) && la[i] != lb[i] {
			return oneLineKeep(la[i]+" → "+lb[i], 200)
		}
	}
	return "estado diferente"
}

// erCheck runs one "after" check in the fixture.
func erCheck(root, cmd string) (string, error) {
	c := exec.Command("sh", "-c", cmd)
	c.Dir = root
	out, err := c.CombinedOutput()
	return string(out), err
}

// erLocalCmd: a write command that only touches the fixture project.
func erLocalCmd(cmd string) bool {
	for _, p := range []string{"git ", "go ", "gofmt ", "python3 ", "make ", "write ", "mv ", "mkdir ", "rm ", "cp ", "touch "} {
		if strings.HasPrefix(cmd, p) {
			return !strings.HasPrefix(cmd, "git push") && !strings.HasPrefix(cmd, "git pull") && !strings.HasPrefix(cmd, "git fetch")
		}
	}
	return false
}

// erParse prints how the offline door reads a request, without acting: the
// fast path that would fire, or the grammar's intent with slots and
// confidence, and the first method that would run. It is the lens for the
// test→fix→retest loop on the real suite.
func erParse(root, text string, w io.Writer) int {
	det := fpDetect(root)
	if m, ok := fpIntents(root).Match(text, det, true); ok {
		fmt.Fprintf(w, "atalho %s → %s\n", m.Intent, m.Cmd)
		return 0
	}
	prof := epGetLight()
	in := nuGet().Parse(text, nuRuntimeWorld(root, prof))
	in.Ents = nil
	b, _ := json.Marshal(in)
	fmt.Fprintln(w, string(b))
	env := &brEnv{Root: root, Profile: prof}
	if m, ok := brFirstMethod(env, in); ok {
		fmt.Fprintf(w, "método %s (risco %s) · age a partir de %.2f\n", m.Name, m.Risk, brActMin(in))
	}
	return 0
}
