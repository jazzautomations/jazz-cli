package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestAskFromFacts(t *testing.T) {
	root := t.TempDir()
	write := func(p, s string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module x\n")
	write("main.go", "package main\n")
	git := exec.Command("git", "init", "-q", "-b", "trabalho", root)
	if err := git.Run(); err != nil {
		t.Skip("git:", err)
	}
	yes := true
	no := false
	cases := []struct {
		q    askArgs
		want string
		ok   bool
	}{
		{askArgs{Question: "Em qual branch eu trabalho?"}, "trabalho", true},
		{askArgs{Question: "Qual branch?", Options: []askOpt{{Label: "main"}, {Label: "trabalho"}}}, "trabalho", true},
		{askArgs{Question: "Qual o comando de teste?"}, "go test ./...", true},
		{askArgs{Question: "Como rodar os testes?", Options: []askOpt{{Label: "go test ./..."}, {Label: "make test"}}, Free: &no}, "go test ./...", true},
		{askArgs{Question: "O arquivo main.go existe?", Options: []askOpt{{Label: "Sim"}, {Label: "Não"}}}, "Sim", true},
		{askArgs{Question: "Já tem um docs/README.md?"}, "não, docs/README.md não existe", true},
		// preferences and intent still go to the user
		{askArgs{Question: "Qual banco você prefere?", Options: []askOpt{{Label: "SQLite"}, {Label: "Postgres"}}}, "", false},
		{askArgs{Question: "Quer que eu crie uma branch nova?"}, "", false},
		{askArgs{Question: "Devo rodar os testes agora?"}, "", false},
		// options that do not name the fact, no free text: ask
		{askArgs{Question: "Qual branch?", Options: []askOpt{{Label: "main"}, {Label: "dev"}}, Free: &no}, "", false},
		{askArgs{Question: "Qual branch?", Options: []askOpt{{Label: "main"}, {Label: "dev"}}, Free: &yes}, "trabalho", true},
	}
	for _, c := range cases {
		f, ok := askFromFacts(root, c.q)
		if ok != c.ok || ok && f.answer != c.want {
			t.Errorf("%q: got (%q, %v) want (%q, %v)", c.q.Question, f.answer, ok, c.want, c.ok)
		}
	}
	// package manager from the lockfile
	write("package.json", `{"scripts":{"test":"vitest run"}}`)
	write("pnpm-lock.yaml", "")
	if f, ok := askFromFacts(root, askArgs{Question: "Qual o test runner?"}); !ok || f.answer != "pnpm test" {
		t.Errorf("js test runner: %q %v", f.answer, ok)
	}
	if f, ok := askFromFacts(root, askArgs{Question: "npm ou pnpm?", Options: []askOpt{{Label: "npm"}, {Label: "pnpm"}}}); !ok || f.answer != "pnpm" {
		t.Errorf("package manager: %q %v", f.answer, ok)
	}
}
