//go:build !windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAcPrefetchTerms(t *testing.T) {
	terms, paths := acPrefetchTerms("O `parseConfig` quebra quando chamo loadUserSettings em app/config.py; veja o erro \"connection refused by upstream\" e o GitHub.\n" +
		"Traceback:\n  File \"svc/worker.py\", line 88, in run\nTambém mexe em http_retry_count e cfg.server.port")
	want := []string{"parseConfig", "connection refused by upstream", "loadUserSettings", "http_retry_count", "cfg.server.port"}
	for _, w := range want {
		if !contains(terms, w) {
			t.Errorf("term %q missing from %q", w, terms)
		}
	}
	if contains(terms, "GitHub") {
		t.Error("stoplist word kept")
	}
	if len(terms) > acPrefetchMaxTerms {
		t.Errorf("%d terms", len(terms))
	}
	if !contains(paths, "svc/worker.py") || !contains(paths, "app/config.py") {
		t.Errorf("paths = %q", paths)
	}
	// A message with no identifiers yields nothing (terminal tasks).
	if terms, paths := acPrefetchTerms("instala o servidor e reinicia a máquina, por favor"); len(terms)+len(paths) != 0 {
		t.Errorf("plain prose gave %q %q", terms, paths)
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// acTestRg makes rg findable or skips: CI images and the Dell have it; a Mac
// with only Claude Code's bundled rg can point JAZZ_TEST_RG_DIR at a folder
// with an rg symlink.
func acTestRg(t *testing.T) {
	t.Helper()
	if d := os.Getenv("JAZZ_TEST_RG_DIR"); d != "" {
		t.Setenv("PATH", d+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg not installed")
	}
}

func TestAcPrefetch(t *testing.T) {
	acTestRg(t)
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "app"), 0o755)
	os.WriteFile(filepath.Join(root, "app", "config.py"), []byte("def parseConfig(path):\n    return load(path)\n"), 0o644)
	os.WriteFile(filepath.Join(root, "app", "use.py"), []byte("from app.config import parseConfig\nx = parseConfig('a')\n"), 0o644)
	// A vague term: in 60 files, 3 hits each → dropped.
	for i := 0; i < 60; i++ {
		os.WriteFile(filepath.Join(root, fmt.Sprintf("noise%02d.py", i)), []byte("common_helper()\ncommon_helper()\ncommon_helper()\n"), 0o644)
	}
	got := acPrefetch(context.Background(), root, "conserta `parseConfig` em app/config.py, e o common_helper também", 1000)
	if !strings.HasPrefix(got, "<system-reminder>Auto-grep (may be incomplete, verify before editing):\n") || !strings.HasSuffix(got, "</system-reminder>") {
		t.Fatalf("format:\n%s", got)
	}
	for _, s := range []string{"file exists: app/config.py", "app/config.py:1:def parseConfig(path):", "app/use.py:2:x = parseConfig('a')"} {
		if !strings.Contains(got, s) {
			t.Errorf("missing %q:\n%s", s, got)
		}
	}
	if strings.Contains(got, "common_helper") {
		t.Errorf("vague term (>40 hits) not dropped:\n%s", got)
	}
	if len(got) > 1000*7/2 {
		t.Errorf("over the 1000-token budget: %d bytes", len(got))
	}
	// Nothing found → nothing injected.
	if got := acPrefetch(context.Background(), root, "renomeia `totallyAbsentName`", 1000); got != "" {
		t.Errorf("no hits must inject nothing:\n%s", got)
	}
	// Hard per-file and total caps.
	for i := 0; i < 5; i++ {
		os.WriteFile(filepath.Join(root, fmt.Sprintf("many%d.go", i)), []byte("spreadName()\nspreadName()\nspreadName()\nspreadName()\n"), 0o644)
	}
	got = acPrefetch(context.Background(), root, "onde está spreadName?", 1000)
	if n := strings.Count(got, "spreadName"); n == 0 || n > acPrefetchMaxHits {
		t.Errorf("%d hits:\n%s", n, got)
	}
	for i := 0; i < 5; i++ {
		if n := strings.Count(got, fmt.Sprintf("many%d.go:", i)); n > acPrefetchPerFile {
			t.Errorf("many%d.go: %d hits (max %d)", i, n, acPrefetchPerFile)
		}
	}
}

func TestAcPrefetchNoteGating(t *testing.T) {
	prev := opSetSettings(opDefaultSettings())
	t.Cleanup(func() { opSetSettings(prev) })
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("func parseConfig() {}\n"), 0o644)
	a := &Agent{tools: &Toolbox{Root: root}}
	if got := a.acPrefetchNote(context.Background(), "fix `parseConfig`"); got != "" {
		t.Errorf("default must be off, got %q", got)
	}
	s := opDefaultSettings()
	s.Prefetch = "grep"
	opSetSettings(s)
	acTestRg(t)
	if got := a.acPrefetchNote(context.Background(), "fix `parseConfig`"); !strings.Contains(got, "a.go:1:func parseConfig() {}") {
		t.Errorf("first turn: %q", got)
	}
	a.Msgs = []Message{{Role: "user", Content: "earlier"}}
	if got := a.acPrefetchNote(context.Background(), "fix `parseConfig`"); got != "" {
		t.Errorf("later turns must not prefetch: %q", got)
	}
	sub := &Agent{tools: a.tools, sub: true}
	if got := sub.acPrefetchNote(context.Background(), "fix `parseConfig`"); got != "" {
		t.Errorf("subagents must not prefetch: %q", got)
	}
}
