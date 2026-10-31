package main

import (
	"context"
	"strings"
	"testing"
)

// TestBrainSlotSubstReachesCaps: a cap that reads in[k] without cx.str would
// see the literal "$slot.target". Regression: proc_kill did exactly that and
// "kill 1234" failed before the permission check. The deny evidence must
// name the resolved command.
func TestBrainSlotSubstReachesCaps(t *testing.T) {
	env := &brEnv{
		Root: t.TempDir(),
		Permit: func(ctx context.Context, cmd string) (bool, string) {
			if IsReadOnly(cmd) {
				return true, ""
			}
			return false, "pasta não confiável"
		},
		Run: func(ctx context.Context, cmd string) (string, int, bool) { return "", 0, false },
	}
	r := brRun(context.Background(), env, "kill 1234")
	if r.Status != tkBlocked {
		t.Fatalf("kill denied should end blocked, got %s (%q)", r.Status, r.Text)
	}
	joined := ""
	if r.Task != nil {
		for _, e := range r.Task.Evidence {
			joined += e.Text + "\n"
		}
	}
	if !strings.Contains(joined, "kill -TERM 1234") {
		t.Fatalf("the denied command must be the resolved one, evidence: %s", joined)
	}
	if strings.Contains(joined, "$slot") {
		t.Fatalf("a literal $slot reached the capability: %s", joined)
	}
}
