//go:build !windows

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAskStdinCLIChild(t *testing.T) {
	if os.Getenv("JAZZ_TEST_ASK_STDIN_CHILD") != "1" {
		t.Skip("CLI child")
	}
	os.Args = []string{"jazz", "-p", "--offline", "--ask-stdin", "compara diretórios"}
	main()
}

func TestAskStdinReservesAnswersForQuestions(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 required")
	}
	root := t.TempDir()
	for _, dir := range []string{"a", "b"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, dir, "x.txt"), []byte(dir), 0600); err != nil {
			t.Fatal(err)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestAskStdinCLIChild$")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "JAZZ_TEST_ASK_STDIN_CHILD=1", "JAZZ_HOME="+t.TempDir())
	cmd.Stdin = strings.NewReader("a\nb\n")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "x.txt") || !strings.Contains(string(out), "primeiro diretório") || !strings.Contains(string(out), "segundo diretório") {
		t.Fatalf("stdin answers became prompt text: err=%v output=%s", err, out)
	}
}
