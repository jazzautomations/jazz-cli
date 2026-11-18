package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// d610, 17/11/2026, evals/weak with glm-4.7-flash and nemotron-3.5-lightning:
// both passed "sandbox": false on ordinary commands (python3 safra.py a b c,
// grep -c ERROR app.log). The refusal said "Do not retry it", so the model
// tried the next command the same way and the third refusal ended the turn as
// denied. The command itself is fine inside the sandbox: the refusal must say
// so, and it must not count as a blocked attempt that stops the turn.
func TestUnsandboxedDenialSteersBackIntoSandbox(t *testing.T) {
	usFakeSandbox(t)
	defer func(v bool) { usAllow = v }(usAllow)
	usAllow = false
	esc := func(id, cmd string) fakeResp {
		return fakeResp{Calls: []fakeCall{{ID: id, Name: "bash", Args: `{"command":"` + cmd + `","sandbox":false}`}}}
	}
	h := newHarness(t, seq(esc("b1", "python3 a.py"), esc("b2", "python3 b.py"), esc("b3", "python3 c.py"), fakeResp{Text: "fim"}),
		func(c *Config) { c.Mode = "yolo" })
	_ = h.run(t, "roda os scripts")

	if n := len(h.ui.approvals); n != 3 {
		t.Fatalf("every sandbox:false call is still a question: got %d approvals, want 3", n)
	}
	if n := len(h.fs.requests()); n != 4 {
		t.Errorf("requests = %d, want 4: three refused escapes must not end the turn", n)
	}
	raw, _ := json.Marshal(h.ag.Msgs)
	hist := string(raw)
	if !strings.Contains(hist, "without the sandbox argument") {
		t.Errorf("the refusal must tell the model to run the command again without the sandbox argument")
	}
	if strings.Contains(hist, "Do not retry it") {
		t.Errorf("the refusal must not tell the model to give the command up: it runs inside the sandbox")
	}
}
