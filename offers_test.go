//go:build !windows

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---------- menu composition (pure) ----------

func TestOffersMenuComposition(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "cmd"), 0o755)
	os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644)
	os.WriteFile(filepath.Join(root, "cmd", "run.go"), []byte("package cmd\n"), 0o644)
	goProj := fpProject{Langs: []string{"go"}, Test: "go test ./...", Git: true}
	type fixture struct {
		name string
		in   olMenuIn
		want []string // labels, in order
	}
	cases := []fixture{
		{"file + term + tests + git dirty, offline",
			olMenuIn{Text: "por que parseArgs quebra em main.go?", Offline: true, Proj: goProj, GitDirty: true, Root: root, HasRg: true},
			[]string{"abrir/ler main.go", "procurar 'parseArgs' no código", "rodar os testes e listar falhas", "mostrar git status/diff resumido", "/online — tentar o modelo de novo"}},
		{"last error location first, waiting online",
			olMenuIn{Text: "conserta isso", Waiting: true, Proj: goProj, LastErr: "--- FAIL: TestX\n    cmd/run.go:12: boom", Root: root},
			[]string{"localizar o erro em cmd/run.go:12", "rodar os testes e listar falhas", "mostrar os últimos commits", "resumo do projeto", "esperar o modelo"}},
		{"quoted term, no project",
			olMenuIn{Text: `onde aparece "token expirado"?`, Offline: true, Root: root},
			[]string{"procurar 'token expirado' no código", "resumo do projeto", "/online — tentar o modelo de novo"}},
		{"chit-chat: only project items",
			olMenuIn{Text: "oi", Offline: true, Proj: goProj, Root: root},
			[]string{"rodar os testes e listar falhas", "mostrar os últimos commits", "resumo do projeto", "/online — tentar o modelo de novo"}},
		{"missing file is not offered",
			olMenuIn{Text: "le o arquivo nao_existe.go", Offline: true, Root: root},
			[]string{"resumo do projeto", "/online — tentar o modelo de novo"}},
	}
	for _, c := range cases {
		var got []string
		for _, it := range olMenu(c.in) {
			got = append(got, it.Label)
		}
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s:\n got  %q\n want %q", c.name, got, c.want)
		}
		if len(got) > olMenuMax {
			t.Errorf("%s: %d items", c.name, len(got))
		}
	}
	// the search command is shell-safe
	items := olMenu(olMenuIn{Text: `procura "x; rm -rf ~"`, Offline: true, Root: root, HasRg: true})
	if !strings.Contains(items[0].Cmd, `-- 'x; rm -rf ~' .`) {
		t.Errorf("unquoted search: %q", items[0].Cmd)
	}
	// a failed !command
	fm := olShellFailMenu("apt install foo", "E: Could not open lock file - open (13: Permission denied)", root, true)
	var fl []string
	for _, it := range fm {
		fl = append(fl, it.Label)
	}
	if strings.Join(fl, "|") != "repetir com sudo|mostrar o manual de apt|procurar 'Permission' no código" {
		t.Errorf("shell-fail menu: %q", fl)
	}
}

func TestOffersPick(t *testing.T) {
	items := olMenu(olMenuIn{Text: "x", Offline: true, Proj: fpProject{Test: "go test ./...", Git: true}, GitDirty: true})
	// 1 testes, 2 git, 3 resumo, 4 online
	for reply, want := range map[string]int{"1": 0, " 2 ": 1, "4": 3, "9": -1, "0": -1, "ro": 0, "rodar": 0, "git": 1,
		"res": 2, "/on": 3, "m": -1, "mostra o diff do arquivo main.go por favor": -1, "explique o código": -1, "": -1} {
		if got := olPick(items, reply); got != want {
			t.Errorf("pick %q = %d, want %d", reply, got, want)
		}
	}
	if l := olMenuLine(items); l != "[1 testes · 2 git · 3 resumo · 4 online · esc cancela]" {
		t.Errorf("one-line menu: %q", l)
	}
}

// ---------- offline flow ----------

func offTestApp(t *testing.T) (*App, *harness, func() string) {
	t.Helper()
	read := uiTestStdout(t)
	app, h := olTestApp(t)
	app.agent.NetOffline = true
	return app, h, read
}

// A dead network ends the turn within 5 s and switches to offline mode with
// the banner, instead of waiting through timeouts and the whole chain.
func TestOffersNetworkFailureGoesOfflineFast(t *testing.T) {
	app, h, read := offTestApp(t)
	oldSlow, oldProbe, oldEvery := pvConnectSlow, bgNetProbe, olBgEvery
	t.Cleanup(func() { pvConnectSlow, bgNetProbe, olBgEvery = oldSlow, oldProbe, oldEvery })
	pvConnectSlow = 300 * time.Millisecond
	bgNetProbe = func(context.Context, []string) bool { return false } // nothing answers
	olBgEvery = time.Hour
	oldDial := olDial
	t.Cleanup(func() { olDial = oldDial })
	olDial = func(string, time.Duration) error { return errors.New("down") }
	// a non-routable address: the connect hangs (or fails at once)
	h.cfg.Providers["fake"].BaseURL = "http://192.0.2.1:9/v1"
	t0 := time.Now()
	app.send("explique o código")
	el := time.Since(t0)
	if el > 5*time.Second {
		t.Fatalf("offline after %s (want < 5 s)", el)
	}
	if !app.ol.on {
		t.Fatalf("not offline; output:\n%s", read())
	}
	out := strings.Join(strings.Fields(stripANSI(read())), " ") // notices are word-wrapped
	for _, w := range []string{"sem rede → modo offline: atalhos determinísticos ativos; /online para tentar de novo", "verifico a rede a cada 30 s"} {
		if !strings.Contains(out, w) {
			t.Errorf("banner misses %q:\n%s", w, out)
		}
	}
	t.Logf("offline in %s", el.Round(10*time.Millisecond))
}

// Offline, a fast path answers with no model and no confirmation, shown like
// a tool call; a request with no fast path gets the numbered menu, a digit
// runs an item, other text passes through.
func TestOffersFastPathAndMenuWhileOffline(t *testing.T) {
	scNeedGit(t)
	app, h, read := offTestApp(t)
	scGit(t, h.root, "init", "-q")
	os.WriteFile(filepath.Join(h.root, "a.txt"), []byte("x\n"), 0o644)
	ctx := context.Background()
	oldDial := olDial
	t.Cleanup(func() { olDial = oldDial })
	olDial = func(string, time.Duration) error { return errors.New("down") }
	app.ol.on, app.ol.lastProbe = true, time.Now()
	app.olGen.Add(1)
	if handled, _ := app.olBefore(ctx, "mostra o git status"); !handled {
		t.Fatal("offline fast path not handled")
	}
	out := stripANSI(read())
	if !strings.Contains(out, "Bash(git status --short --branch)") || !strings.Contains(out, "?? a.txt") || !strings.Contains(out, "exit 0") {
		t.Fatalf("fast path output:\n%s", out)
	}
	// no fast path: the menu
	if handled, _ := app.olBefore(ctx, "por que o a.txt está assim?"); !handled {
		t.Fatal("offline request should be held with a menu")
	}
	out = stripANSI(read())
	if !strings.Contains(out, "1  abrir/ler a.txt") || !strings.Contains(out, "/online — tentar o modelo de novo") || len(app.menu) == 0 {
		t.Fatalf("menu:\n%s", out)
	}
	// "1" runs it
	if handled, _ := app.olBefore(ctx, "1"); !handled {
		t.Fatal("digit not taken as a pick")
	}
	if out = stripANSI(read()); !strings.Contains(out, "Bash(head -n 80 -- a.txt)") {
		t.Fatalf("pick did not run the action:\n%s", out)
	}
	if app.menu != nil {
		t.Error("menu not cleared after a pick")
	}
	// text instead of a digit is a normal prompt (held offline, new menu)
	app.olOffer("t", olMenu(app.olMenuState("x", true, false)))
	if _, ok := app.olTakePick("explique o projeto"); ok {
		t.Error("text taken as a pick")
	}
	// resumo do projeto offline runs the scripts
	if handled, _ := app.olBefore(ctx, "resumo do projeto"); !handled {
		t.Error("resumo do projeto not handled offline")
	}
}

// The background probe brings the session back online by itself, with a notice.
func TestOffersAutoReturnOnline(t *testing.T) {
	app, _, read := offTestApp(t)
	oldEvery, oldDial := olBgEvery, olDial
	t.Cleanup(func() { olBgEvery, olDial = oldEvery, oldDial })
	olBgEvery = 30 * time.Millisecond
	var up atomic.Bool
	olDial = func(string, time.Duration) error {
		if up.Load() {
			return nil
		}
		return errors.New("down")
	}
	app.olEnter("sem rede")
	time.Sleep(100 * time.Millisecond)
	if app.olBack.Load() {
		t.Fatal("came back while the network was down")
	}
	up.Store(true)
	deadline := time.Now().Add(3 * time.Second)
	for !app.olBack.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !app.olBack.Load() {
		t.Fatal("background probe never noticed the network")
	}
	if out := stripANSI(read()); !strings.Contains(out, "rede de volta → modo online") {
		t.Errorf("no notice:\n%s", out)
	}
	if handled, _ := app.olBefore(context.Background(), "explique o código"); handled || app.ol.on {
		t.Errorf("still offline after the network came back (handled=%v on=%v)", handled, app.ol.on)
	}
}

// Online and slow: after 5 s without a token the status line offers the
// collapsed menu; a digit runs the action while the request goes on.
func TestOffersWhileWaiting(t *testing.T) {
	r := uiNewLiveRig(t, 86, 27, true, false)
	ran := make(chan olAction, 1)
	items := []olAction{{Key: "testes", Label: "rodar os testes", Kind: "test"}, {Key: "git", Label: "git", Kind: "shell"}, {Key: "esperar", Kind: "wait"}}
	r.u.SetOffers(items, func(a olAction) { ran <- a })
	r.u.Thinking("pensando")
	r.u.mu.Lock()
	r.u.spinStart = time.Now().Add(-6 * time.Second)
	r.u.liveRender(true)
	r.u.mu.Unlock()
	if sc := strings.Join(r.screen(), "\n"); !strings.Contains(sc, "[1 testes · 2 git · esc cancela]") {
		t.Fatalf("collapsed menu not shown:\n%s", sc)
	}
	if !r.u.offerPick('2') {
		t.Fatal("digit not taken")
	}
	select {
	case a := <-ran:
		if a.Key != "git" {
			t.Errorf("ran %q", a.Key)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("offer did not run")
	}
	// once tokens flow the menu goes away
	r.u.StreamText("resposta")
	r.frame()
	if sc := strings.Join(r.screen(), "\n"); strings.Contains(sc, "esc cancela]") {
		t.Errorf("menu still shown while generating:\n%s", sc)
	}
	if r.u.offerPick('1') {
		t.Error("digit taken while generating")
	}
}

func TestOffersFormatters(t *testing.T) {
	js := `{"ok":true,"data":{"framework":"gotest","status":"fail","passed":10,"failed":2,"skipped":0,"failures":[{"test":"TestA","file":"a_test.go","line":12,"message":"want 1 got 2"},{"test":"TestB","file":"b_test.go","line":0,"message":"panic"}]}}`
	got := olFormatTests(js)
	if !strings.HasPrefix(got, "2 falhas, 10 ok (gotest)") || !strings.Contains(got, "TestA a_test.go:12 want 1 got 2") {
		t.Errorf("tests: %q", got)
	}
	sum := olFormatSummary(`{"data":{"langs":["go"],"test":"go test ./...","files":3}}`, `{"data":{"files":3,"loc":120,"dirs":[{"path":".","files":3,"loc":120}]}}`)
	if !strings.Contains(sum, "linguagens: go") || !strings.Contains(sum, "testes: go test ./...") || !strings.Contains(sum, "3 arquivos, 120 linhas") {
		t.Errorf("summary: %q", sum)
	}
}

// The connect watchdog fails a request that cannot connect within
// pvConnectSlow when the probe finds no network (no 20 s dial timeout).
func TestNetConnectWatchdog(t *testing.T) {
	oldSlow, oldProbe := pvConnectSlow, bgNetProbe
	t.Cleanup(func() { pvConnectSlow, bgNetProbe = oldSlow, oldProbe })
	pvConnectSlow = 50 * time.Millisecond
	var probes atomic.Int32
	bgNetProbe = func(context.Context, []string) bool { probes.Add(1); return false }
	ctx, cancel := context.WithCancelCause(context.Background())
	var connected atomic.Bool
	var phases []string
	stop := pvConnectWatch(ctx, "https://api.example.test/v1", &connected, func(p string) { phases = append(phases, p) }, cancel)
	defer stop()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog did not fire")
	}
	if context.Cause(ctx) != error(errNoNet) || probes.Load() != 1 {
		t.Fatalf("cause %v, probes %d", context.Cause(ctx), probes.Load())
	}
	if strings.Join(phases, ",") != "conexão lenta — testando a rede,sem rede" {
		t.Errorf("phases %q", phases)
	}
	// connected in time: nothing happens
	ctx2, cancel2 := context.WithCancelCause(context.Background())
	var c2 atomic.Bool
	c2.Store(true)
	stop2 := pvConnectWatch(ctx2, "https://api.example.test/v1", &c2, func(string) {}, cancel2)
	defer stop2()
	time.Sleep(150 * time.Millisecond)
	if ctx2.Err() != nil {
		t.Error("cancelled a connected request")
	}
	// with a proxy only the proxy is probed
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9")
	if a := pvProbeAddrs("https://api.example.test/v1"); strings.Join(a, ",") != "127.0.0.1:9" {
		t.Errorf("proxy addrs %v", a)
	}
}
