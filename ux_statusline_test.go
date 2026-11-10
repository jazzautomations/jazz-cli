//go:build !windows

package main

// ux_statusline_test.go: offline visual parity (docs/research/ux-offline-parity.md)
// P1 statusline, P4 "conferi", P5 mode chip, P7 economy seal.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------- P5: mode chip (3 states, no alarm) ----------

func TestUXChipThreeStates(t *testing.T) {
	cases := []struct {
		name              string
		forced, on, flaky bool
		want              olChipState
		text, ascii       string
	}{
		{"online has no chip", false, false, false, olChipOnline, "", ""},
		{"forced", true, true, false, olChipForced, "⚡ offline · modo rápido · /online volta", "offline - modo rápido - /online volta"},
		{"dropped", false, true, false, olChipDown, "◌ sem rede · tentando em 12s · /online agora", "sem rede - tentando em 12s - /online agora"},
		{"flaky", false, true, true, olChipFlaky, "~ rede instável · ficando no offline · /online agora", "~ rede instável - ficando no offline - /online agora"},
	}
	for _, c := range cases {
		if got := olChipOf(c.forced, c.on, c.flaky); got != c.want {
			t.Errorf("%s: state %v, want %v", c.name, got, c.want)
		}
		if got := olChipText(c.want, false, 12*time.Second); got != c.text {
			t.Errorf("%s: text %q, want %q", c.name, got, c.text)
		}
		if got := olChipText(c.want, true, 12*time.Second); got != c.ascii {
			t.Errorf("%s: ascii %q, want %q", c.name, got, c.ascii)
		}
		if c.want != olChipOnline {
			if strings.Contains(olChipText(c.want, false, 12*time.Second), "⚠") {
				t.Errorf("%s: permanent alarm glyph", c.name)
			}
			if !strings.Contains(olChipText(c.want, false, 12*time.Second), "/online") {
				t.Errorf("%s: /online must stay visible", c.name)
			}
		}
	}
	// the drop chip never hides the network state or the probe countdown
	down := olChipText(olChipDown, false, 12*time.Second)
	for _, w := range []string{"sem rede", "12s", "/online agora"} {
		if !strings.Contains(down, w) {
			t.Errorf("drop chip misses %q: %q", w, down)
		}
	}
}

func TestUXChipFooter(t *testing.T) {
	app, _ := olTestApp(t)
	app.ui.T = Theme{}      // deterministic: Unicode glyphs, no colour
	app.mcp = &MCPManager{} // footer reads the server list

	app.ol = olState{forced: true}
	if out := stripANSI(app.footer()); !strings.Contains(out, "⚡ offline") || strings.Contains(out, "⚠") {
		t.Errorf("forced footer = %q", out)
	}

	app.ol = olState{on: true}
	app.olNext.Store(time.Now().Add(12 * time.Second).UnixNano())
	if out := stripANSI(app.footer()); !strings.Contains(out, "◌ sem rede") || !strings.Contains(out, "/online agora") {
		t.Errorf("drop footer = %q", out)
	}

	// flapping: two online↔offline transitions inside two probe intervals
	app.olSeen, app.olFlaps, app.olFlapAt = false, 0, time.Time{}
	app.ol = olState{}
	app.olNoteMode()
	app.ol = olState{on: true}
	app.olNoteMode()
	app.ol = olState{}
	app.olNoteMode()
	app.ol = olState{on: true}
	app.olNoteMode()
	if !app.olFlaky() {
		t.Fatalf("two transitions in a row must read flaky (flaps=%d)", app.olFlaps)
	}
	if out := stripANSI(app.footer()); !strings.Contains(out, "~ rede instável") || strings.Contains(out, "⚠") {
		t.Errorf("flaky footer = %q", out)
	}
}

// TERM=dumb / no Unicode: the chip degrades to ASCII and carries no escape.
func TestUXChipDumbTerminal(t *testing.T) {
	app, _ := olTestApp(t)
	app.ui.T = Theme{ascii: true, dumb: true}
	app.mcp = &MCPManager{}
	app.ol = olState{on: true}
	app.olNext.Store(time.Now().Add(12 * time.Second).UnixNano())
	out := app.footer()
	if strings.Contains(out, "\x1b") {
		t.Errorf("dumb footer has escapes: %q", out)
	}
	for _, g := range []string{"⚡", "◌", "⚠", "·"} { // no fancy glyphs in ascii
		if strings.Contains(out, g) {
			t.Fatalf("dumb footer has glyph %q: %q", g, out)
		}
	}
	if !strings.Contains(out, "sem rede") || !strings.Contains(out, "/online agora") {
		t.Errorf("dumb footer = %q", out)
	}
}

// ---------- P5/P7: startup line names the mode, not a model promise ----------

func TestUXOfflineStartLine(t *testing.T) {
	app, _ := olTestApp(t)
	app.ui.T = Theme{}

	app.ol = olState{forced: true}
	out := stripANSI(app.offlineStartLine())
	for _, w := range []string{"offline", "atalhos determinísticos", "cérebro ativos", "/online volta"} {
		if !strings.Contains(out, w) {
			t.Errorf("start line misses %q: %q", w, out)
		}
	}
	if strings.Contains(out, "modelo") || strings.Contains(out, "por resposta") {
		t.Errorf("start line still promises a model: %q", out)
	}

	app.ol = olState{on: true} // dropped: never hide "sem rede"
	if out := stripANSI(app.offlineStartLine()); !strings.Contains(out, "sem rede") {
		t.Errorf("dropped start line = %q", out)
	}
}

// ---------- P7: economy seal, once per turn ----------

func TestUXEconomySeal(t *testing.T) {
	if got := olEconomySeal(false); got != "⚡ local · 0 tokens" {
		t.Errorf("seal = %q", got)
	}
	if got := olEconomySeal(true); strings.Contains(got, "⚡") || !strings.Contains(got, "0 tokens") {
		t.Errorf("ascii seal = %q", got)
	}
}

func TestUXOfflineTurnSealOnce(t *testing.T) {
	app, _, read := offTestApp(t)
	app.ui.T = Theme{ascii: true} // deterministic glyphs for the count
	app.ol = olState{on: true, forced: true}

	handled, _ := app.offlineTurn(context.Background(), "lista os arquivos")
	if !handled {
		t.Fatal("offline request was not handled")
	}
	if app.ui.spinOn {
		t.Error("status line left running after the turn")
	}
	app.ui.mu.Lock()
	label := app.ui.spinLabel
	app.ui.mu.Unlock()
	if label != "offline · interpretando" {
		t.Errorf("status line label = %q, want the offline phase", label)
	}
	if n := strings.Count(stripANSI(read()), "0 tokens"); n != 1 {
		t.Errorf("economy seal printed %d times, want 1", n)
	}
}

// ---------- P4: "conferi" proof in human language ----------

func TestUXConferiProof(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte(strings.Repeat("x", 49)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	newCtx := func() *brCtx {
		return &brCtx{env: &brEnv{Root: root}, task: &TaskState{Slots: map[string]string{}}, vars: map[string]any{}}
	}

	cx := newCtx()
	m := mhMethod{Verify: []mhCheck{{Check: "file_exists", Arg: "README.md"}, {Check: "file_exists", Arg: "docs"}}}
	if ok, unavailable, why := brVerify(cx, m); !ok || unavailable {
		t.Fatalf("brVerify = ok %v unavailable %v why %q", ok, unavailable, why)
	}
	out := cx.out.String()
	for _, w := range []string{"conferi: README.md existe (49 bytes)", "conferi: docs existe (pasta)"} {
		if !strings.Contains(out, w) {
			t.Errorf("proof misses %q:\n%s", w, out)
		}
	}

	// no check passed, no "conferi": the verdict must never outrun the proof
	cx = newCtx()
	if ok, _, _ := brVerify(cx, mhMethod{Verify: []mhCheck{{Check: "file_exists", Arg: "nao_existe.txt"}}}); ok {
		t.Fatal("missing file verified")
	}
	if strings.Contains(cx.out.String(), "conferi:") {
		t.Errorf("failed check printed a proof:\n%s", cx.out.String())
	}

	// commands and tests
	cx = newCtx()
	cx.vars["res"] = brTestRes{Exit: 0, Cmd: "go test ./...", Parsed: &opResult{Data: opData{Kind: "test", Passed: 42, Total: 42}}}
	m = mhMethod{Verify: []mhCheck{{Check: "tests_passed", Arg: "$res"}}}
	if ok, _, why := brVerify(cx, m); !ok {
		t.Fatalf("tests_passed: %q", why)
	}
	out = cx.out.String()
	if !strings.Contains(out, "conferi: 42 teste(s) passaram · 0 falhas") {
		t.Errorf("tests proof:\n%s", out)
	}

	cx = newCtx()
	cx.vars["g"] = brCmdRes{Cmd: "git status --short", Exit: 0}
	if ok, _, _ := brVerify(cx, mhMethod{Verify: []mhCheck{{Check: "exit_zero", Arg: "$g"}}}); !ok {
		t.Fatal("exit_zero failed")
	}
	if !strings.Contains(cx.out.String(), "conferi: `git status --short` saiu com exit 0") {
		t.Errorf("cmd proof:\n%s", cx.out.String())
	}

	cx = newCtx()
	cx.vars["g"] = brCmdRes{Cmd: "systemctl status x", Exit: 3}
	if ok, _, _ := brVerify(cx, mhMethod{Risk: "low", Verify: []mhCheck{{Check: "cmd_ran", Arg: "$g"}}}); !ok {
		t.Fatal("cmd_ran: a non-zero informative answer still ran")
	}
	if !strings.Contains(cx.out.String(), "conferi: `systemctl status x` respondeu (exit 3)") {
		t.Errorf("cmd_ran proof:\n%s", cx.out.String())
	}
}

// A real offline turn prints the proof above the status line.
func TestUXConferiInBrainAnswer(t *testing.T) {
	app, _, read := offTestApp(t)
	if err := os.WriteFile(filepath.Join(app.root, "README.md"), []byte("# jazz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	app.ol = olState{on: true, forced: true}
	app.offlineTurn(context.Background(), "mostra o README.md")
	out := stripANSI(read())
	if !strings.Contains(out, "conferi: README.md existe (7 bytes)") {
		t.Errorf("brain answer has no proof line:\n%s", out)
	}
}

// ---------- P1: headless liveness as a status event ----------

func TestUXJSONStatusEvents(t *testing.T) {
	var buf bytes.Buffer
	j := NewJSONUI(&buf, true)
	j.Thinking("offline: interpretando")
	j.StopThinking()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("events = %q", buf.String())
	}
	var run, idle map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &run); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &idle); err != nil {
		t.Fatal(err)
	}
	if run["type"] != "status" || run["state"] != "running" || run["text"] != "offline: interpretando" {
		t.Errorf("running event = %v", run)
	}
	if idle["type"] != "status" || idle["state"] != "idle" {
		t.Errorf("idle event = %v", idle)
	}

	var quiet bytes.Buffer // buffered json stays one result object
	j = NewJSONUI(&quiet, false)
	j.Thinking("x")
	j.StopThinking()
	if quiet.Len() != 0 {
		t.Errorf("buffered json emitted %q", quiet.String())
	}
}

func TestEachCleanVerify(t *testing.T) {
	newCtx := func() *brCtx {
		return &brCtx{env: &brEnv{Root: t.TempDir()}, task: &TaskState{Slots: map[string]string{}}, vars: map[string]any{}}
	}
	chk := func() mhMethod { return mhMethod{Verify: []mhCheck{{Check: "each_clean", Arg: "$fe"}}} }

	// in-process int counts
	cx := newCtx()
	cx.vars["fe"] = map[string]any{"data": map[string]any{"done": 2, "failed": 0, "skipped": 0, "total": 2}}
	if ok, _, why := brVerify(cx, chk()); !ok {
		t.Fatalf("int counts: %q", why)
	}
	if !strings.Contains(cx.out.String(), "conferi: for_each limpo: 2/2 processados") {
		t.Errorf("proof: %s", cx.out.String())
	}

	// JSON-decoded float64 counts
	cx = newCtx()
	cx.vars["fe"] = map[string]any{"data": map[string]any{"done": 2.0, "failed": 0.0, "skipped": 0.0, "total": 2.0}}
	if ok, _, _ := brVerify(cx, chk()); !ok {
		t.Fatal("float64 counts rejected")
	}

	// serialized string counts (evidence round-trip)
	cx = newCtx()
	cx.vars["fe"] = map[string]any{"data": map[string]any{"done": "2", "failed": "0", "skipped": "0", "total": "2"}}
	if ok, _, _ := brVerify(cx, chk()); !ok {
		t.Fatal("string counts rejected")
	}

	// vacuous success must fail: zero items processed
	cx = newCtx()
	cx.vars["fe"] = map[string]any{"data": map[string]any{"done": 0, "failed": 0, "skipped": 0, "total": 0}}
	if ok, _, _ := brVerify(cx, chk()); ok {
		t.Fatal("empty for_each verified")
	}

	// partial failure must fail
	cx = newCtx()
	cx.vars["fe"] = map[string]any{"data": map[string]any{"done": 1, "failed": 1, "skipped": 0, "total": 2}}
	if ok, _, _ := brVerify(cx, chk()); ok {
		t.Fatal("for_each with failure verified")
	}

	// missing var / wrong shape must fail, not panic
	cx = newCtx()
	if ok, _, _ := brVerify(cx, chk()); ok {
		t.Fatal("missing $fe verified")
	}
	cx.vars["fe"] = "lixo"
	if ok, _, _ := brVerify(cx, chk()); ok {
		t.Fatal("garbage $fe verified")
	}
}
