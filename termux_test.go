package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestIsTermuxDetection(t *testing.T) {
	cases := []struct {
		ver, prefix string
		want        bool
	}{
		{"", "", false},
		{"0.118", "", true},
		{"", "/data/data/com.termux/files/usr", true},
		{"", "/data/user/0/com.termux/files/usr", true},
		{"", "/workspace/com.termux-prefix", false},
		{"", "/workspace/com.termux/files/other", false},
		{"", "/usr/local", false},
	}
	for _, c := range cases {
		t.Setenv("TERMUX_VERSION", c.ver)
		t.Setenv("PREFIX", c.prefix)
		if got := isTermux(); got != c.want {
			t.Errorf("TERMUX_VERSION=%q PREFIX=%q: got %v want %v", c.ver, c.prefix, got, c.want)
		}
	}
}

func TestTermuxTempDir(t *testing.T) {
	px := filepath.Join(t.TempDir(), "usr")
	t.Setenv("TERMUX_VERSION", "1")
	t.Setenv("PREFIX", px)
	t.Setenv("TMPDIR", "")
	termuxEnsureTmp()
	want := filepath.Join(px, "tmp")
	if os.TempDir() != want {
		t.Fatalf("TempDir = %q, want %q", os.TempDir(), want)
	}
	if fi, err := os.Stat(want); err != nil || !fi.IsDir() {
		t.Fatalf("temp dir not created: %v", err)
	}
	// an explicit TMPDIR wins
	other := t.TempDir()
	t.Setenv("TMPDIR", other)
	termuxEnsureTmp()
	if os.TempDir() != other || termuxTmpDir() != other {
		t.Fatalf("TMPDIR must win, got %q", os.TempDir())
	}
}

func TestFallbackShNeverHardcodedOnTermux(t *testing.T) {
	px := t.TempDir()
	t.Setenv("TERMUX_VERSION", "1")
	t.Setenv("PREFIX", px)
	t.Setenv("PATH", t.TempDir()) // no sh at all
	if got := fallbackShPath(); got != filepath.Join(px, "bin", "sh") {
		t.Fatalf("got %q", got)
	}
	// with sh on PATH, that one is used
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "sh"), []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("PATH", bin)
	if got := fallbackShPath(); got != filepath.Join(bin, "sh") {
		t.Fatalf("got %q", got)
	}
}

func TestTermuxPkgHintAndMissing(t *testing.T) {
	if h := termuxPkgHint([]string{"ripgrep", "fd-find"}); h != "pkg install ripgrep fd" {
		t.Fatal(h)
	}
	none := func(string) (string, error) { return "", exec.ErrNotFound }
	if m := strings.Join(termuxMissing(none), " "); m != "python git curl openssl" {
		t.Fatal(m)
	}
	t.Setenv("TERMUX_VERSION", "1")
	if h := installHint([]string{"fd-find"}); h != "pkg install fd" {
		t.Fatal(h)
	}
}

func TestTermuxLowPowerAndSandbox(t *testing.T) {
	t.Setenv("TERMUX_VERSION", "1")
	if b, _, why := sbProbeOS(); runtime.GOOS == "linux" && (b != "" || !strings.Contains(why, "Android")) {
		t.Fatalf("sandbox must be unavailable on Termux: %q %q", b, why)
	}
}

func TestInstallShSyntax(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	if out, err := exec.Command("sh", "-n", filepath.Join("scripts", "install.sh")).CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v\n%s", err, out)
	}
}

// install.sh with a faked Termux environment against a local release folder.
func TestInstallShTermuxDryRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh")
	}
	arch := runtime.GOARCH
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" || (arch != "amd64" && arch != "arm64" && arch != "386") {
		t.Skip("no release asset name for this platform")
	}
	asset := "jazz-android-arm64"
	bin := []byte("#!/bin/sh\necho jazz-fake 9.9.9\n")
	sum := sha256.Sum256(bin)
	files := map[string][]byte{
		asset:          bin,
		"manifest.txt": []byte("jazz-cli release manifest v1\nversion 9.9.9\n" + hex.EncodeToString(sum[:]) + "  " + asset + "\n"),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		b, ok := files[strings.TrimPrefix(q.URL.Path, "/")]
		if !ok {
			http.NotFound(w, q)
			return
		}
		w.Write(b)
	}))
	defer srv.Close()

	home := t.TempDir()
	prefix := filepath.Join(home, "fakeprefix")
	if err := os.MkdirAll(filepath.Join(prefix, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", filepath.Join("scripts", "install.sh"), "--system", "--base-url", srv.URL)
	cmd.Env = append(os.Environ(), "HOME="+home, "TERMUX_VERSION=1", "PREFIX="+prefix, "TMPDIR="+t.TempDir(),
		"PATH="+filepath.Join(prefix, "bin")+":"+os.Getenv("PATH"), "JAZZ_BIN_DIR=", "JAZZ_BASE_URL=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install.sh: %v\n%s", err, out)
	}
	got, err := os.ReadFile(filepath.Join(prefix, "bin", "jazz"))
	if err != nil || string(got) != string(bin) {
		t.Fatalf("binary not installed in $PREFIX/bin: %v\n%s", err, out)
	}
	s := string(out)
	if strings.Contains(s, ".local/bin") || strings.Contains(s, "sudo") || strings.Contains(s, "não está no PATH") {
		t.Fatalf("Termux install must not mention ~/.local/bin, sudo or PATH hint:\n%s", s)
	}
	if !strings.Contains(s, "Termux") {
		t.Fatalf("missing Termux note:\n%s", s)
	}
	if _, err := os.Stat(filepath.Join(home, ".local")); err == nil {
		t.Fatal("~/.local must not be created")
	}
}
