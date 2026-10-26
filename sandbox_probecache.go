package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Cached verdict of the sandbox probe (low-level study §11.2, item 6).
//
// sbProbeOS runs `bwrap ... /bin/true` (or `sandbox-exec ... true`) on every
// start: a fork+exec, new user and pid namespaces, mounts and another exec —
// tens of milliseconds on the reference Pentium M, for an answer that only
// changes when the binary, the kernel or the user-namespace settings do.
// Only a positive verdict is cached ("the backend works"), keyed by all of
// those; any change, a missing or unreadable cache, or an entry older than
// sbProbeMaxAge probes again. A negative verdict is never cached, so a
// machine that gains a working sandbox uses it on the next start, and the
// cache can never turn the sandbox off.

const (
	sbProbeCacheV = "v1"
	sbProbeMaxAge = 7 * 24 * time.Hour
)

func sbProbeCachePath() string { return filepath.Join(dataDir(), "sandbox-probe") }

// sbProbeCached is sbProbeOS behind the verdict cache.
func sbProbeCached() (backend, bin, why string) {
	name := ""
	switch runtime.GOOS {
	case "linux":
		if isTermux() {
			return sbProbeOS()
		}
		name = "bwrap"
	case "darwin":
		name = "sandbox-exec"
	default:
		return sbProbeOS() // windows: no child probe to save
	}
	p, err := exec.LookPath(name)
	if err != nil && runtime.GOOS == "darwin" {
		p = "/usr/bin/sandbox-exec"
	}
	key := sbProbeKey(p)
	if key != "" {
		if b, bn, ok := sbProbeCacheRead(key); ok {
			return b, bn, ""
		}
	}
	backend, bin, why = sbProbeOS()
	if backend != "" && key != "" && bin == p {
		sbProbeCacheWrite(key, backend, bin)
	}
	return backend, bin, why
}

// sbProbeKey hashes what the verdict depends on: the backend binary (path
// and content), the kernel release and the user-namespace knobs (Linux) or
// the OS version (macOS), and the uid. "" = cannot key (do not cache).
func sbProbeKey(bin string) string {
	if bin == "" {
		return ""
	}
	f, err := os.Open(bin)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%d\x00", sbProbeCacheV, bin, os.Getuid())
	if _, err := io.Copy(h, io.LimitReader(f, 64<<20)); err != nil {
		return ""
	}
	var env []string
	switch runtime.GOOS {
	case "linux":
		env = []string{"/proc/sys/kernel/osrelease", "/proc/sys/kernel/unprivileged_userns_clone",
			"/proc/sys/user/max_user_namespaces", "/proc/sys/kernel/apparmor_restrict_unprivileged_userns"}
	case "darwin":
		env = []string{"/System/Library/CoreServices/SystemVersion.plist"}
	}
	for _, p := range env {
		b, err := os.ReadFile(p)
		if err != nil {
			b = []byte("<absent>")
		}
		fmt.Fprintf(h, "\x00%s=%s", p, strings.TrimSpace(string(b)))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// sbProbeCacheRead: the cached backend and binary for key, if fresh.
func sbProbeCacheRead(key string) (backend, bin string, ok bool) {
	b, err := os.ReadFile(sbProbeCachePath())
	if err != nil || len(b) > 4096 {
		return "", "", false
	}
	f := strings.Split(strings.TrimSpace(string(b)), "\t")
	if len(f) != 5 || f[0] != sbProbeCacheV || f[1] != key {
		return "", "", false
	}
	ts, err := strconv.ParseInt(f[4], 10, 64)
	if err != nil || time.Since(time.Unix(ts, 0)) > sbProbeMaxAge || time.Unix(ts, 0).After(time.Now().Add(time.Hour)) {
		return "", "", false
	}
	if f[2] != sbBwrap && f[2] != sbSeatbelt {
		return "", "", false
	}
	return f[2], f[3], true
}

func sbProbeCacheWrite(key, backend, bin string) {
	p := sbProbeCachePath()
	if os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return
	}
	line := strings.Join([]string{sbProbeCacheV, key, backend, bin, strconv.FormatInt(time.Now().Unix(), 10)}, "\t") + "\n"
	tmp := p + ".tmp"
	if os.WriteFile(tmp, []byte(line), 0o600) == nil && os.Rename(tmp, p) != nil {
		os.Remove(tmp)
	}
}

// sbProbeForget drops the cached verdict (jazz doctor probes fresh).
func sbProbeForget() { os.Remove(sbProbeCachePath()) }
