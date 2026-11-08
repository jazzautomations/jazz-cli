package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Enum fan-out: "dirs X and Y" must create ALL named dirs — the FALSE_SUCCESS
// class Jev flagged (nb-0649 created only `mnt` of `mnt and point`).
func TestEnumTargetsParse(t *testing.T) {
	w := &nuWorld{}
	for _, tc := range []struct {
		text string
		want string
	}{
		{`make directories mnt and point`, "mnt|point"},
		{`Make directories "mnt" and "point"`, "mnt|point"},
		{`create dirs alpha beta and gamma`, "alpha|beta|gamma"},
		{`cria as pastas logs e tmp`, "logs|tmp"},
	} {
		in := nuGet().Parse(tc.text, w)
		if in.Name != "create" {
			t.Fatalf("%q -> %s, quero create", tc.text, in.Name)
		}
		if in.Slots["targets"] != tc.want {
			t.Fatalf("%q -> targets=%q, quero %q (slots=%v)", tc.text, in.Slots["targets"], tc.want, in.Slots)
		}
	}
}

// Single name never fans out.
func TestEnumSingleStaysTarget(t *testing.T) {
	in := nuGet().Parse("make a directory backup", &nuWorld{})
	if in.Slots["targets"] != "" {
		t.Fatalf("alvo único virou enum: %v", in.Slots)
	}
}

// Run-level: both dirs on disk, verified — never partial green.
func TestEnumDirsAllCreated(t *testing.T) {
	root := brFixture(t)
	env, _ := brTestEnv(t, root, nil, 0)
	r := brRun(context.Background(), env, "make directories mnt and point")
	if r.Status != tkSucceeded {
		t.Fatalf("status=%s\n%s", r.Status, r.Text)
	}
	for _, d := range []string{"mnt", "point"} {
		if st, err := os.Stat(filepath.Join(root, d)); err != nil || !st.IsDir() {
			t.Fatalf("%s não existe: %v", d, err)
		}
	}
}

// Every unsafe name rejected by enum → nothing to do → not succeeded.
func TestEnumUnsafeNamesRefused(t *testing.T) {
	root := brFixture(t)
	env, _ := brTestEnv(t, root, nil, 0)
	r := brRun(context.Background(), env, `make directories "../evil" and "/abs"`)
	if r.Status == tkSucceeded {
		t.Fatalf("nomes inseguros não podem sair verdes\n%s", r.Text)
	}
}

// "in every directory" = fan-out inside each parent — verified per child.
func TestEnumEveryDirRun(t *testing.T) {
	root := brFixture(t)
	for _, d := range []string{"pa", "pb", "pc"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env, _ := brTestEnv(t, root, nil, 0)
	r := brRun(context.Background(), env, "make a new directory new-dir in every directory in the current directory")
	if r.Status != tkSucceeded {
		t.Fatalf("status=%s\n%s", r.Status, r.Text)
	}
	for _, d := range []string{"pa", "pb", "pc"} {
		if st, err := os.Stat(filepath.Join(root, d, "new-dir")); err != nil || !st.IsDir() {
			t.Fatalf("%s/new-dir não existe: %v", d, err)
		}
	}
	// o falso-sucesso clássico: nada de new-dir solto no root
	if _, err := os.Stat(filepath.Join(root, "new-dir")); err == nil {
		t.Fatal("new-dir criado no root — ação errada pintada de verde")
	}
}

// Empty scope → honest block, never green on nothing.
func TestEnumEveryDirEmptyBlocked(t *testing.T) {
	root := brFixture(t)
	env, _ := brTestEnv(t, root, nil, 0)
	r := brRun(context.Background(), env, "make a new directory nd in every directory inside zzz-nonexistent")
	if r.Status == tkSucceeded {
		t.Fatalf("conjunto vazio não pode sair verde\n%s", r.Text)
	}
}
