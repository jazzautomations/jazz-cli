package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditChainVerifiesAndDetectsTampering(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	t.Setenv("JAZZ_AUDIT", "")
	auSetSession("s1")
	for i := 0; i < 5; i++ {
		auditRec("shell", map[string]any{"cmd": "echo " + string(rune('a'+i))})
	}
	var out bytes.Buffer
	if cmdAudit([]string{"--verify"}, &out) != 0 || !strings.Contains(out.String(), "5 registros") {
		t.Fatalf("verify: %s", out.String())
	}
	b, _ := os.ReadFile(auPath())
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")

	// edit a record
	tampered := strings.Replace(string(b), "echo c", "echo X", 1)
	os.WriteFile(auPath(), []byte(tampered), 0o600)
	out.Reset()
	if cmdAudit([]string{"--verify"}, &out) == 0 || !strings.Contains(out.String(), "hash não confere") {
		t.Fatalf("edit not detected: %s", out.String())
	}
	// drop a record
	os.WriteFile(auPath(), []byte(strings.Join(append(lines[:2:2], lines[3:]...), "\n")+"\n"), 0o600)
	out.Reset()
	if cmdAudit([]string{"--verify"}, &out) == 0 || !strings.Contains(out.String(), "elo anterior não confere") {
		t.Fatalf("removal not detected: %s", out.String())
	}
	// restore, then a torn line from a crash: reported, chain still intact
	os.WriteFile(auPath(), []byte(strings.Join(lines, "\n")+"\n"+`{"prev":"ab`), 0o600)
	auditRec("shell", map[string]any{"cmd": "after crash"})
	out.Reset()
	if cmdAudit([]string{"--verify"}, &out) != 0 || !strings.Contains(out.String(), "1 linha(s) cortada") {
		t.Fatalf("torn line: %s", out.String())
	}
}

func TestAuditRotationKeepsChain(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	old := auMaxBytes
	auMaxBytes = 600
	defer func() { auMaxBytes = old }()
	for i := 0; i < 12; i++ {
		auditRec("net.egress", map[string]any{"host": "example.com", "via": "test"})
	}
	if n := len(auFiles()); n < 3 {
		t.Fatalf("%d files: rotation did not happen", n)
	}
	v := auVerify()
	if v.Records != 12 || len(v.Broken) != 0 {
		t.Fatalf("verify across rotation: %+v", v)
	}
}

func TestAuditConfigChangeRecorded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JAZZ_HOME", home)
	cfg := &Config{Model: "fake/m1", Mode: "auto"}
	os.MkdirAll(configDir(), 0o700)
	os.WriteFile(filepath.Join(configDir(), "config.json"), []byte(`{"model":"a"}`), 0o600)
	auConfigStart(cfg, "/p", "fake/m1", "sandbox-exec", map[string]any{"guards": "on"})
	auConfigStart(cfg, "/p", "fake/m1", "sandbox-exec", map[string]any{"guards": "on"})
	os.WriteFile(filepath.Join(configDir(), "config.json"), []byte(`{"model":"b"}`), 0o600)
	auConfigStart(cfg, "/p", "fake/m1", "sandbox-exec", map[string]any{"guards": "on"})
	var out bytes.Buffer
	cmdAudit([]string{"--kind", "config"}, &out)
	if c := strings.Count(out.String(), "config.json mudou"); c != 2 { // first sight + the edit
		t.Fatalf("config changes = %d:\n%s", c, out.String())
	}
	if c := strings.Count(out.String(), "config em vigor"); c != 3 {
		t.Fatalf("config records = %d", c)
	}
}

// Overhead on the Dell: one event or audit record per call.
func BenchmarkObsEmit(b *testing.B) {
	b.Setenv("JAZZ_HOME", b.TempDir())
	l, err := obOpen(filepath.Join(b.TempDir(), "s.jsonl"), "bench")
	if err != nil {
		b.Fatal(err)
	}
	defer l.close()
	f := map[string]any{"id": "call_1", "name": "bash", "dur_ms": 123, "bytes": 4096, "is_err": false, "ran": true}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		l.emit("tool.end", f, false)
	}
}

func BenchmarkAuditRec(b *testing.B) {
	b.Setenv("JAZZ_HOME", b.TempDir())
	f := map[string]any{"cmd_sha": obSHA("go test ./..."), "cmd": "go test ./...", "is_err": false}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		auditRec("shell", f)
	}
}
