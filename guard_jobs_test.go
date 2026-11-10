//go:build !windows

package main

// guard_jobs_test.go: the honest-done guard for background jobs left running
// (guard_honest.go, item 7). Unix-only: the job launcher differs on Windows
// (startDetached/job object) and these tests spawn `sleep` through setsid.

import (
	"context"
	"strings"
	"testing"
)

func bgJobCall(id, cmd string) fakeCall {
	return fakeCall{ID: id, Name: "bash", Args: jsonArgs(map[string]any{"command": cmd, "background": true})}
}

func jobKillCall(id, job string) fakeCall {
	return fakeCall{ID: id, Name: "bash", Args: jsonArgs(map[string]any{"job": job, "action": "kill"})}
}

func countUserNotes(msgs []Message, marker string) int {
	n := 0
	for _, m := range msgs {
		if m.Role == "user" && strings.Contains(m.Content, marker) {
			n++
		}
	}
	return n
}

// The model starts a background job and says "done" with it alive: the
// reminder names the job (id/pid/command), the model kills it, the turn ends
// clean and no process is left.
func TestGuardJobsReminderThenKilled(t *testing.T) {
	h := newGuardHarness(t, nil,
		fakeResp{Calls: []fakeCall{bgJobCall("b1", "sleep 30")}},
		fakeResp{Text: "pronto, kill -TERM encerra o servidor"},
		fakeResp{Calls: []fakeCall{jobKillCall("b2", "j1")}},
		fakeResp{Text: "servidor parado"})
	t.Cleanup(func() { h.ag.tools.KillJobs() })
	stop, err := h.ag.Run(context.Background(), "Suba um servidor de teste, confira que responde e no fim encerre tudo: não pode sobrar nenhum processo dele.")
	if err != nil || stop != StopDone {
		t.Fatalf("stop=%s err=%v", stop, err)
	}
	reqs := h.fs.requests()
	if len(reqs) != 4 {
		t.Fatalf("requests=%d, want 4 (job, final, kill, final)", len(reqs))
	}
	last := reqs[2].last()
	c, _ := last["content"].(string)
	if last["role"] != "user" {
		t.Fatalf("the reminder rides as a user message, got %v", last["role"])
	}
	for _, want := range []string{"still running", "j1", "pid", "sleep 30", `action:"kill"`} {
		if !strings.Contains(c, want) {
			t.Errorf("jobs reminder lacks %q:\n%s", want, c)
		}
	}
	if n := countUserNotes(h.ag.Msgs, "still running"); n != 1 {
		t.Errorf("the jobs reminder fires exactly once, got %d", n)
	}
	if n := len(h.ag.tools.LiveJobs()); n != 0 {
		t.Errorf("the job must be dead at the end, live=%d", n)
	}
	if !anyContains(h.ui.notices, "guarda de conclusão") {
		t.Errorf("the user must see the guard note: %v", h.ui.notices)
	}
}

// The model ignores the reminder: the turn ends anyway (keeping a job is a
// legitimate choice) and the final status note names the live job.
func TestGuardJobsReminderIgnored(t *testing.T) {
	h := newGuardHarness(t, nil,
		fakeResp{Calls: []fakeCall{bgJobCall("b1", "sleep 30")}},
		fakeResp{Text: "pronto"},
		fakeResp{Text: "fica assim mesmo"})
	t.Cleanup(func() { h.ag.tools.KillJobs() })
	stop, err := h.ag.Run(context.Background(), "Suba o servidor e use.")
	if err != nil || stop != StopDone {
		t.Fatalf("stop=%s err=%v", stop, err)
	}
	if n := len(h.fs.requests()); n != 3 {
		t.Fatalf("requests=%d, want 3 (job, final, final)", n)
	}
	if n := countUserNotes(h.ag.Msgs, "still running"); n != 1 {
		t.Errorf("the jobs reminder fires exactly once, got %d", n)
	}
	live := h.ag.tools.LiveJobs()
	if len(live) != 1 {
		t.Fatalf("the job must still be running, live=%d", len(live))
	}
	if !anyContains(h.ui.warns, "j1") || !anyContains(h.ui.warns, "pid") {
		t.Errorf("the final status must name the live job (id/pid): %v", h.ui.warns)
	}
}

// "Deixa rodando" (or keep/leave it running): no reminder — the request asks
// for the job to stay up. The status note still says it is up.
func TestGuardJobsKeepRunningRequest(t *testing.T) {
	h := newGuardHarness(t, nil,
		fakeResp{Calls: []fakeCall{bgJobCall("b1", "sleep 30")}},
		fakeResp{Text: "servidor no ar"})
	t.Cleanup(func() { h.ag.tools.KillJobs() })
	stop, err := h.ag.Run(context.Background(), "Suba um servidor de teste em segundo plano e deixa rodando.")
	if err != nil || stop != StopDone {
		t.Fatalf("stop=%s err=%v", stop, err)
	}
	if n := len(h.fs.requests()); n != 2 {
		t.Fatalf("requests=%d, want 2 (job, final) — keep-running asks for no reminder", n)
	}
	if n := countUserNotes(h.ag.Msgs, "still running"); n != 0 {
		t.Errorf("no jobs reminder when the request says keep it running, got %d", n)
	}
	if !anyContains(h.ui.warns, "j1") {
		t.Errorf("the status note must still say a job is up: %v", h.ui.warns)
	}
}

// No background job: the guard changes nothing (no reminder, no job note).
func TestGuardJobsNoJobUnchanged(t *testing.T) {
	h := newGuardHarness(t, nil, fakeResp{Text: "ok"})
	stop, err := h.ag.Run(context.Background(), "diga oi")
	if err != nil || stop != StopDone {
		t.Fatalf("stop=%s err=%v", stop, err)
	}
	if n := len(h.fs.requests()); n != 1 {
		t.Fatalf("requests=%d, want 1", n)
	}
	if anyContains(h.ui.warns, "segundo plano") || countUserNotes(h.ag.Msgs, "still running") != 0 {
		t.Errorf("no job must mean no job note and no reminder: %v", h.ui.warns)
	}
}

func TestGuardJobsKeepRunningPhrases(t *testing.T) {
	yes := []string{
		"suba o servidor e deixa rodando",
		"deixe rodando",
		"mantém o servidor rodando",
		"pode deixar ele rodando",
		"fica de pé",
		"suba em segundo plano e deixa",
		"deixa o worker em background",
		"keep it running",
		"leave it running",
		"keep the server running",
		"let it run",
		"leave it in the background",
	}
	for _, s := range yes {
		if !hgKeepRunning(s) {
			t.Errorf("keep-running not detected: %q", s)
		}
	}
	no := []string{
		"suba o servidor e no fim pare: não pode sobrar nenhum processo rodando",
		"não deixe rodando",
		"don't leave it running",
		"pare o servidor",
		"rode os testes",
		"keep working on the parser",
		"let the tests run",
		"",
	}
	for _, s := range no {
		if hgKeepRunning(s) {
			t.Errorf("not a keep-running request: %q", s)
		}
	}
}
