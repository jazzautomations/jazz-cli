//go:build !windows

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func pfResetDebug(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		pfDbg.mu.Lock()
		if pfDbg.f != nil {
			pfDbg.f.Close()
		}
		pfDbg.f, pfDbg.path, pfDbg.size, pfDbg.ring, pfDbg.sysSeen = nil, "", 0, nil, nil
		pfDbg.mu.Unlock()
		debugOn = false
	})
}

func TestDebugLogRedactsAndHashesSystem(t *testing.T) {
	pfTestHome(t)
	pfResetDebug(t)
	t.Setenv("OPENROUTER_API_KEY", "sk-or-v1-TESTKEY0000000000000000000000")
	refreshSecrets()
	dbg("request", map[string]any{"x": 1}) // off: no-op
	path, err := pfDebugStart()
	if err != nil {
		t.Fatal(err)
	}
	sys := strings.Repeat("You are Jazz. ", 40)
	body := map[string]any{"model": "m", "messages": []any{map[string]any{"role": "system", "content": sys}, map[string]any{"role": "user", "content": "key sk-or-v1-TESTKEY0000000000000000000000"}}}
	dbg("request", map[string]any{"url": "https://x", "headers": map[string]string{"Authorization": "Bearer abc"}, "body": body})
	dbg("request", map[string]any{"body": body})
	b, _ := os.ReadFile(path)
	s := string(b)
	if strings.Contains(s, "sk-or-v1-TESTKEY0000000000000000000000") || strings.Contains(s, "Bearer abc") {
		t.Errorf("secret in debug log:\n%s", s)
	}
	if strings.Count(s, "You are Jazz.") != 40 {
		t.Errorf("system prompt must appear in full exactly once, got %d copies", strings.Count(s, "You are Jazz."))
	}
	if !strings.Contains(s, `"sha256:`) {
		t.Error("second request must reference the system prompt by hash")
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("log mode %v", fi.Mode().Perm())
	}
}

func TestDebugSSECapAndPrune(t *testing.T) {
	pfTestHome(t)
	pfResetDebug(t)
	path, _ := pfDebugStart()
	line := strings.Repeat("d", 10<<10)
	for i := 0; i < 40; i++ {
		dbg("sse", line)
	}
	b, _ := os.ReadFile(path)
	if n := strings.Count(string(b), `"kind":"sse"`); n >= 40 || n == 0 {
		t.Errorf("SSE lines logged = %d, want capped", n)
	}
	d := pfLogDir()
	for i := 0; i < 8; i++ {
		os.WriteFile(filepath.Join(d, fmt.Sprintf("2000010%d-000000.000-1.log", i)), []byte("x"), 0o600)
	}
	pfPruneLogs(d)
	ents, _ := os.ReadDir(d)
	logs := 0
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".log") {
			logs++
		}
	}
	if logs != pfLogKeep {
		t.Errorf("kept %d logs, want %d", logs, pfLogKeep)
	}
}

func TestDebugCrashWritesFile(t *testing.T) {
	pfTestHome(t)
	code := -1
	exited := make(chan struct{})
	old := pfExit
	pfExit = func(c int) { code = c; close(exited) }
	t.Cleanup(func() {
		pfExit = old
		pfCrashState.mu.Lock()
		pfCrashState.once = sync.Once{}
		pfCrashState.cleanups = nil
		pfCrashState.mu.Unlock()
	})
	cleaned := false
	pfOnCrash(func() { cleaned = true })
	fin := make(chan struct{})
	go func() { // same shape as safeGo, plus a signal once the guard has returned
		defer close(fin)
		defer pfCrashGuard()
		panic("boom in goroutine")
	}()
	select {
	case <-fin:
	case <-time.After(5 * time.Second):
		t.Fatal("crash handler did not run")
	}
	<-exited
	if code != 70 || !cleaned {
		t.Errorf("exit code %d cleaned %v", code, cleaned)
	}
	p := pfNewestFile(dataDir(), "crash-", ".txt")
	b, err := os.ReadFile(p)
	if err != nil || !strings.Contains(string(b), "boom in goroutine") || !strings.Contains(string(b), version) {
		t.Errorf("crash file %q: %v\n%s", p, err, b)
	}
}

func TestUpdateHelpers(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{{"0.2.1", "0.2.0", true}, {"v0.10.0", "0.9.9", true}, {"0.2.0", "0.2.0", false}, {"0.1.9", "0.2.0", false}, {"1.0.0-rc1", "0.9", true}}
	for _, c := range cases {
		if got := pfVersionNewer(c.a, c.b); got != c.want {
			t.Errorf("newer(%s,%s)=%v", c.a, c.b, got)
		}
	}
	sums := []byte("aaaa  other\n" + strings.Repeat("ab", 32) + "  jazz-linux-386\n")
	if got := pfParseSums(sums, "jazz-linux-386"); got != strings.Repeat("ab", 32) {
		t.Errorf("sums = %q", got)
	}
}

func TestUpdateFromDirectorySource(t *testing.T) {
	pfTestHome(t)
	bin := []byte("#!/bin/sh\necho new\n")
	sum := pfHash(bin)
	asset := pfAssetName()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rel/VERSION":
			fmt.Fprint(w, "v9.9.9\n")
		case "/rel/checksums.txt":
			fmt.Fprintf(w, "%s  %s\n", sum, asset)
		case "/rel/" + asset:
			w.Write(bin)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	rel, err := pfLatestRelease(context.Background(), &Config{UpdateURL: srv.URL + "/rel/"})
	if err != nil || rel.Version != "9.9.9" || !strings.HasSuffix(rel.AssetURL, asset) {
		t.Fatalf("release %+v err %v", rel, err)
	}
	exe := filepath.Join(t.TempDir(), "jazz")
	os.WriteFile(exe, []byte("old"), 0o755)
	if _, err := pfInstallBinary(exe, bin, strings.Repeat("0", 64)); err == nil {
		t.Fatal("checksum mismatch must abort")
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Fatal("failed update must leave the old binary")
	}
	if hint, err := pfInstallBinary(exe, bin, sum); err != nil || hint != "" {
		t.Fatalf("install: %q %v", hint, err)
	}
	if b, _ := os.ReadFile(exe); string(b) != string(bin) {
		t.Error("binary not replaced")
	}
	if fi, _ := os.Stat(exe); fi.Mode().Perm() != 0o755 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
}

func TestUpdateNoticeFromCache(t *testing.T) {
	pfTestHome(t)
	t.Setenv("JAZZ_NO_UPDATE_CHECK", "")
	b := []byte(fmt.Sprintf(`{"checked":%d,"latest":"99.0.0"}`, time.Now().Unix()))
	os.MkdirAll(dataDir(), 0o700)
	os.WriteFile(pfUpdateCachePath(), b, 0o600)
	if n := pfUpdateNotice(&Config{}); !strings.Contains(n, "99.0.0") {
		t.Errorf("notice %q", n)
	}
	t.Setenv("JAZZ_NO_UPDATE_CHECK", "1")
	if pfUpdateNotice(&Config{}) != "" {
		t.Error("JAZZ_NO_UPDATE_CHECK must silence it")
	}
}

func TestMainMaskKey(t *testing.T) {
	if got := pfMaskKey("sk-ant-TESTKEY-00000ijkl"); got != "…ijkl" {
		t.Errorf("long key: %q", got)
	}
	if got := pfMaskKey("short123"); strings.Contains(got, "short") || strings.Contains(got, "123") {
		t.Errorf("short key leaked: %q", got)
	}
}

func TestMainSessionRetention(t *testing.T) {
	pfTestHome(t)
	dir := filepath.Join(dataDir(), "sessions", "proj")
	os.MkdirAll(dir, 0o700)
	oldF, newF := filepath.Join(dir, "old.jsonl"), filepath.Join(dir, "new.jsonl")
	os.WriteFile(oldF, []byte("{}"), 0o600)
	os.WriteFile(newF, []byte("{}"), 0o600)
	past := time.Now().Add(-40 * 24 * time.Hour)
	os.Chtimes(oldF, past, past)
	if pfSessionDays() != 30 {
		t.Errorf("default session_days = %d", pfSessionDays())
	}
	if n := pfCleanSessions(30); n != 1 {
		t.Errorf("removed %d", n)
	}
	if _, err := os.Stat(oldF); err == nil {
		t.Error("old session kept")
	}
	if _, err := os.Stat(newF); err != nil {
		t.Error("recent session removed")
	}
	if pfCleanSessions(-1) != 0 {
		t.Error("negative days must keep everything")
	}
}

func TestToolsExecRedactsSecrets(t *testing.T) {
	pfTestHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-TESTKEY-SECRETSECRET00000000")
	refreshSecrets()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, ".env"), []byte("ANTHROPIC_API_KEY=sk-ant-TESTKEY-SECRETSECRET00000000\nOTHER=ghp_TESTKEY00000000000000000000000000000\n"), 0o600)
	tb := NewToolbox(root)
	defer tb.Shell.Close()
	out := tb.Exec(context.Background(), "read", `{"path":".env"}`)
	if strings.Contains(out.Text, "SECRETSECRET") || strings.Contains(out.Text, "ghp_TESTKEY") {
		t.Errorf("read output not redacted: %s", out.Text)
	}
}
