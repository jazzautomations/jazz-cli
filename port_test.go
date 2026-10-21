package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Platform-neutral tests of the Windows port helpers.

func TestWinThemeFallsBackToASCIIWithoutVT(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if th := winTheme(false, env(nil)); th.color || !th.ascii {
		t.Errorf("no VT: %+v", th)
	}
	if th := winTheme(true, env(map[string]string{"WT_SESSION": "x"})); !th.color || !th.truecolor || th.ascii {
		t.Errorf("Windows Terminal: %+v", th)
	}
	if th := winTheme(true, env(nil)); !th.color || th.truecolor || !th.ascii {
		t.Errorf("plain ConHost with VT: %+v", th)
	}
	if th := winTheme(true, env(map[string]string{"NO_COLOR": "1", "WT_SESSION": "x"})); th.color {
		t.Errorf("NO_COLOR: %+v", th)
	}
	if th := winTheme(true, env(map[string]string{"JAZZ_ASCII": "0"})); th.ascii {
		t.Errorf("JAZZ_ASCII=0 forces Unicode with VT: %+v", th)
	}
	if th := winTheme(false, env(map[string]string{"JAZZ_ASCII": "0"})); !th.ascii {
		t.Errorf("JAZZ_ASCII=0 cannot force Unicode without VT: %+v", th)
	}
}

func TestPfAssetNameFor(t *testing.T) {
	cases := map[[2]string]string{
		{"linux", "amd64"}:   "jazz-linux-amd64",
		{"darwin", "arm64"}:  "jazz-darwin-arm64",
		{"windows", "amd64"}: "jazz-windows-amd64.exe",
		{"windows", "arm64"}: "jazz-windows-arm64.exe",
	}
	for in, want := range cases {
		if got := pfAssetNameFor(in[0], in[1]); got != want {
			t.Errorf("pfAssetNameFor(%v) = %q want %q", in, got, want)
		}
	}
}

func TestPfReplaceExe(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "jazz.exe")
	tmp := filepath.Join(dir, ".jazz-update-1")
	os.WriteFile(exe, []byte("old"), 0o755)
	os.WriteFile(tmp, []byte("new"), 0o755)
	if err := pfReplaceExe(tmp, exe); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new" {
		t.Fatalf("exe = %q", b)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf("tmp should be gone: %v", err)
	}
}
