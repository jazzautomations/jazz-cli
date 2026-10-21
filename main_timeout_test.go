package main

import (
	"testing"
	"time"
)

// jazz -p must end at --timeout with exit code 124, even when the model hangs.
func TestHeadlessTimeoutEndsHungRun(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	fs := newFakeServer(t, func(int, recReq) fakeResp { return fakeResp{Text: "começando", HangAfter: true} })
	cfg := &Config{
		Model: "fake/m1", Mode: "auto", MaxSteps: 5, ContextWindow: 128000, MaxTokens: 1000,
		Providers: map[string]*ProviderConf{"fake": {Type: "openai", BaseURL: fs.URL(), NoKey: true}},
	}
	start := time.Now()
	code := runHeadless(cfg, t.TempDir(), "fake/m1", "oi", pfOpts{format: "json", timeout: time.Second})
	if code != 124 {
		t.Fatalf("exit code %d, want 124", code)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("took %s", d)
	}
}

func TestPfDeadlineOff(t *testing.T) {
	hit, stop := pfDeadline(0, func() { t.Fatal("fired") })
	defer stop()
	if hit() {
		t.Fatal("no deadline")
	}
	fired := make(chan struct{})
	hit, stop = pfDeadline(10*time.Millisecond, func() { close(fired) })
	defer stop()
	<-fired
	if !hit() {
		t.Fatal("deadline not reported")
	}
}
