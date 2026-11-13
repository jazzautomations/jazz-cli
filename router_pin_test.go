//go:build !windows

package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// F1.3: the chosen model is a contract (PROV-1)
// ---------------------------------------------------------------------------

// A pinned model marked dead stays first, and a free pin gets no implicit
// free chain (macbookpro 261110-191600-5c06: -m gemini, 0 requests to gemini).
func TestCandidatesPinnedDeadKept(t *testing.T) {
	c := pvTestCfg(t)
	pin := "openrouter/foo:free"
	MarkExhausted(pin, time.Now().Add(time.Hour))
	got := c.CandidatesFor(Need{Pin: pin}, pin)
	if strings.Join(got, ",") != pin {
		t.Fatalf("pinned chain = %v, want only the pin", got)
	}
	// unpinned, the same ref is dropped and the implicit chain answers
	if got := c.CandidatesFor(Need{}, pin); len(got) == 0 || got[0] == pin {
		t.Fatalf("unpinned chain = %v", got)
	}
}

// The configured fallback is consent: kept after the pin (omni aliases in it
// expand); a dead fallback drops out, the pin never does.
func TestCandidatesPinnedKeepsUserFallback(t *testing.T) {
	c := pvTestCfg(t)
	pin := "openrouter/foo:free"
	c.Fallback = []string{"groq/y", "anthropic/z"}
	if got := c.CandidatesFor(Need{Pin: pin}, pin); strings.Join(got, ",") != "openrouter/foo:free,groq/y,anthropic/z" {
		t.Fatalf("pinned chain = %v", got)
	}
	MarkExhausted("groq/y", time.Now().Add(time.Hour))
	if got := c.CandidatesFor(Need{Pin: pin}, pin); strings.Join(got, ",") != "openrouter/foo:free,anthropic/z" {
		t.Fatalf("dead fallback kept: %v", got)
	}
	// the turn's sticky fallback goes first; a ref outside pin+fallback never enters
	if got := c.CandidatesFor(Need{Pin: pin}, "anthropic/z", pin, "kilo/x"); strings.Join(got, ",") != "anthropic/z,openrouter/foo:free" {
		t.Fatalf("sticky pinned chain = %v", got)
	}
}

func TestStrictModelOnlyPin(t *testing.T) {
	c := pvTestCfg(t)
	c.StrictModel = true
	c.Fallback = []string{"groq/y"}
	if got := c.CandidatesFor(Need{Pin: "gemini/g"}, "gemini/g"); strings.Join(got, ",") != "gemini/g" {
		t.Fatalf("strict chain = %v", got)
	}
}

// rpPinHarness: the pinned free model a:free answers 429 quota; kilo (the
// implicit free chain an unpinned run would use) answers fine.
func rpPinHarness(t *testing.T, mod func(*Config)) *harness {
	t.Helper()
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if r.Model == "a:free" {
			return fakeResp{Status: 429, Body: rcBodyPromo}
		}
		return fakeResp{Text: "resposta de " + r.Model}
	}, func(c *Config) {
		c.Model = "fake/a:free"
		c.Providers["kilo"] = &ProviderConf{Type: "openai", BaseURL: c.Providers["fake"].BaseURL, NoKey: true, DefaultModel: "kfree"}
		c.Pinned = true
		if mod != nil {
			mod(c)
		}
	})
	h.ag.Model = "fake/a:free"
	return h
}

// Headless, no fallback configured: zero requests to another model and an
// error that names the pin and the fix.
func TestPinnedHeadlessNeverSwaps(t *testing.T) {
	h := rpPinHarness(t, nil)
	err := h.run(t, "oi")
	if err == nil {
		t.Fatal("a failed pin must fail the run")
	}
	if n := h.fs.countModel("kfree"); n != 0 {
		t.Fatalf("pinned run sent %d requests to the implicit chain", n)
	}
	var pe *acPinError
	if !errors.As(err, &pe) || pe.Ref != "fake/a:free" || pe.Alt != "kilo/kfree" {
		t.Fatalf("err = %#v", err)
	}
	msg := friendlyErr(err)
	for _, w := range []string{"a:free", "cota grátis", "não troquei de modelo sozinho", "-m kilo/kfree", "fallback"} {
		if !strings.Contains(msg, w) {
			t.Errorf("message %q: missing %q", msg, w)
		}
	}
	if strings.Contains(msg, "{") {
		t.Errorf("raw JSON: %q", msg)
	}
	if StopModelUnavailable.ExitCode() != 75 {
		t.Fatal("a failed pin exits 75")
	}
	// unpinned, the same failure moves on — and says so
	h2 := rpPinHarness(t, func(c *Config) { c.Pinned = false })
	if err := h2.run(t, "oi"); err != nil {
		t.Fatalf("unpinned: %v", err)
	}
	if h2.fs.countModel("kfree") != 1 || !anyContains(h2.ui.notices, "kfree") {
		t.Fatalf("unpinned swap must happen and be announced: notices %q warns %q", h2.ui.notices, h2.ui.warns)
	}
}

// Interactive: the pin failed, the user is asked; yes = this turn only.
func TestPinnedInteractiveAsks(t *testing.T) {
	h := rpPinHarness(t, nil)
	h.ag.Interactive = true
	h.ui.answer = Allow
	if err := h.run(t, "oi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(h.ui.approvals) != 1 || h.ui.approvals[0].Tool != "modelo" || !strings.Contains(h.ui.approvals[0].Detail, "Usar kfree neste turno?") {
		t.Fatalf("approvals = %+v", h.ui.approvals)
	}
	if lm := lastMsg(h.ag.Msgs); lm.Content != "resposta de kfree" {
		t.Fatalf("final = %+v", lm)
	}
	if !h.cfg.Pinned {
		t.Fatal("\"yes\" is for this turn: the pin stays")
	}
	// a new turn starts from the pin again (it is still in cota: asks again)
	if err := h.run(t, "de novo"); err != nil {
		t.Fatal(err)
	}
	if len(h.ui.approvals) != 2 {
		t.Fatalf("second turn must ask again: %d approvals", len(h.ui.approvals))
	}

	// "não": the run fails, nothing else is asked
	h2 := rpPinHarness(t, nil)
	h2.ag.Interactive = true
	h2.ui.answer = Deny
	if err := h2.run(t, "oi"); err == nil || h2.fs.countModel("kfree") != 0 {
		t.Fatalf("declined: err %v, kfree %d", err, h2.fs.countModel("kfree"))
	}

	// "sempre": unpinned for the session
	h3 := rpPinHarness(t, nil)
	h3.ag.Interactive = true
	h3.ui.answer = AllowAlways
	if err := h3.run(t, "oi"); err != nil {
		t.Fatal(err)
	}
	if h3.cfg.Pinned || !anyContains(h3.ui.notices, "troco sozinho") {
		t.Fatalf("always: pinned=%v notices %q", h3.cfg.Pinned, h3.ui.notices)
	}
}

// fallback_on_pinned: auto switches on its own and announces it; --strict-model
// never uses even the configured fallback.
func TestPinnedPolicies(t *testing.T) {
	h := rpPinHarness(t, func(c *Config) { c.FallbackOnPinned = "auto" })
	if err := h.run(t, "oi"); err != nil {
		t.Fatalf("auto: %v", err)
	}
	if h.fs.countModel("kfree") != 1 || !anyContains(h.ui.notices, "kfree") {
		t.Fatalf("auto: swap must be announced: %q", h.ui.notices)
	}

	h2 := rpPinHarness(t, func(c *Config) {
		c.StrictModel = true
		c.Fallback = []string{"kilo/kfree"}
	})
	h2.ag.Interactive = true
	h2.ui.answer = Allow
	if err := h2.run(t, "oi"); err == nil || h2.fs.countModel("kfree") != 0 || len(h2.ui.approvals) != 0 {
		t.Fatalf("strict: err %v kfree %d approvals %d", err, h2.fs.countModel("kfree"), len(h2.ui.approvals))
	}

	// the configured fallback is consent: used without asking, and said
	h3 := rpPinHarness(t, func(c *Config) { c.Fallback = []string{"kilo/kfree"} })
	h3.ag.Interactive = true
	if err := h3.run(t, "oi"); err != nil {
		t.Fatalf("configured fallback: %v", err)
	}
	if len(h3.ui.approvals) != 0 || !anyContains(h3.ui.warns, "→ tentando kfree") {
		t.Fatalf("configured fallback: approvals %d warns %q", len(h3.ui.approvals), h3.ui.warns)
	}
}

// A dead pin is said before the request and tried anyway.
func TestPinnedDeadIsWarnedAndTried(t *testing.T) {
	h := newHarness(t, seq(fakeResp{Text: "voltei"}), func(c *Config) {
		c.Model = "fake/a"
		c.Pinned = true
	})
	h.ag.Model = "fake/a"
	MarkExhausted("fake/a", time.Now().Add(2*time.Hour))
	if err := h.run(t, "oi"); err != nil {
		t.Fatal(err)
	}
	if h.fs.countModel("a") != 1 || !anyContains(h.ui.warns, "tentando assim mesmo") {
		t.Fatalf("pin must be tried with a warning: reqs %d warns %q", h.fs.countModel("a"), h.ui.warns)
	}
}

// Unpinned: the requested model dropped before any request is announced
// (with the reason), once.
func TestDeadPrimaryDropIsAnnounced(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp { return fakeResp{Text: "de " + r.Model} }, func(c *Config) {
		c.Model = "fake/a"
		c.Fallback = []string{"fake/b"}
	})
	h.ag.Model = "fake/a"
	MarkExhausted("fake/a", time.Now().Add(2*time.Hour))
	if err := h.run(t, "oi"); err != nil {
		t.Fatal(err)
	}
	if err := h.run(t, "de novo"); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, w := range h.ui.warns {
		if strings.Contains(w, "a indisponível (cota esgotada até") && strings.Contains(w, "→ usando b") {
			n++
		}
	}
	if n != 2 || h.fs.countModel("a") != 0 {
		t.Fatalf("drop announced %d times (want once per turn: 2), a asked %d: %q", n, h.fs.countModel("a"), h.ui.warns)
	}
}

// With the judge on, a pin keeps the turn on the pin (39/238 turns of 0.13
// had their 1st request on another model).
func TestJudgeKeepsPinnedModel(t *testing.T) {
	t.Setenv("JAZZ_OMNI_JUDGE", "on")
	h := newHarness(t, seq(fakeResp{Text: "ok"}), func(c *Config) {
		c.Model = "fake/a"
		c.Pinned = true
		c.Roles.Editor = "fake/editor"
	})
	h.ag.Model = "fake/a"
	if m := h.ag.ojBeginTurn("corrige o bug no parser"); m != "" {
		t.Fatalf("pinned: judge picked %q", m)
	}
	h.cfg.Pinned = false
	if m := h.ag.ojBeginTurn("corrige o bug no parser"); m != "fake/editor" {
		t.Fatalf("unpinned: judge picked %q, want the configured editor", m)
	}
}

// ---------------------------------------------------------------------------
// F1.2: state read again per candidate (PROV-3)
// ---------------------------------------------------------------------------

// A quota 429 (kilo PROMOTION) cools the whole provider: the other models of
// the same provider get no request in the same step (406 cascades in 0.13).
func TestQuotaCascadeSkipsSameProvider(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if r.Model == "a" || r.Model == "b" {
			return fakeResp{Status: 429, Body: rcBodyPromo}
		}
		return fakeResp{Text: "de " + r.Model}
	}, func(c *Config) {
		u := c.Providers["fake"].BaseURL
		c.Providers["k"] = &ProviderConf{Type: "openai", BaseURL: u, NoKey: true}
		c.Providers["o"] = &ProviderConf{Type: "openai", BaseURL: u, NoKey: true}
		c.Model = "k/a"
		c.Fallback = []string{"k/b", "o/c"}
	})
	h.ag.Model = "k/a"
	if err := h.run(t, "oi"); err != nil {
		t.Fatal(err)
	}
	if got := len(h.fs.requests()); got != 2 || h.fs.countModel("b") != 0 {
		t.Fatalf("%d requests (b: %d), want exactly 2: k/a then o/c", got, h.fs.countModel("b"))
	}
	if !anyContains(h.ui.warns, "a cota grátis do provedor acabou") {
		t.Errorf("warns %q", h.ui.warns)
	}
}

// Everything down after one real failure: no request per dead ref.
func TestAllDownStopsAfterOneRequest(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp {
		return fakeResp{Status: 429, Body: rcBodyPromo}
	}, func(c *Config) {
		c.Model = "fake/a"
		c.Fallback = []string{"fake/b", "fake/c"}
	})
	h.ag.Model = "fake/a"
	if err := h.run(t, "oi"); err == nil {
		t.Fatal("want an error")
	}
	if got := len(h.fs.requests()); got != 1 {
		t.Fatalf("%d requests, want 1 (b and c are on the provider that just ran out)", got)
	}
	if !anyContains(h.ui.warns, "parei aqui pra não gastar pedido à toa") {
		t.Errorf("warns %q", h.ui.warns)
	}
}

// A request bigger than a model's learned cap skips it while another fits
// (the 44 HTTP 413 of 0.13: the old filter read the state with 0 tokens).
func TestTooBigCandidateSkipped(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp { return fakeResp{Text: "de " + r.Model} }, func(c *Config) {
		c.Model = "fake/a"
		c.Fallback = []string{"fake/small", "fake/c"}
		c.Models = map[string]*ModelConf{"fake/small": {MaxReq: 10}}
	})
	h.ag.Model = "fake/a"
	h.ag.retryDelay = acFastRetry
	MarkExhausted("fake/a", time.Now().Add(time.Hour))
	if err := h.run(t, "oi"); err != nil {
		t.Fatal(err)
	}
	if h.fs.countModel("small") != 0 || h.fs.countModel("c") != 1 {
		t.Fatalf("small %d c %d", h.fs.countModel("small"), h.fs.countModel("c"))
	}
}

// ---------------------------------------------------------------------------
// F1.5: retry with a time budget (PROV-5)
// ---------------------------------------------------------------------------

func TestRetryBudgetStopsBackoff(t *testing.T) {
	old := acWaitBudget
	acWaitBudget = func(bool) time.Duration { return 50 * time.Millisecond }
	t.Cleanup(func() { acWaitBudget = old })
	h := newHarness(t, func(n int, r recReq) fakeResp {
		return fakeResp{Status: 503, Body: `{"error":{"message":"overloaded"}}`}
	}, nil)
	h.ag.retryDelay = func(error, int) time.Duration { return 30 * time.Millisecond }
	if err := h.run(t, "oi"); err == nil {
		t.Fatal("want an error")
	}
	if got := len(h.fs.requests()); got != 2 {
		t.Fatalf("%d requests, want 2 (the 2nd wait would pass the budget)", got)
	}
	if !anyContains(h.ui.notices, "m1: provedor instável (HTTP 503) — tentando de novo em") || !anyContains(h.ui.notices, "(2/6)") {
		t.Fatalf("notices %q", h.ui.notices)
	}
	for _, n := range h.ui.notices {
		if strings.Contains(n, "{") {
			t.Fatalf("raw JSON in the retry line: %q", n)
		}
	}
}

// ---------------------------------------------------------------------------
// telemetry: requested vs served
// ---------------------------------------------------------------------------

func TestTelemetryRequestedServed(t *testing.T) {
	cfg, root, fs := obHeadlessEnv(t, func(n int, r recReq) fakeResp {
		if r.Model == "m1" {
			return fakeResp{Status: 410, Body: rcBody410}
		}
		return fakeResp{Text: "ok de " + r.Model}
	})
	cfg.Fallback = []string{"fake/m2"}
	code, res := obRun(t, cfg, root, "oi", pfOpts{timeout: time.Minute})
	if code != 0 {
		t.Fatalf("exit %d res %v", code, res)
	}
	_ = fs
	sid, _ := res["session_id"].(string)
	si, err := pfFindSession(root, sid)
	if err != nil {
		t.Fatal(err)
	}
	var sawStart, sawClass, sawEnd bool
	for _, e := range obEvents(t, si.Path) {
		switch e.str("kind") {
		case "request.start":
			sawStart = sawStart || e.str("requested") == "fake/m1"
		case "request.end":
			sawClass = sawClass || e.str("class") == "gone"
		case "run.end":
			sawEnd = e.str("requested") == "fake/m1" && e.flag("swapped")
		}
	}
	if !sawStart || !sawClass || !sawEnd {
		t.Fatalf("start %v class %v run.end %v", sawStart, sawClass, sawEnd)
	}
}

// headless pinned: exit 75, nothing asked to another provider.
func TestHeadlessPinnedExit75(t *testing.T) {
	cfg, root, fs := obHeadlessEnv(t, func(n int, r recReq) fakeResp {
		if r.Model == "m1" {
			return fakeResp{Status: 429, Body: rcBodyPromo}
		}
		return fakeResp{Text: "não devia"}
	})
	cfg.Providers["kilo"] = &ProviderConf{Type: "openai", BaseURL: fs.URL(), NoKey: true, DefaultModel: "kfree"}
	cfg.Model = "fake/m1:free"
	cfg.Pinned = true
	fs.handler = func(n int, r recReq) fakeResp {
		if r.Model == "m1:free" {
			return fakeResp{Status: 429, Body: rcBodyPromo}
		}
		return fakeResp{Text: "não devia"}
	}
	code, res := obRun(t, cfg, root, "oi", pfOpts{timeout: time.Minute})
	if code != 75 {
		t.Fatalf("exit %d, want 75; res %v", code, res)
	}
	if fs.countModel("kfree") != 0 {
		t.Fatal("headless pinned run swapped models")
	}
	if e, _ := res["error"].(string); !strings.Contains(e, "m1:free") || !strings.Contains(e, "não troquei") {
		t.Fatalf("error = %q", e)
	}
}

// Inside one turn the drop is said once, not once per step.
func TestDropAnnouncedOncePerTurn(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "c1", Name: "bash", Args: jsonArgs(map[string]any{"command": "echo oi"})}}},
		fakeResp{Text: "pronto"},
	), func(c *Config) {
		c.Model = "fake/a"
		c.Fallback = []string{"fake/b"}
		c.Mode = "yolo"
	})
	h.ag.Model = "fake/a"
	MarkExhausted("fake/a", time.Now().Add(2*time.Hour))
	if err := h.run(t, "roda echo"); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, w := range h.ui.warns {
		if strings.Contains(w, "a indisponível") {
			n++
		}
	}
	if len(h.fs.requests()) != 2 || n != 1 {
		t.Fatalf("%d requests, drop said %d times: %q", len(h.fs.requests()), n, h.ui.warns)
	}
}

// PROV-4: groq/gpt-oss-120b (177/577 ok in the main loop) is not part of the
// main agent's implicit chain; it stays for sub/fast while the request fits
// half its tokens-per-minute.
func TestImplicitChainGroqNotInMain(t *testing.T) {
	c := pvTestCfg(t)
	t.Setenv("GROQ_API_KEY", "gsk_x")
	has := func(got []string) bool {
		for _, r := range got {
			if strings.HasPrefix(r, "groq/") {
				return true
			}
		}
		return false
	}
	if got := c.CandidatesFor(Need{Role: "main"}, "openrouter/foo:free"); has(got) {
		t.Fatalf("main chain has groq: %v", got)
	}
	if got := c.CandidatesFor(Need{Role: "sub", Tokens: 2000}, "openrouter/foo:free"); !has(got) {
		t.Fatalf("sub chain lost groq: %v", got)
	}
	if got := c.CandidatesFor(Need{Role: "sub", Tokens: 6000}, "openrouter/foo:free"); has(got) {
		t.Fatalf("a request over half of groq's TPM (8000) must not go to groq: %v", got)
	}
}

// Pinned, the hedged compaction (judge + hedge on) does not race the user's
// conversation against the implicit free chain: the pin summarizes it.
func TestPinnedHedgedSummaryOff(t *testing.T) {
	t.Setenv("JAZZ_OMNI_JUDGE", "on")
	t.Setenv("JAZZ_HEDGE", "on")
	h := newHarness(t, func(n int, r recReq) fakeResp { return fakeResp{Text: "resumo de " + r.Model} }, func(c *Config) {
		c.Model = "fake/a:free"
		c.Providers["kilo"] = &ProviderConf{Type: "openai", BaseURL: c.Providers["fake"].BaseURL, NoKey: true, DefaultModel: "kfree"}
		c.Pinned = true
	})
	h.ag.Model = "fake/a:free"
	prefix := []Message{{Role: "user", Content: "conversa"}, {Role: "assistant", Content: "ok"}}
	if s, ok := h.ag.ojHedgedSummary(context.Background(), prefix); ok {
		t.Fatalf("pinned: hedged summary ran (%q)", s)
	}
	if n := len(h.fs.requests()); n != 0 {
		t.Fatalf("pinned: %d requests", n)
	}
	h.cfg.Pinned = false // control: unpinned, the hedge does run
	if _, ok := h.ag.ojHedgedSummary(context.Background(), prefix); !ok || h.fs.countModel("kfree") == 0 && h.fs.countModel("a:free") == 0 {
		t.Fatalf("unpinned: hedge ok=%v, requests %d", ok, len(h.fs.requests()))
	}
}

// Pinned, the compaction threshold follows the pin (and its fallback), not
// a small-window model of the implicit chain that will never answer.
func TestPinnedCompactThresholdFollowsPin(t *testing.T) {
	h := rpPinHarness(t, func(c *Config) {
		c.Models = map[string]*ModelConf{
			"fake/a:free": {Context: 1000000},
			"kilo/kfree":  {Context: 32000},
		}
	})
	pinned := h.ag.compactThreshold()
	if w := acThreshold(1000000, 0); pinned < w/2 {
		t.Fatalf("pinned threshold %d: the 32k implicit model leaked in", pinned)
	}
	h.cfg.Pinned = false
	if un := h.ag.compactThreshold(); un >= pinned {
		t.Fatalf("control: unpinned threshold %d should follow the 32k model (pinned %d)", un, pinned)
	}
}

// Unpinned, a reorder (fast model first) that keeps working is said once per
// session, not on every turn; a failure swap is still said every turn.
func TestReorderServedNoticeOncePerSession(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp { return fakeResp{Text: "de " + r.Model} }, func(c *Config) {
		c.Model = "fake/a"
		c.Fallback = []string{"fake/b"}
	})
	h.ag.Model = "fake/a"
	stall := map[string]time.Time{"fake/a": time.Now()} // a stalled: demoted behind b
	for i, in := range []string{"oi", "de novo", "mais uma"} {
		h.ag.stalled = stall
		if err := h.run(t, in); err != nil {
			t.Fatalf("turn %d: %v", i, err)
		}
	}
	if h.fs.countModel("b") != 3 {
		t.Fatalf("b served %d of 3 turns (a demoted: %d)", h.fs.countModel("b"), h.fs.countModel("a"))
	}
	n := 0
	for _, m := range h.ui.notices {
		if strings.Contains(m, "respondendo com b") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("reorder said %d times in 3 turns, want 1: %q", n, h.ui.notices)
	}
}
