package main

import "testing"

// Non-task gate: text with no task evidence (only fillers, greetings,
// profanity) never reaches the classifier — it is "none" with the
// "chitchat" hint. Before the gate, "porra" scored list_ports and similar
// noise fired real commands.
func TestNLUGateNonTask(t *testing.T) {
	m, w := nuTestWorld(t)
	for _, s := range []string{"porra", "responde só: ok", "oi"} {
		in := m.Parse(s, w)
		if in.Understood() && in.Name != "none" {
			t.Errorf("%q => %s conf=%.2f (%v), esperava não entendido", s, in.Name, in.Confidence, in.Evidence)
		}
	}
	// zero evidence: marked chitchat before the classifier runs
	for _, s := range []string{"porra", "oi"} {
		if in := m.Parse(s, w); in.Name != "none" || in.Hint != "chitchat" {
			t.Errorf("%q => name=%q hint=%q, esperava none/chitchat", s, in.Name, in.Hint)
		}
	}
	// real requests keep working: concept, entity name and tool words are
	// task evidence
	cases := map[string]string{
		"roda os testes":   "run_tests",
		"abre o README.md": "open_file",
		"git status":       "git_status",
	}
	for text, want := range cases {
		if in := m.Parse(text, w); !in.Understood() || in.Name != want {
			t.Errorf("%q => %s conf=%.2f (%v), want %s", text, in.Name, in.Confidence, in.Evidence, want)
		}
	}
}

// The split of a compound request discards parts with no task evidence, so
// noise cannot veto the split or become an action ("porra e o d610?" is one
// no-evidence part plus a name, not two requests).
func TestNLUGateSplitDiscardsNoise(t *testing.T) {
	m, w := nuTestWorld(t)
	if parts := brSplitMulti("porra e o d610?", m, w); len(parts) != 0 {
		t.Errorf("porra e o d610? => %#v, esperava split descartado", parts)
	}
	parts := brSplitMulti("git status e porra e roda os testes", m, w)
	if len(parts) != 2 || parts[0] != "git status" || parts[1] != "roda os testes" {
		t.Errorf("split com ruído => %#v, esperava [git status, roda os testes]", parts)
	}
	// two real tasks still split
	if parts := brSplitMulti("lista os arquivos e mostra o git status", m, w); len(parts) != 2 {
		t.Errorf("split de tarefas => %#v, esperava 2 partes", parts)
	}
}
