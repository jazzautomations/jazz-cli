//go:build !windows

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOfflineErrorClassification(t *testing.T) {
	yes := []error{
		&APIError{Status: 0, Body: "dial tcp: lookup api.x: no such host"},
		&APIError{Status: 401, Body: "bad key"},
		&APIError{Status: 403, Body: "forbidden"},
		&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")},
		&net.DNSError{Err: "no such host", Name: "api.x"},
		fmt.Errorf("wrapped: %w", &net.DNSError{Err: "no such host", Name: "x"}),
		errors.New("sem chave para groq — rode /key groq (ou exporte GROQ_API_KEY)"),
		errors.New("nenhum modelo disponível — configure uma chave (/key) ou faça login (/login)"),
		errors.New("Post \"https://x/v1\": dial tcp 1.2.3.4:443: connect: network is unreachable"),
	}
	for _, e := range yes {
		if !olIsOffline(e) {
			t.Errorf("%v should be offline-class", e)
		}
	}
	no := []error{nil, context.Canceled, &APIError{Status: 429, Body: "slow down"}, &APIError{Status: 500}, &APIError{Status: 400, Body: "bad request"},
		errStreamIdle, errors.New("contexto estourou")}
	for _, e := range no {
		if olIsOffline(e) {
			t.Errorf("%v must not switch to offline mode", e)
		}
	}
}

func TestOfflineForcedEnvAndOffWords(t *testing.T) {
	for v, want := range map[string]bool{"1": true, "true": true, "yes": true, "": false, "0": false, "off": false, "false": false} {
		t.Setenv("JAZZ_OFFLINE", v)
		if got := olForcedEnv(); got != want {
			t.Errorf("JAZZ_OFFLINE=%q -> %v", v, got)
		}
	}
}

func TestOfflineProbe(t *testing.T) {
	cfg := &Config{Model: "a/m", Providers: map[string]*ProviderConf{
		"a": {Type: "openai", BaseURL: "https://api.a.test/v1", NoKey: true},
		"b": {Type: "openai", BaseURL: "http://10.0.0.5:8080/v1", NoKey: true},
	}, Fallback: []string{"b/m", "a/m2"}}
	hosts := olProbeHosts(cfg, "a/m")
	if strings.Join(hosts, ",") != "api.a.test:443,10.0.0.5:8080" {
		t.Fatalf("hosts = %v", hosts)
	}
	old := olDial
	defer func() { olDial = old }()
	var mu sync.Mutex
	var dialed []string
	olDial = func(hp string, d time.Duration) error {
		mu.Lock()
		dialed = append(dialed, hp)
		mu.Unlock()
		if hp == "10.0.0.5:8080" {
			return nil
		}
		return errors.New("unreachable")
	}
	if !olProbe(cfg, "a/m") {
		t.Error("one reachable host should be enough")
	}
	if len(dialed) != 2 {
		t.Errorf("dialed %v", dialed)
	}
	olDial = func(string, time.Duration) error { return errors.New("down") }
	if olProbe(cfg, "a/m") {
		t.Error("all down should report offline")
	}
}

func TestOfflineHeadlessForced(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	h := newHarness(t, seq(fakeResp{Text: "model was called"}), nil)
	scGit(t, h.root, "init", "-q")
	ui := &HeadlessUI{}
	ctx := context.Background()
	t.Setenv("JAZZ_OFFLINE", "1")
	res, _ := olHeadless(ctx, h.ag, h.ag.tools, ui, h.cfg, "git status", pfOpts{})
	if res == nil || res.Status != StopDone {
		t.Fatalf("forced offline fast path: %+v", res)
	}
	res, _ = olHeadless(ctx, h.ag, h.ag.tools, ui, h.cfg, "explique este repositório", pfOpts{})
	if res == nil || res.Status != StopError || !strings.Contains(res.Err, "modo offline") {
		t.Errorf("forced offline without a fast path must end with a clear error: %+v", res)
	}
	if n := len(h.fs.requests()); n != 0 {
		t.Errorf("%d model requests while offline", n)
	}
	// a local model is still allowed
	h.cfg.Providers["llamacpp"] = &ProviderConf{Type: "openai", BaseURL: "http://127.0.0.1:1/v1", NoKey: true, Local: true}
	h.ag.Model = "llamacpp/qwen"
	if res, p := olHeadless(ctx, h.ag, h.ag.tools, ui, h.cfg, "explique", pfOpts{}); res != nil || p != "explique" {
		t.Errorf("local model should pass through offline mode: %+v", res)
	}
}

// Every provider refuses (401): the run is rescued by the fast path instead of
// exiting 1; a non-offline error (429) is not.
func TestOfflineHeadlessFallbackAfterModelFailure(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	h := newHarness(t, func(int, recReq) fakeResp { return fakeResp{Status: 401, Body: `{"error":{"message":"invalid key"}}`} }, nil)
	scGit(t, h.root, "init", "-q")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := h.ag.Run(ctx, "git status")
	if err == nil || !olIsOffline(err) {
		t.Fatalf("want an offline-class error, got %v", err)
	}
	res := olHeadlessFallback(ctx, h.ag, h.ag.tools, &HeadlessUI{}, err, "git status")
	if res == nil || res.Status != StopDone || res.Status.ExitCode() != 0 {
		t.Errorf("fallback: %+v", res)
	}
	if res := olHeadlessFallback(ctx, h.ag, h.ag.tools, &HeadlessUI{}, &APIError{Status: 429}, "git status"); res != nil {
		t.Errorf("429 must not trigger the offline fallback: %+v", res)
	}
	if res := olHeadlessFallback(ctx, h.ag, h.ag.tools, &HeadlessUI{}, err, "conserte o bug"); res != nil {
		t.Errorf("no fast path, no rescue: %+v", res)
	}
}

func olTestApp(t *testing.T) (*App, *harness) {
	t.Helper()
	h := newHarness(t, seq(fakeResp{Text: "ok"}), nil)
	app := &App{cfg: h.cfg, ui: NewTermUI(h.cfg, nil), agent: h.ag, tools: h.ag.tools, root: h.root}
	return app, h
}

func TestOfflineInteractiveStateMachine(t *testing.T) {
	app, _ := olTestApp(t)
	ctx := context.Background()
	old := olDial
	defer func() { olDial = old }()
	up := false
	olDial = func(string, time.Duration) error {
		if up {
			return nil
		}
		return errors.New("down")
	}

	// a network failure enters offline mode
	if !app.olAfter(&APIError{Status: 0, Body: "no route"}) || !app.ol.on {
		t.Fatal("network error should enter offline mode")
	}
	// within 60 s nothing goes to the model
	if handled, _ := app.olBefore(ctx, "explique o código"); !handled {
		t.Error("offline: message should be held back before the probe interval")
	}
	// after the interval a failed probe still holds it back
	app.ol.lastProbe = time.Time{}
	if handled, _ := app.olBefore(ctx, "explique o código"); !handled {
		t.Error("offline: unreachable providers should hold the message")
	}
	// a successful probe lets it through and a model answer leaves offline mode
	app.ol.lastProbe = time.Time{}
	up = true
	if handled, _ := app.olBefore(ctx, "explique o código"); handled {
		t.Error("reachable provider: the message should go to the model")
	}
	app.olAfter(nil)
	if app.ol.on {
		t.Error("a model answer should leave offline mode")
	}
	// 429 is not offline
	if app.olAfter(&APIError{Status: 429}) || app.ol.on {
		t.Error("429 must not enter offline mode")
	}
	// forced: never reaches the model, /online clears it
	app.olCommand("/offline", "")
	if handled, _ := app.olBefore(ctx, "explique"); !handled || !app.ol.forced {
		t.Error("forced offline must hold every message")
	}
	app.olCommand("/online", "")
	if app.ol.forced || app.ol.on {
		t.Errorf("/online with a reachable provider: %+v", app.ol)
	}
	// a fast path the user declines (Esc) goes to the model unchanged when online
	if handled, text := app.olBefore(ctx, "git status"); handled || text != "git status" {
		t.Errorf("declined fast path: handled=%v text=%q", handled, text)
	}
	// fastpaths off: no prompt at all
	app.cfg.FastPaths = "off"
	if fpEnabled(app.cfg) {
		t.Error("fastpaths off ignored")
	}
}

func TestOfflineCmdScriptExitCodes(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	scTestEnv(t)
	root := t.TempDir()
	if c := cmdScript(root, []string{"extract_versions", `{"text":"v1.0.0"}`}); c != 0 {
		t.Errorf("ok script exit %d", c)
	}
	if c := cmdScript(root, []string{"extract_stacktrace", `{"text":"nothing here"}`}); c != 1 {
		t.Errorf("failed script exit %d", c)
	}
	if c := cmdScript(root, []string{"nope"}); c != 1 {
		t.Errorf("unknown script exit %d", c)
	}
	if c := cmdScript(root, nil); c != 0 {
		t.Errorf("list exit %d", c)
	}
}

func TestOfflineBannerMentionsTools(t *testing.T) {
	cfg := &Config{Providers: map[string]*ProviderConf{}}
	b := olBanner(cfg, "sem rede")
	for _, w := range []string{"sem rede → modo offline", "atalhos determinísticos ativos", "/online para tentar de novo", "rode os testes", "procura X no código", "resumo do projeto", "/script", "!comando", "/undo", "/rewind", "/resume", "a cada 30 s"} {
		if !strings.Contains(b, w) {
			t.Errorf("banner misses %q:\n%s", w, b)
		}
	}
}
