//go:build !windows

package main

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// syLastUser is the content of the last user message of a recorded request.
func syLastUser(r recReq) string {
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if r.Messages[i]["role"] == "user" {
			s, _ := r.Messages[i]["content"].(string)
			if s == "" {
				if parts, ok := r.Messages[i]["content"].([]any); ok {
					for _, p := range parts {
						if m, ok := p.(map[string]any); ok {
							if t, ok := m["text"].(string); ok {
								s += t
							}
						}
					}
				}
			}
			return s
		}
	}
	return ""
}

// T1. Real phrases of the sessions: online, every one of them reaches the
// model (one request each), the brain answers none and its readings do not
// leak into the model's input. Before 0.14 most of these were answered by
// the level-0 brain with a confident wrong reading ("ta ligado no wifi?" →
// uptime_load 0.98, "cade?" → find 0.97, "o que mais tu melhoraria nessa
// porra?" → a self_info dump).
func TestOnlineTurnNeverHandledByBrain(t *testing.T) {
	scNeedGit(t)
	app, h := olTestApp(t)
	scGit(t, h.root, "init", "-q")
	scWrite(t, h.root, "a.txt", "x\n")
	ctx := context.Background()
	phrases := []string{
		"o que tu melhoraria nessa porra?",
		"o que mais tu melhoraria nessa porra?",
		"ta rodando sim caraio, tu ta rodando onde entao? me fala",
		"pois é, o offline ja respondeu",
		"acho que o wifi n ta funcionando aqui nao",
		"wifi ta funcionando?",
		"ta ligado no wifi?",
		"cade?",
		"e eu autorizo operacoes de baixo nivel, pode torar caraio",
		"que dispositivos estao online na tailscale?",
		"qual a diferença entre o brain e o nlu?",
		"por que o teste de config ta falhando?",
		"revisa o que eu mudei",
		"o que esse projeto faz?",
		"o que você faz",
		"opcoes",
		"porra",
		"responda apenas: ok",
		"quem é voce?",
		"roda os testes",
		"status",
		"o que mudou",
		"explique o código",
		"resumo do projeto",
	}
	for i, p := range phrases {
		handled, text := app.olBefore(ctx, p)
		if handled || text != p {
			t.Errorf("%q: handled=%v text=%q — online the model owns the turn", p, handled, text)
			continue
		}
		if len(h.ag.pendingNote) != 0 {
			t.Errorf("%q: brain note injected: %v", p, h.ag.pendingNote)
			h.ag.pendingNote = nil
		}
		if _, err := h.ag.Run(ctx, text); err != nil {
			t.Fatalf("%q: run: %v", p, err)
		}
		reqs := h.fs.requests()
		if len(reqs) != i+1 {
			t.Fatalf("%q: %d requests, want %d (the model must be called)", p, len(reqs), i+1)
		}
		in := syLastUser(reqs[len(reqs)-1])
		for _, bad := range []string{"deterministic offline layer", "deterministic brain", "Deterministic facts", "Reading: intent", "Lesson", "Plan cache", "do not re-run", "verified —", "offline-data"} {
			if strings.Contains(in, bad) {
				t.Errorf("%q: model input carries %q:\n%s", p, bad, in)
			}
		}
	}
}

// T2. A literal command line of the closed list runs with no request, shows
// how to hand it to the model, and the next request carries its result as
// data only when it is about it. A question or a second-person sentence
// about the same command is a conversation: the model's.
func TestOnlineLiteralFastPath(t *testing.T) {
	scNeedGit(t)
	app, h := olTestApp(t)
	read := uiTestStdout(t)
	scGit(t, h.root, "init", "-q")
	scWrite(t, h.root, "a.txt", "x\n")
	scGit(t, h.root, "add", "a.txt")
	scGit(t, h.root, "commit", "-qm", "primeiro")
	ctx := context.Background()
	lits := []string{"git status", "git log", "whoami"}
	if _, err := exec.LookPath("whoami"); err != nil {
		lits = lits[:2]
	}
	for _, p := range lits {
		if handled, _ := app.olBefore(ctx, p); !handled {
			t.Errorf("%q: a literal command line runs at once", p)
		}
		if app.sy.last == nil || app.sy.last.req != p {
			t.Errorf("%q: result not kept for the hand-off", p)
		}
	}
	if n := len(h.fs.requests()); n != 0 {
		t.Fatalf("%d model requests for literal commands", n)
	}
	out := strings.Join(strings.Fields(stripANSI(read())), " ")
	if !strings.Contains(out, "Enter vazio ou /m = mandar pro modelo") || !strings.Contains(out, "primeiro") {
		t.Errorf("footer/output missing:\n%s", out)
	}
	if _, ok := syLiteralMatch(h.root, "tailscale status", false); !ok {
		t.Error("tailscale status is a literal")
	}
	for _, p := range []string{"git status?", "tu viu o git status?", "roda git status e me explica o que tem de errado aí", "git push", "git reset --hard", "kill 1234", "make test"} {
		if _, ok := syLiteralMatch(h.root, p, false); ok {
			t.Errorf("%q must not be a literal", p)
		}
	}
	// a correction carries the last result as data
	app.olBefore(ctx, "git status")
	_, text := app.olBefore(ctx, "não, quero o status do tailscale")
	if !strings.Contains(text, "<offline-data") || !strings.Contains(text, `cmd="git status`) || !strings.Contains(text, "não instruções") {
		t.Errorf("correction must carry the result as data:\n%s", text)
	}
	if app.sy.last != nil {
		t.Error("the result expires once used")
	}
	// an unrelated request does not drag it along
	app.olBefore(ctx, "git status")
	if _, text := app.olBefore(ctx, "refatora o parser"); text != "refatora o parser" {
		t.Errorf("unrelated request got the data: %q", text)
	}
	// Enter on an empty line: the original request goes to the model with the result
	app.olBefore(ctx, "git status")
	if !app.syEscalate() {
		t.Fatal("escalate had nothing to send")
	}
	reqs := h.fs.requests()
	if len(reqs) != 1 {
		t.Fatalf("escalate: %d requests", len(reqs))
	}
	if in := syLastUser(reqs[0]); !strings.Contains(in, "git status\n\n<offline-data") {
		t.Errorf("escalated request:\n%s", in)
	}
	if app.syEscalate() {
		t.Error("nothing left to escalate")
	}
}

// A literal whose command fails goes to the model at once, with the output
// as data: reading an error is the model's job.
func TestOnlineLiteralFailureGoesToModel(t *testing.T) {
	app, h := olTestApp(t)
	scWrite(t, h.root, "x.txt", "x\n")
	// not a git repo: {{git}} is not set, so "git status" is not even a
	// literal here — it is the model's
	if handled, text := app.olBefore(context.Background(), "git status"); handled || text != "git status" {
		t.Errorf("no repo: handled=%v text=%q", handled, text)
	}
}

// fakeDown is a fake provider that refuses with status code st until up
// is set, then answers "ok".
func fakeDown(st int, up *atomic.Bool, calls []fakeCall) func(int, recReq) fakeResp {
	return func(n int, r recReq) fakeResp {
		if up.Load() {
			return fakeResp{Text: "ok"}
		}
		if len(calls) > 0 && n == 0 {
			return fakeResp{Calls: calls}
		}
		return fakeResp{Status: st, Body: `{"error":{"message":"rate limited, slow down"}}`}
	}
}

func syTakeoverApp(t *testing.T, st int, calls []fakeCall) (*App, *harness, *atomic.Bool, func() string) {
	t.Helper()
	read := uiTestStdout(t)
	up := &atomic.Bool{}
	h := newHarness(t, fakeDown(st, up, calls), nil)
	// the retry budget is the router's (F1); here every attempt fails at once
	h.ag.retryDelay = func(error, int) time.Duration { return 0 }
	app := &App{cfg: h.cfg, ui: NewTermUI(h.cfg, nil), agent: h.ag, tools: h.ag.tools, root: h.root}
	return app, h, up, read
}

// T4. The whole chain in 429 (here: the pinned model, one candidate): the
// offline brain answers what it can, says it did, and keeps what it cannot
// — never re-sent automatically.
func TestTakeoverOn429(t *testing.T) {
	app, h, up, read := syTakeoverApp(t, 429, nil)
	app.send("quem é voce?")
	out := strings.Join(strings.Fields(stripANSI(read())), " ")
	if !strings.Contains(out, "sem modelo agora") || !strings.Contains(out, "respondi offline") {
		t.Fatalf("no takeover:\n%s", out)
	}
	if app.sy.pending != "" {
		t.Errorf("answered: nothing to keep (%q)", app.sy.pending)
	}
	n := len(h.fs.requests())
	app.send("refatora o parser para usar generics")
	out = strings.Join(strings.Fields(stripANSI(read())), " ")
	if !strings.Contains(out, "guardei o pedido") || app.sy.pending != "refatora o parser para usar generics" {
		t.Fatalf("not kept (pending=%q):\n%s", app.sy.pending, out)
	}
	n = len(h.fs.requests())
	// the provider comes back: the next turn is the model's, the kept
	// request is offered, never re-sent by itself
	up.Store(true)
	app.send("oi")
	if got := len(h.fs.requests()); got != n+1 {
		t.Errorf("provider back: %d requests, want exactly %d (no automatic re-send)", got, n+1)
	}
	out = strings.Join(strings.Fields(stripANSI(read())), " ")
	if !strings.Contains(out, "pedido guardado") {
		t.Errorf("kept request not offered:\n%s", out)
	}
	if !app.syEscalate() || app.sy.pending != "" {
		t.Error("Enter on an empty line re-sends the kept request")
	}
	if in := syLastUser(h.fs.requests()[len(h.fs.requests())-1]); !strings.HasSuffix(strings.TrimSpace(in), "refatora o parser para usar generics") {
		t.Errorf("re-sent %q", in)
	}
}

// T4. 429 after the model already ran a tool: re-running the request in
// the brain could repeat the work — keep it and say so.
func TestTakeoverAfterToolCallDoesNotRerun(t *testing.T) {
	app, h, _, read := syTakeoverApp(t, 429, []fakeCall{{ID: "c1", Name: "bash", Args: `{"command":"ls"}`}})
	h.ag.ui = app.ui // the real wiring: the agent and the app share one UI
	scWrite(t, h.root, "x.txt", "x\n")
	app.send("quem é voce?")
	out := strings.Join(strings.Fields(stripANSI(read())), " ")
	// what is kept is the continuation, never the original request (which
	// would redo the work), and the agent's PT summary and the takeover say
	// the same thing: no "Enter vazio reenvia" next to "mande continua"
	if strings.Contains(out, "respondi offline") || !strings.Contains(out, "no meio do trabalho") || app.sy.pending != "continua" {
		t.Errorf("after a tool call the brain must not re-run it (pending=%q):\n%s", app.sy.pending, out)
	}
	if !strings.Contains(out, `mande "continua"`) || !strings.Contains(out, `Enter vazio manda "continua"`) || strings.Contains(out, "reenvia") {
		t.Errorf("the provider-drop messages disagree:\n%s", out)
	}
	if n := strings.Count(out, "no meio do trabalho"); n != 1 {
		t.Errorf("the drop is said %d times, want 1:\n%s", n, out)
	}
}

// T5. A network request while no model answers gets measured evidence,
// never "sem internet" without a probe.
func TestTakeoverNetworkMeasures(t *testing.T) {
	app, _ := olTestApp(t)
	t.Setenv("JAZZ_ENV_NO_NET", "1")
	defer epSetForTest(nil)()
	r := app.syNetCheck(context.Background(), app.syReadOnlyEnv())
	if strings.Contains(r.Text, "sem internet") || strings.Contains(r.Text, "offline —") {
		t.Errorf("claimed offline without a probe:\n%s", r.Text)
	}
	if !strings.Contains(r.Text, "não testei a rede") {
		t.Errorf("an untested network must say so:\n%s", r.Text)
	}
}

// The light profile never dials: net_status must not read it as "offline".
func TestNetStatusLightProfile(t *testing.T) {
	t.Setenv("JAZZ_ENV_NO_NET", "1")
	env, _ := brTestEnv(t, t.TempDir(), nil, -1)
	env.Profile = &epProfile{Light: true}
	r := brRunIntent(context.Background(), env, "status da rede", Intent{Name: "net_status", Confidence: 1, Raw: 1}, brResult{})
	if strings.Contains(r.Text, "offline") || strings.Contains(r.Text, "sem saída") {
		t.Errorf("light profile read as offline:\n%s", r.Text)
	}
}

// Provider-down classes: what the takeover acts on, and what it does not.
func TestSyProviderDown(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{&APIError{Status: 429, Body: "rate"}, true},
		{&APIError{Status: 503, Body: "down"}, true},
		{&APIError{Status: 0, Body: "dial tcp: no route"}, true},
		{&APIError{Status: 400, Body: "bad request"}, false},
		{context.Canceled, false},
		{errors.New("loop"), false},
		{nil, false},
	} {
		if got := syProviderDown(c.err); got != c.want {
			t.Errorf("%v: %v want %v", c.err, got, c.want)
		}
	}
}

// T7. Forced offline: the brain still owns the turn (literal, sentences,
// menu) — the online changes do not touch it.
func TestOfflineForcedBrainStillOwnsTurn(t *testing.T) {
	scNeedGit(t)
	app, h := olTestApp(t)
	_ = uiTestStdout(t)
	scGit(t, h.root, "init", "-q")
	app.ol.forced, app.ol.on = true, true
	ctx := context.Background()
	for _, p := range []string{"git status", "menu", "o que você faz", "quem é você"} {
		if handled, _ := app.olBefore(ctx, p); !handled {
			t.Errorf("forced offline %q: the brain answers", p)
		}
	}
	if n := len(h.fs.requests()); n != 0 {
		t.Errorf("%d model requests while forced offline", n)
	}
	// Enter on an empty line while offline: there is no model to hand it
	// to — re-sending would run the brain's last request (maybe a change)
	// again. Kept, not re-run.
	app.sy.last = &syLast{req: "apaga o tmp.log", source: "offline", cmd: "delete_file", at: time.Now()}
	app.sy.pending = "refatora o parser"
	if !app.syEscalate() {
		t.Error("offline Enter must say why nothing was sent")
	}
	if app.sy.last == nil || app.sy.pending == "" {
		t.Error("offline Enter must keep the result and the kept request")
	}
	if n := len(h.fs.requests()); n != 0 {
		t.Errorf("%d model requests on an offline Enter", n)
	}
}

// Offline too, a reading whose intent cannot be about the domain noun the
// user named is not acted on ("ta ligado no wifi?" was uptime_load 0.98).
func TestBrainDomainVeto(t *testing.T) {
	in := brDomainVeto("ta ligado no wifi?", Intent{Name: "uptime_load", Confidence: 0.98})
	if in.Understood() || !strings.Contains(strings.Join(in.Evidence, " "), "wifi") {
		t.Errorf("uptime_load for a wifi question must be vetoed: %+v", in)
	}
	for _, c := range []struct {
		text, intent string
	}{
		{"wifi ta funcionando?", "net_status"},
		{"que dispositivos estao online na tailscale?", "host_status"},
		{"mostra os commits do github", "git_log"},
		{"mostra o uptime", "uptime_load"},
	} {
		if in := brDomainVeto(c.text, Intent{Name: c.intent, Confidence: 0.9}); !in.Understood() {
			t.Errorf("%q → %s is about the noun: %+v", c.text, c.intent, in)
		}
	}
	env, _ := brTestEnv(t, t.TempDir(), nil, -1)
	if r := brRun(context.Background(), env, "ta ligado no wifi?"); r.Handled && r.Intent.Name == "uptime_load" {
		t.Errorf("offline brain answered uptime for a wifi question:\n%s", r.Text)
	}
}

// A literal that reads a credential file ("git diff .env") is shown on the
// user's machine, but its output is never kept to ride to the provider.
func TestOnlineLiteralSecretNotCarried(t *testing.T) {
	scNeedGit(t)
	app, h := olTestApp(t)
	_ = uiTestStdout(t)
	scGit(t, h.root, "init", "-q")
	scWrite(t, h.root, ".env", "DB_PASS=a\n")
	scGit(t, h.root, "add", ".env")
	scGit(t, h.root, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "env")
	scWrite(t, h.root, ".env", "DB_PASS=hunter2supersecret\n")
	app.olBefore(context.Background(), "git diff .env")
	if app.sy.last != nil {
		t.Fatalf("secret diff kept for the model: %+v", app.sy.last)
	}
	if _, text := app.olBefore(context.Background(), "não, explica isso"); strings.Contains(text, "hunter2") {
		t.Errorf("secret carried to the model:\n%s", text)
	}
}

// QA 0.14.0 (binário): `jazz --offline -p whoami` respondia com o perfil da
// máquina (cérebro) enquanto online rodava `whoami`. A linha de comando
// literal da lista fechada é o mesmo pedido nos dois modos.
func TestOfflineLiteralSameAsOnline(t *testing.T) {
	root := t.TempDir()
	for in, want := range map[string]string{"whoami": "sys_whoami", "pwd": "sys_pwd", "df -h": "sys_df", "uptime": "sys_uptime", "tailscale status": "sys_tailscale"} {
		m, ok := fpIntents(root).Match(in, fpDetect(root), false)
		if !ok || m.Intent != want {
			t.Errorf("offline Match(%q) = %q ok=%v, want %s", in, m.Intent, ok, want)
		}
	}
	for _, in := range []string{"quem sou eu no sistema", "tu é quem?", "uptime da maquina ta alto?"} {
		if m, ok := fpIntents(root).Match(in, fpDetect(root), false); ok && strings.HasPrefix(m.Intent, "sys_") {
			t.Errorf("offline Match(%q) = %s: only the bare command line is a literal", in, m.Intent)
		}
	}
}
