package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// scripts/install.ps1 end to end against a local release folder: the pure
// PowerShell Ed25519 check must accept a real signature (made by Go's
// crypto/ed25519, like scripts/sign) and refuse a forged one or a wrong
// sha256. Needs PowerShell: always there on Windows, JAZZ_TEST_PWSH elsewhere.

type ps1Release struct {
	files map[string][]byte
}

func newPs1Release(t *testing.T, priv ed25519.PrivateKey, bin []byte, asset string) *ps1Release {
	t.Helper()
	sum := sha256.Sum256(bin)
	man := []byte("jazz-cli release manifest v1\nversion 9.9.9\n" + hex.EncodeToString(sum[:]) + "  " + asset + "\n")
	return &ps1Release{files: map[string][]byte{
		asset:              bin,
		"manifest.txt":     man,
		"manifest.txt.sig": []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, man)) + "\n"),
	}}
}

func (r *ps1Release) serve(t *testing.T) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		b, ok := r.files[strings.TrimPrefix(q.URL.Path, "/")]
		if !ok {
			http.NotFound(w, q)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// runInstallPs1 runs a copy of install.ps1 carrying pub ("" = no key).
func runInstallPs1(t *testing.T, pwsh, pub, base string, extraEnv ...string) (string, string, error) {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("scripts", "install.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?m)^\$JazzPubKey = .*$`)
	if !re.Match(src) {
		t.Fatal("install.ps1 has no $JazzPubKey line for release.sh to fill in")
	}
	src = re.ReplaceAll(src, []byte("$$JazzPubKey = '"+pub+"'"))
	dir := t.TempDir()
	script := filepath.Join(dir, "install.ps1")
	if err := os.WriteFile(script, src, 0o644); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(dir, "bin")
	cmd := exec.Command(pwsh, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script)
	cmd.Env = append(os.Environ(), "JAZZ_BASE_URL="+base, "JAZZ_BIN_DIR="+binDir, "JAZZ_ARCH=amd64", "JAZZ_NO_PATH=1", "JAZZ_REQUIRE_SIG=")
	cmd.Env = append(cmd.Env, extraEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), filepath.Join(binDir, "jazz.exe"), err
}

func TestInstallPs1VerifiesSignatureAndHash(t *testing.T) {
	shells := []string{testPwsh(t)}
	if runtime.GOOS == "windows" { // Windows PowerShell 5.1 too: what a fresh Windows has
		if p, err := exec.LookPath("powershell"); err == nil && p != shells[0] {
			shells = append(shells, p)
		}
	}
	for _, sh := range shells {
		t.Run(filepath.Base(sh), func(t *testing.T) { installPs1Cases(t, sh) })
	}
}

func installPs1Cases(t *testing.T, pwsh string) {
	pk, priv, _ := ed25519.GenerateKey(rand.Reader)
	pub := base64.StdEncoding.EncodeToString(pk)
	const asset = "jazz-windows-amd64.exe"
	bin := []byte("MZ fake jazz binary " + strings.Repeat("x", 4096))

	// good release: installed
	good := newPs1Release(t, priv, bin, asset)
	out, dest, err := runInstallPs1(t, pwsh, pub, good.serve(t))
	if err != nil || !strings.Contains(out, "assinatura ok") {
		t.Fatalf("good release refused: %v\n%s", err, out)
	}
	if b, _ := os.ReadFile(dest); string(b) != string(bin) {
		t.Fatalf("installed binary differs (%d bytes)", len(b))
	}

	// forged signature (another key): refused, nothing installed
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	forged := newPs1Release(t, priv, bin, asset)
	forged.files["manifest.txt.sig"] = []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(other, forged.files["manifest.txt"])))
	out, dest, err = runInstallPs1(t, pwsh, pub, forged.serve(t))
	if err == nil || !strings.Contains(out, "assinatura da release inválida") {
		t.Fatalf("forged signature accepted: %v\n%s", err, out)
	}
	if _, e := os.Stat(dest); e == nil {
		t.Fatal("binary installed despite a bad signature")
	}

	// tampered manifest (signature no longer matches): refused
	tm := newPs1Release(t, priv, bin, asset)
	tm.files["manifest.txt"] = append([]byte{}, tm.files["manifest.txt"]...)
	tm.files["manifest.txt"][len(tm.files["manifest.txt"])-2] ^= 1
	if out, _, err := runInstallPs1(t, pwsh, pub, tm.serve(t)); err == nil {
		t.Fatalf("tampered manifest accepted:\n%s", out)
	}

	// signed manifest, swapped binary: sha256 refused
	sw := newPs1Release(t, priv, bin, asset)
	sw.files[asset] = []byte("MZ evil")
	if out, _, err := runInstallPs1(t, pwsh, pub, sw.serve(t)); err == nil || !strings.Contains(out, "sha256") {
		t.Fatalf("swapped binary accepted: %v\n%s", err, out)
	}

	// installer without a key: warns, checks the sha256, installs; refuses with JAZZ_REQUIRE_SIG=1
	nk := newPs1Release(t, priv, bin, asset)
	if out, _, err := runInstallPs1(t, pwsh, "", nk.serve(t)); err != nil || !strings.Contains(out, "NÃO verificada") {
		t.Fatalf("keyless install: %v\n%s", err, out)
	}
	if out, _, err := runInstallPs1(t, pwsh, "", nk.serve(t), "JAZZ_REQUIRE_SIG=1"); err == nil {
		t.Fatalf("JAZZ_REQUIRE_SIG=1 without a key must fail:\n%s", out)
	}
}
