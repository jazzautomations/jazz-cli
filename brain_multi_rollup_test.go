package main

import (
	"context"
	"strings"
	"testing"
)

// A step that ends needs_input stops the chain — no later step may run and
// the run-level event must carry needs_input, not a later step's verdict.
// (nb-1322: "Apagar qual arquivo?" step 1 → list_files.tree succeeded step 2
// → run sealed succeeded on a delete that never ran.)
func TestMultiStopsOnNeedsInput(t *testing.T) {
	root := brFixture(t)
	env, _ := brTestEnv(t, root, nil, 0)
	r := brRun(context.Background(), env,
		"remove all the zzz-unique-name files and then list files as tree")
	if r.Status == tkSucceeded {
		t.Fatalf("needs_input no passo 1 não pode sair verde\n%s", r.Text)
	}
	if strings.Contains(r.Text, "(parou") == false && r.Status == tkSucceeded {
		t.Fatalf("cadeia continuou após terminal ruim\n%s", r.Text)
	}
}

// The parent rollup event is the worst status — never a last-step green.
func TestMultiRollupWorstStatus(t *testing.T) {
	root := brFixture(t)
	env, _ := brTestEnv(t, root, nil, 0)
	r := brRun(context.Background(), env,
		"move all the files in the current folder to temp folder and search atleast in one subfolder")
	if r.Status != tkFailed && r.Status != tkNeedsInput && r.Status != tkBlocked && r.Status != tkUnverified {
		t.Fatalf("produtor vazio + move: status=%s\n%s", r.Status, r.Text)
	}
}

// An objective slot the winning method never consumes = a part of the
// request nobody executed → unverified, never green. (nb-0062: "saves to
// output.file" fell to list_files.tree after the write was blocked — the
// verify proved the tree, not the save.)
func TestSlotIgnoredUnverified(t *testing.T) {
	m := mhMethod{
		Name: "list_files.read",
		Steps: []mhStep{
			{Cap: "script:tree_summary", In: map[string]string{"path": "$slot.dir"}},
		},
		Verify: []mhCheck{{Check: "script_ok"}},
	}
	cx := &brCtx{task: &TaskState{Slots: map[string]string{"out": "output.file"}}}
	if got := brSlotIgnored(cx, m); got == "" {
		t.Fatal("out ignorado passou batido")
	}
	cx.task.Slots["out"] = ""
	cx.task.Slots["ext"] = "log" // filtro não-objetivo nunca reclama
	if got := brSlotIgnored(cx, m); got != "" {
		t.Fatalf("filtro não-objetivo reclamou: %s", got)
	}
}
