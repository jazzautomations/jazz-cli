package main

import (
	"strings"
	"testing"
)

func TestNormalizeArgsBashArgv(t *testing.T) {
	cases := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"cmd": []any{"bash", "-lc", "ls -R"}}, "ls -R"},
		{map[string]any{"command": []any{"/bin/sh", "-c", "cat a | wc -l"}}, "cat a | wc -l"},
		{map[string]any{"command": []any{"ls", "-la", "my dir"}}, "ls -la 'my dir'"},
		{map[string]any{"command": []any{"ls", "-la", "|", "wc", "-l"}}, "ls -la | wc -l"},
		{map[string]any{"command": []any{"ls -la"}}, "ls -la"},
		{map[string]any{"command": `["bash","-c","uname -a"]`}, "uname -a"},
		{map[string]any{"command": []any{"echo", "it's"}}, `echo 'it'\''s'`},
		{map[string]any{"command": "[ -f x ] && echo ok"}, "[ -f x ] && echo ok"}, // a real test command stays
		{map[string]any{"command": "ls"}, "ls"},
		{map[string]any{"command": `[ "$x" ]`}, `[ "$x" ]`}, // test(1) that is also a 1-element JSON array
		{map[string]any{"command": `[ "-f" ] && echo ok`}, `[ "-f" ] && echo ok`},
	}
	for _, c := range cases {
		got, _ := argStr(normalizeArgs("bash", c.args), "command")
		if got != c.want {
			t.Errorf("%v: command = %q, want %q", c.args, got, c.want)
		}
	}
	// the raw-JSON path the tools and the gate both use
	if got, _ := argStr(acArgsMap("bash", `{"cmd":["bash","-lc","ls -R"]}`), "command"); got != "ls -R" {
		t.Errorf("acArgsMap: %q", got)
	}
}

func TestNormalizeArgsFuzzyKey(t *testing.T) {
	m := normalizeArgs("read", map[string]any{"path Metall": "a.txt"})
	if p, _ := argStr(m, "path"); p != "a.txt" {
		t.Errorf("dirty key not renamed: %v", m)
	}
	m = normalizeArgs("write", map[string]any{"path:": "a.txt", " content": "x"})
	if p, _ := argStr(m, "path"); p != "a.txt" {
		t.Errorf("path: not renamed: %v", m)
	}
	if c, _ := argStr(m, "content"); c != "x" {
		t.Errorf("' content' not renamed: %v", m)
	}
	m = normalizeArgs("bash", map[string]any{"command ": "ls"})
	if c, _ := argStr(m, "command"); c != "ls" {
		t.Errorf("'command ' not renamed: %v", m)
	}
}

func TestFuzzyKeyNoFalseRename(t *testing.T) {
	for _, k := range []string{"paths", "pathname", "path_list", "path2", "commands_list"} {
		m := normalizeArgs("read", map[string]any{k: "a.txt"})
		if _, ok := m["path"]; ok {
			t.Errorf("%q must not become path: %v", k, m)
		}
	}
	// two candidates: ambiguous, nothing renamed
	m := normalizeArgs("read", map[string]any{"path a": "x", "path b": "y"})
	if _, ok := m["path"]; ok {
		t.Errorf("ambiguous dirty keys must stay: %v", m)
	}
	// the name is there: the dirty key is left alone
	m = normalizeArgs("read", map[string]any{"path": "x", "path y": "y"})
	if p, _ := argStr(m, "path"); p != "x" {
		t.Errorf("existing path overwritten: %v", m)
	}
	// MCP tools are never touched
	m = normalizeArgs("mcp__x__y", map[string]any{"command": []any{"a", "b"}})
	if _, ok := m["command"].([]any); !ok {
		t.Errorf("mcp args changed: %v", m)
	}
}

func TestValidationErrHasExample(t *testing.T) {
	for tool, ex := range afExample {
		o := validationErr(tool, "missing required parameter 'x'", "{}")
		if !strings.Contains(o.Text, "Correct format: "+tool+" "+ex) {
			t.Errorf("%s: no example: %q", tool, o.Text)
		}
	}
	if o := validationErr("mcp__a__b", "bad", "{}"); strings.Contains(o.Text, "Correct format") {
		t.Errorf("no invented example for MCP tools: %q", o.Text)
	}
}

// An argv array with a destructive command reaches the permission gate as
// the same command line the shell would run: `rm -rf /` is caught, not a
// harmless-looking `["rm","-rf","/"]`.
func TestPermitSeesNormalizedCommand(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "c1", Name: "bash", Args: `{"command":["rm","-rf","/"]}`}}},
		fakeResp{Text: "ok"},
	), nil)
	if err := h.run(t, "limpa tudo"); err != nil {
		t.Fatal(err)
	}
	res := h.fs.requests()[1].toolResults()["c1"]
	if strings.Contains(res, "exit code") {
		t.Fatalf("rm -rf / ran: %q", res)
	}
	if cmd, _ := argStr(acArgsMap("bash", `{"command":["rm","-rf","/"]}`), "command"); cmd != "rm -rf /" {
		t.Errorf("gate sees %q", cmd)
	}
	seen := false
	for _, a := range h.ui.approvals {
		seen = seen || strings.Contains(a.Detail, "rm -rf /")
	}
	if !seen && !strings.Contains(strings.ToLower(res), "rm -rf /") {
		t.Errorf("the gate never saw the normalized command: approvals=%v res=%q", h.ui.approvals, res)
	}
}

// `bash {}`: re-asked once without a tool result in the history and
// without counting a step; a second empty call gets the error with the format.
func TestEmptyArgsRetryNoStep(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "e1", Name: "bash", Args: `{}`}}},
		fakeResp{Calls: []fakeCall{{ID: "b1", Name: "bash", Args: `{"command":"echo oi"}`}}},
		fakeResp{Text: "pronto"},
	), nil)
	if err := h.run(t, "roda echo oi"); err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	if len(reqs) != 3 {
		t.Fatalf("want 3 requests (empty, retry, answer), got %d", len(reqs))
	}
	if _, ok := reqs[1].toolResults()["e1"]; ok {
		t.Error("the empty call must not leave a tool result in the history")
	}
	if !strings.Contains(reqs[2].toolResults()["b1"], "oi") {
		t.Errorf("the retried call must run: %v", reqs[2].toolResults())
	}
	for _, m := range h.ag.Msgs {
		for _, tc := range m.ToolCalls {
			if tc.ID == "e1" {
				t.Error("the empty call stays out of the history")
			}
		}
	}

	// twice empty: the second one runs into the validation error with the example
	h2 := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "e1", Name: "bash", Args: `{}`}}},
		fakeResp{Calls: []fakeCall{{ID: "e2", Name: "bash", Args: `{}`}}},
		fakeResp{Text: "desisto"},
	), nil)
	if err := h2.run(t, "roda echo oi"); err != nil {
		t.Fatal(err)
	}
	r := h2.fs.requests()
	if got := r[len(r)-1].toolResults()["e2"]; !strings.Contains(got, `Correct format: bash {"command":"ls -la"}`) {
		t.Errorf("second empty call: error with example, got %q", got)
	}
}

// The 2nd byte-identical call that already failed validation does not run.
func TestIdenticalFailingCallShortCircuit(t *testing.T) {
	bad := `{"offset":3}`
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "r1", Name: "read", Args: bad}}},
		fakeResp{Calls: []fakeCall{{ID: "r2", Name: "read", Args: bad}}},
		fakeResp{Text: "ok"},
	), nil)
	if err := h.run(t, "lê o arquivo"); err != nil {
		t.Fatal(err)
	}
	res := h.fs.requests()[2].toolResults()
	if !strings.Contains(res["r1"], "Validation failed") {
		t.Fatalf("first call: validation error, got %q", res["r1"])
	}
	if !strings.Contains(res["r2"], "TOOL_NOT_EXECUTED: this exact call already failed validation") || !strings.Contains(res["r2"], `Correct format: read {"path":"src/main.go"}`) {
		t.Errorf("second identical call must be short-circuited with the format: %q", res["r2"])
	}
	// a different (fixed) call runs normally afterwards
	if h.ag.badCalls == nil {
		t.Error("the refused call must be remembered for the turn")
	}
}

// The install hardening rewrites the command the gate saw, also when the
// model sent it as an argv array or under an alias key.
func TestReplaceCommandArgv(t *testing.T) {
	for _, raw := range []string{`{"command":["npm","install","left-pad"]}`, `{"cmd":"npm install left-pad"}`, `{"command":"npm install left-pad","timeout":60}`} {
		old, _ := argStr(acArgsMap("bash", raw), "command")
		got := acReplaceCommand(raw, old, "npm install --ignore-scripts left-pad")
		if c, _ := argStr(acArgsMap("bash", got), "command"); c != "npm install --ignore-scripts left-pad" {
			t.Errorf("%s: hardened command lost: %s", raw, got)
		}
	}
}

// argPath (subagent path lock, steering, obs) sees the path a dirty key
// carries, the same one the tool writes after normalizeArgs.
func TestArgPathSeesDirtyKey(t *testing.T) {
	if p := argPath(`{"path Metall":"a.go","content":"x"}`); p != "a.go" {
		t.Errorf("argPath = %q, want a.go", p)
	}
	if p := argPath(`{"paths":["a.go"]}`); p != "" {
		t.Errorf("paths is not path: %q", p)
	}
}
