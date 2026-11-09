package main

import (
	"os"
	"os/exec"
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

// Guard da race do hard-exit: stop() rodando no miolo do onTimeout (antes
// do callback armar `hard`) deixava o timer escapar — pfQuit(124) caía
// pfHardExitGrace DEPOIS, matando o processo já encerrado. No binário de
// teste isso é "FAIL sem nenhum teste falho" (observado no job windows).
// O filho força o interleave com canais; o pai só confere o exit code.
func TestPfDeadlineStopDuringOnTimeout(t *testing.T) {
	const flag = "JAZZ_TEST_DEADLINE_CHILD"
	if os.Getenv(flag) == "1" {
		old := pfHardExitGrace
		pfHardExitGrace = 60 * time.Millisecond
		defer func() { pfHardExitGrace = old }()
		fired := make(chan struct{})
		letgo := make(chan struct{})
		_, stop := pfDeadline(time.Millisecond, func() {
			close(fired)
			<-letgo // segura o callback antes de armar o hard-exit
		})
		<-fired
		stop() // corre enquanto onTimeout ainda executa — o caso da fuga
		close(letgo)
		time.Sleep(5 * pfHardExitGrace)
		return // sem o gate `stopped`: pfQuit(124) mata o filho aqui
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestPfDeadlineStopDuringOnTimeout$", "-test.count=1")
	cmd.Env = append(os.Environ(), flag+"=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("filho saiu %v — hard-exit vazou do stop(): %s", err, out)
	}
}
