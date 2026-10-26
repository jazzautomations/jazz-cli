package main

// Termux (Android, no root) support: detection, temp dir, shell fallback and
// the doctor lines. Termux has no /tmp, /bin/sh or /usr/bin/env at their
// usual places, no bubblewrap and no unprivileged user namespaces, so jazz
// resolves everything through PATH and $PREFIX and runs without an OS sandbox.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func init() { termuxEnsureTmp() }

// isTermux: TERMUX_VERSION is set, or PREFIX has Termux's conventional suffix.
func isTermux() bool {
	if os.Getenv("TERMUX_VERSION") != "" {
		return true
	}
	return strings.HasSuffix(filepath.ToSlash(os.Getenv("PREFIX")), "/com.termux/files/usr")
}

// termuxPrefix is $PREFIX, or the stock Termux prefix.
func termuxPrefix() string {
	if p := os.Getenv("PREFIX"); p != "" {
		return p
	}
	return "/data/data/com.termux/files/usr"
}

// termuxTmpDir is the temp dir on Termux: $TMPDIR, else $PREFIX/tmp.
func termuxTmpDir() string {
	if d := os.Getenv("TMPDIR"); d != "" {
		return d
	}
	return filepath.Join(termuxPrefix(), "tmp")
}

// termuxEnsureTmp makes os.TempDir() (which reads TMPDIR) point at a real
// directory on Termux, where /tmp does not exist.
func termuxEnsureTmp() {
	if !isTermux() {
		return
	}
	d := termuxTmpDir()
	if os.Getenv("TMPDIR") == "" {
		os.Setenv("TMPDIR", d)
	}
	_ = os.MkdirAll(d, 0o700)
}

// fallbackShPath is the plain sh used when bash is missing: PATH first, the
// literal /bin/sh only where nothing else is known (Termux has no /bin/sh).
func fallbackShPath() string {
	if p, err := exec.LookPath("sh"); err == nil {
		return p
	}
	if isTermux() {
		return filepath.Join(termuxPrefix(), "bin", "sh")
	}
	return "/bin/sh"
}

// termuxPkgHint is the install hint for missing tools (apt names -> pkg names).
func termuxPkgHint(names []string) string {
	fix := map[string]string{"ripgrep": "ripgrep", "fd-find": "fd"}
	out := make([]string, 0, len(names))
	for _, n := range names {
		if v, ok := fix[n]; ok {
			n = v
		}
		out = append(out, n)
	}
	return "pkg install " + strings.Join(out, " ")
}

// termuxMissing lists the Termux packages jazz relies on that are absent.
func termuxMissing(look func(string) (string, error)) []string {
	var miss []string
	for _, b := range []string{"python3", "git", "curl", "openssl"} {
		if _, err := look(b); err != nil {
			if b == "python3" {
				b = "python" // the Termux package name
			}
			miss = append(miss, b)
		}
	}
	return miss
}

// termuxDoctor prints the Termux lines of jazz doctor.
func termuxDoctor(ok func(bool) string, gray func(string) string) {
	if !isTermux() {
		return
	}
	fmt.Println("  Termux detectado · sandbox: indisponível no Android (proteções de software ativas) · modo padrão: auto")
	for _, b := range []string{"python3", "git", "curl", "openssl"} {
		_, err := exec.LookPath(b)
		fmt.Printf("  %s %s\n", ok(err == nil), b)
	}
	if m := termuxMissing(exec.LookPath); len(m) > 0 {
		fmt.Println("  " + gray("dica: "+termuxPkgHint(m)))
	}
	fmt.Println("  " + gray(fmt.Sprintf("temp: %s · %s/%s", os.TempDir(), runtime.GOOS, runtime.GOARCH)))
}

// installHint is the package-manager command for the missing tools.
func installHint(names []string) string {
	if isTermux() {
		return termuxPkgHint(names)
	}
	return "sudo apt install " + strings.Join(names, " ")
}
