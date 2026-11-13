package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMmAppend(t *testing.T) {
	p := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(p, []byte("# Projeto\nUse go 1.22"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"rode os testes com -v", "nunca\nmexa em vendor/"} {
		if err := mmAppend(p, s); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(p)
	want := "# Projeto\nUse go 1.22\n\n## Notas\n- rode os testes com -v\n- nunca mexa em vendor/\n"
	if string(b) != want {
		t.Errorf("got %q", b)
	}
	if !strings.HasSuffix(mmProjectFile(filepath.Dir(p)), "AGENTS.md") {
		t.Error("existing AGENTS.md must be the project file")
	}
}

func TestMmProjectFilePrefersExistingClaudeMd(t *testing.T) {
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "CLAUDE.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := mmProjectFile(d); filepath.Base(got) != "CLAUDE.md" {
		t.Errorf("got %s", got)
	}
	if got := mmProjectFile(t.TempDir()); filepath.Base(got) != "AGENTS.md" {
		t.Errorf("new project file: %s", got)
	}
}
