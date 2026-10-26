package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

var tkAll = []tkStatus{tkNew, tkInvestigating, tkNeedsInput, tkPlanning, tkReady, tkRunning, tkVerifying, tkRecovering, tkBlocked, tkSucceeded, tkUnverified, tkFailed, tkCancelled}

// The full table: every legal edge is accepted, every other one refused.
func TestTaskTransitionTable(t *testing.T) {
	legal := map[[2]tkStatus]bool{}
	for from, tos := range tkTransitions {
		for _, to := range tos {
			legal[[2]tkStatus{from, to}] = true
		}
	}
	for _, from := range tkAll {
		for _, to := range tkAll {
			want := !tkTerminal(from) && (legal[[2]tkStatus{from, to}] || to == tkCancelled)
			if got := tkCanMove(from, to); got != want {
				t.Errorf("%s → %s: got %v want %v", from, to, got, want)
			}
		}
	}
	// the spec's happy path and its branches
	paths := [][]tkStatus{
		{tkNew, tkInvestigating, tkPlanning, tkReady, tkRunning, tkVerifying, tkSucceeded},
		{tkNew, tkInvestigating, tkNeedsInput, tkPlanning, tkReady, tkRunning, tkVerifying, tkUnverified},
		{tkNew, tkInvestigating, tkPlanning, tkReady, tkRunning, tkRecovering, tkRunning, tkVerifying, tkFailed},
		{tkNew, tkInvestigating, tkPlanning, tkReady, tkRunning, tkRecovering, tkBlocked},
		{tkNew, tkInvestigating, tkBlocked},
		{tkNew, tkCancelled},
	}
	for _, p := range paths {
		tk := newTask("x", "open_file", nil, nil)
		tk.AddEvidence("note", "e")
		for _, s := range p[1:] {
			if err := tk.To(s, ""); err != nil {
				t.Fatalf("path %v: %v", p, err)
			}
		}
	}
}

func TestTaskSucceededNeedsEvidence(t *testing.T) {
	tk := newTask("x", "run_tests", nil, nil)
	for _, s := range []tkStatus{tkInvestigating, tkPlanning, tkReady, tkRunning, tkVerifying} {
		if err := tk.To(s, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := tk.To(tkSucceeded, ""); err == nil {
		t.Fatal("succeeded without evidence must be refused")
	}
	if tk.Status != tkVerifying {
		t.Fatal("a refused transition changes nothing")
	}
	if err := tk.To(tkUnverified, "sem verificador"); err != nil {
		t.Fatal(err)
	}
	if err := tk.To(tkInvestigating, ""); err == nil {
		t.Fatal("terminal states have no exits")
	}
}

// Transitions are session entries and events; jazz trace shows them.
func TestTaskPersistedAndTraced(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	root := t.TempDir()
	s, err := NewSession(root, "offline", "")
	if err != nil {
		t.Fatal(err)
	}
	l, err := obOpen(s.Path, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	obCur.Store(l)
	defer obCloseCur()
	s.Append(Message{Role: "user", Content: "abre o parsr"})
	tk := newTask("abre o parsr", "open_file", map[string]string{"target": "parsr"}, tkSessionSink(s))
	tk.must(tkInvestigating, "interpretado")
	tk.must(tkPlanning, "método open_file.by_name")
	tk.AddEvidence("file", "parser.go")
	tk.must(tkReady, "")
	tk.must(tkRunning, "")
	tk.must(tkVerifying, "")
	tk.must(tkSucceeded, "verificado")
	s.Close()
	b, _ := os.ReadFile(s.Path)
	if n := strings.Count(string(b), `"t":"task"`); n != 7 {
		t.Fatalf("session has %d task entries, want 7:\n%s", n, b)
	}
	ls, _, _, _, err := LoadSession(s.Path)
	if err != nil {
		t.Fatalf("a session with task entries must still load: %v", err)
	}
	ls.Close() // LoadSession reopens the file for append; Windows cannot remove it while open
	var out bytes.Buffer
	if code := cmdTrace(root, []string{s.ID}, &out); code != 0 {
		t.Fatalf("trace exit %d", code)
	}
	tr := out.String()
	for _, want := range []string{"tarefa " + tk.ID + " nova: open_file", "investigating → planning", "verifying → succeeded", "1 evidência"} {
		if !strings.Contains(tr, want) {
			t.Errorf("trace lacks %q:\n%s", want, tr)
		}
	}
}
