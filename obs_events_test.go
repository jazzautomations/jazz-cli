package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// obHeadlessEnv isolates a headless run: temp home, fake provider, yolo
// (the temp folder is untrusted) and stdout captured to a file.
func obHeadlessEnv(t *testing.T, h func(int, recReq) fakeResp) (*Config, string, *fakeServer) {
	t.Helper()
	t.Setenv("JAZZ_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	fs := newFakeServer(t, h)
	cfg := &Config{
		Model: "fake/m1", Mode: "yolo", MaxSteps: 20, ContextWindow: 128000, MaxTokens: 1000,
		Providers: map[string]*ProviderConf{"fake": {Type: "openai", BaseURL: fs.URL(), NoKey: true}},
	}
	return cfg, t.TempDir(), fs
}

// obRun runs jazz -p and returns the exit code and the JSON result object.
func obRun(t *testing.T, cfg *Config, root, prompt string, o pfOpts) (int, map[string]any) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "stdout.json")
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = f
	o.format = "json"
	code := runHeadless(cfg, root, cfg.Model, prompt, o)
	os.Stdout = old
	f.Close()
	b, _ := os.ReadFile(out)
	var res map[string]any
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	_ = json.Unmarshal([]byte(lines[len(lines)-1]), &res)
	return code, res
}

func obEvents(t *testing.T, sessPath string) []obEvent {
	t.Helper()
	evs, err := obReadEvents(obEventsPath(sessPath))
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func obKinds(evs []obEvent) map[string]int {
	m := map[string]int{}
	for _, e := range evs {
		m[e.str("kind")]++
	}
	return m
}

func obHasGit() bool { _, err := exec.LookPath("git"); return err == nil }

// The deadline grace stops the run before the hard timeout: the tool in
// flight finishes, nothing new is requested, the run ends incomplete with a
// checkpoint and a handoff note, and a resume continues exactly there.
func TestObsDeadlineGraceThenResume(t *testing.T) {
	cfg, root, fs := obHeadlessEnv(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "c1", Name: "bash", Args: jsonArgs(map[string]any{"command": "sleep 1.5; echo 52 passed | tee progress.txt"})},
			{ID: "c2", Name: "bash", Args: jsonArgs(map[string]any{"command": "echo should-not-run > never.txt"})}}},
		fakeResp{Text: "should never be requested"},
	))
	o := pfOpts{timeout: 3 * time.Second, grace: 2500 * time.Millisecond, metricsOut: filepath.Join(t.TempDir(), "m.json")}
	code, res := obRun(t, cfg, root, "make the tests pass", o)
	if code != 124 {
		t.Fatalf("exit %d, want 124 (incomplete by deadline); res=%v", code, res)
	}
	if res["outcome"] != obIncomplete || res["handoff"] == "" || res["resume"] == nil {
		t.Fatalf("result lacks outcome/handoff/resume: %v", res)
	}
	if n := len(fs.requests()); n != 1 {
		t.Fatalf("%d requests: no request may start after the grace point", n)
	}
	if b, err := os.ReadFile(filepath.Join(root, "progress.txt")); err != nil || !strings.Contains(string(b), "52 passed") {
		t.Fatalf("the in-flight tool must finish: %v %q", err, b)
	}
	if _, err := os.Stat(filepath.Join(root, "never.txt")); err == nil {
		t.Fatal("the second call of the batch must not start after the grace point")
	}
	sid, _ := res["session_id"].(string)
	si, err := pfFindSession(root, sid)
	if err != nil {
		t.Fatal(err)
	}
	end := sessLastEnd(si.Path)
	if end == nil || end.Outcome != obIncomplete || !strings.Contains(end.Note, "Resume note") {
		t.Fatalf("end record = %+v", end)
	}
	if obHasGit() && end.Checkpoint == "" {
		t.Error("no checkpoint taken at the abnormal end")
	}
	note, err := os.ReadFile(end.Handoff)
	if err != nil || !strings.Contains(string(note), "jazz -p -r "+sid) || !strings.Contains(string(note), "progress.txt") {
		t.Fatalf("handoff file: %v\n%s", err, note)
	}
	k := obKinds(obEvents(t, si.Path))
	for _, want := range []string{"session.start", "turn.start", "request.start", "request.end", "tool.start", "tool.end", "deadline.soft", "turn.end", "run.end"} {
		if k[want] == 0 {
			t.Errorf("event %s missing: %v", want, k)
		}
	}
	var m obSummary
	mb, _ := os.ReadFile(o.metricsOut)
	if json.Unmarshal(mb, &m) != nil || m.Outcome != obIncomplete || m.Requests != 1 || m.Tools != 1 || m.Exit != 124 {
		t.Fatalf("metrics = %s", mb)
	}

	// second attempt: resume, no prompt → continues from the note with history
	fs2 := newFakeServer(t, seq(fakeResp{Text: "all 54 tests pass now"}))
	cfg.Providers["fake"].BaseURL = fs2.URL()
	code, res = obRun(t, cfg, root, "", pfOpts{resumeID: sid, timeout: time.Minute})
	if code != 0 || res["outcome"] != obCompleted {
		t.Fatalf("resume: exit %d res %v", code, res)
	}
	reqs := fs2.requests()
	if len(reqs) != 1 {
		t.Fatalf("%d requests", len(reqs))
	}
	r := reqs[0]
	for _, want := range [][2]string{{"user", "Resume note from the harness"}, {"tool", "52 passed"}, {"user", "make the tests pass"}, {"tool", "TOOL_NOT_STARTED"}} {
		if !r.contains(want[0], want[1]) {
			t.Errorf("resume request lacks %s %q", want[0], want[1])
		}
	}
	if res["session_id"] != sid {
		t.Errorf("resume changed the session: %v", res["session_id"])
	}
	if e := sessLastEnd(si.Path); e == nil || e.Outcome != obCompleted {
		t.Errorf("end after resume = %+v", e)
	}
}

// A provider error is an abnormal end: incomplete, checkpoint, handoff.
func TestObsProviderErrorKeepsWork(t *testing.T) {
	cfg, root, _ := obHeadlessEnv(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "c1", Name: "write", Args: jsonArgs(map[string]any{"path": "a.txt", "content": "half done\n"})}}},
		fakeResp{Status: 400, Body: `{"error":{"message":"quota exhausted for today"}}`},
	))
	code, res := obRun(t, cfg, root, "write a.txt", pfOpts{timeout: time.Minute})
	if code != 1 || res["outcome"] != obIncomplete {
		t.Fatalf("exit %d res %v", code, res)
	}
	sid, _ := res["session_id"].(string)
	si, _ := pfFindSession(root, sid)
	evs := obEvents(t, si.Path)
	var failed obEvent
	for _, e := range evs {
		if e.str("kind") == "request.end" && e.str("error") != "" {
			failed = e
		}
	}
	if failed == nil || failed.int("status") != 400 || !strings.Contains(failed.str("payload"), "quota") {
		t.Fatalf("the failed request must be in the stream with its payload: %v", failed)
	}
	if e := sessLastEnd(si.Path); e == nil || e.Handoff == "" {
		t.Fatalf("end = %+v", e)
	}
	// audit: the write is recorded with before/after hashes, chain verifies
	var buf bytes.Buffer
	if cmdAudit([]string{"--kind", "file", "--verify"}, &buf) != 0 {
		t.Fatalf("audit: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "a.txt (create via write): ∅ → sha256:") || !strings.Contains(buf.String(), "cadeia íntegra") {
		t.Fatalf("audit output:\n%s", buf.String())
	}
}

// The spiral breaker (repeated blocked attempts) ends the run as denied
// (exit 5), still with a handoff and every refusal audited.
func TestObsDeniedRunIsAuditedAndHandedOff(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "elsewhere.txt")
	call := func(id string) fakeResp {
		return fakeResp{Calls: []fakeCall{{ID: id, Name: "write", Args: jsonArgs(map[string]any{"path": outside, "content": "x"})}}}
	}
	cfg, root, _ := obHeadlessEnv(t, seq(call("c1"), call("c2"), call("c3"), call("c4"), fakeResp{Text: "gave up"}))
	cfg.Mode = "auto"
	code, res := obRun(t, cfg, root, "write outside", pfOpts{timeout: time.Minute})
	if code != 5 || res["outcome"] != obDenied {
		t.Fatalf("exit %d res %v", code, res)
	}
	sid, _ := res["session_id"].(string)
	si, _ := pfFindSession(root, sid)
	k := obKinds(obEvents(t, si.Path))
	if k["guard"] < 3 {
		t.Fatalf("guard events = %d: %v", k["guard"], k)
	}
	var buf bytes.Buffer
	cmdAudit([]string{"--kind", "action"}, &buf)
	if strings.Count(buf.String(), "recusado [") < 3 {
		t.Fatalf("refusals not audited:\n%s", buf.String())
	}
	if e := sessLastEnd(si.Path); e == nil || e.Outcome != obDenied || e.Handoff == "" {
		t.Fatalf("end = %+v", e)
	}
}

// jazz trace and jazz sessions render a finished run.
func TestObsTraceAndSessions(t *testing.T) {
	cfg, root, _ := obHeadlessEnv(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "c1", Name: "bash", Args: jsonArgs(map[string]any{"command": "echo hi"})}}, UsageIn: 100, UsageOut: 20},
		fakeResp{Text: "done", UsageIn: 150, UsageOut: 10},
	))
	code, res := obRun(t, cfg, root, "say hi", pfOpts{timeout: time.Minute})
	if code != 0 {
		t.Fatalf("exit %d %v", code, res)
	}
	var tr bytes.Buffer
	if cmdTrace(root, nil, &tr) != 0 {
		t.Fatal(tr.String())
	}
	for _, want := range []string{"→ requisição #1 fake/m1", "← resposta em", "⎿ bash", "totais: 2 requisições", "in 250 out 30", "status done (concluída)"} {
		if !strings.Contains(tr.String(), want) {
			t.Errorf("trace lacks %q:\n%s", want, tr.String())
		}
	}
	var ss bytes.Buffer
	cmdSessions(root, nil, &ss)
	if !strings.Contains(ss.String(), "concluída") || !strings.Contains(ss.String(), res["session_id"].(string)) {
		t.Errorf("sessions:\n%s", ss.String())
	}
	var js bytes.Buffer
	cmdSessions(root, []string{"--json"}, &js)
	var rows []map[string]any
	if json.Unmarshal(js.Bytes(), &rows) != nil || len(rows) != 1 || rows[0]["tokens_in"].(float64) != 250 {
		t.Errorf("sessions --json: %s", js.String())
	}
}

// Every write leaves a traced commit: jazz changes names session, step, model.
func TestObsChangesAttributesEdits(t *testing.T) {
	if !obHasGit() {
		t.Skip("git missing")
	}
	cfg, root, _ := obHeadlessEnv(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "c1", Name: "write", Args: jsonArgs(map[string]any{"path": "main.go", "content": "package main\n"})}}},
		fakeResp{Calls: []fakeCall{{ID: "c2", Name: "bash", Args: jsonArgs(map[string]any{"command": "printf 'func main() {}\\n' >> main.go"})}}},
		fakeResp{Text: "done"},
	))
	code, res := obRun(t, cfg, root, "create main.go", pfOpts{timeout: time.Minute, loop: LoopOpts{Quality: "off"}})
	if code != 0 {
		t.Fatalf("exit %d %v", code, res)
	}
	sid := res["session_id"].(string)
	var buf bytes.Buffer
	if cmdChanges(root, []string{filepath.Join(root, "main.go")}, &buf) != 0 {
		t.Fatal(buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "sessão "+sid+" · passo 1 · fake/m1") || !strings.Contains(out, "sessão "+sid+" · passo 2 · fake/m1") {
		t.Fatalf("changes:\n%s", out)
	}
	// the bash change is audited with git blob ids
	var au bytes.Buffer
	cmdAudit([]string{"--kind", "file"}, &au)
	if !strings.Contains(au.String(), "(edit via bash): git:") {
		t.Errorf("bash change not audited:\n%s", au.String())
	}
}

// A tool result that finished but never reached the history (crash between
// the tool and the append) comes back from the write-ahead record.
func TestObsWriteAheadRecoversResult(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	s, err := NewSession(t.TempDir(), "fake/m1", "SYS")
	if err != nil {
		t.Fatal(err)
	}
	tc := ToolCall{ID: "z1", Name: "bash", Args: `{"command":"go test ./..."}`}
	s.Append(Message{Role: "user", Content: "fix it"})
	s.Append(Message{Role: "assistant", ToolCalls: []ToolCall{tc}})
	s.ToolStart(tc, 1)
	s.ToolResult(tc, 1, ToolOutput{Text: "ok  52 passed, 2 failed"}, time.Second)
	s.Close() // crash: the tool message is never written
	rs, msgs, _, _, err := LoadSession(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	rs.Close() // Windows cannot remove a file with an open handle
	last := msgs[len(msgs)-1]
	if last.Role != "tool" || !strings.Contains(last.Content, "52 passed") || !strings.Contains(last.Content, "write-ahead") {
		t.Fatalf("not recovered: %+v", last)
	}
}
