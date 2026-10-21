package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLessonReuseRepoSigAndPick(t *testing.T) {
	a, b, c := t.TempDir(), t.TempDir(), t.TempDir()
	for _, d := range []string{a, b} {
		os.WriteFile(filepath.Join(d, "pedidos.py"), []byte("x"), 0o644)
		os.WriteFile(filepath.Join(d, "test_pedidos.py"), []byte("x"), 0o644)
	}
	os.WriteFile(filepath.Join(c, "go.mod"), []byte("module x"), 0o644)
	if lrRepoSig(a) == "" || lrRepoSig(a) != lrRepoSig(b) || lrRepoSig(a) == lrRepoSig(c) {
		t.Fatalf("signatures: %q %q %q", lrRepoSig(a), lrRepoSig(b), lrRepoSig(c))
	}
	class := lrTaskClass("corrija o bug no cálculo de desconto em pedidos.py")
	plans := []pcPlan{
		{ID: "p1", Task: "old", Kind: class, Repo: lrRepoSig(a), Wins: 1, Time: 1},
		{ID: "p2", Task: "newer", Kind: class, Repo: lrRepoSig(a), Wins: 1, Time: 2, Hint: "o desconto fica em PedidoA.calcular"},
		{ID: "p3", Task: "failing", Kind: class, Repo: lrRepoSig(a), Wins: 1, Fails: 1, Time: 3},
		{ID: "p4", Task: "go", Kind: class, Repo: lrRepoSig(c), Wins: 3, Time: 4},
	}
	p := lrPick(plans, nil, class, lrRepoSig(b))
	if p == nil || p.ID != "p2" {
		t.Fatalf("pick: %+v", p)
	}
	if p := lrPick(plans, []pcPlan{plans[1]}, class, lrRepoSig(b)); p == nil || p.ID != "p1" {
		t.Fatal("a plan the keyword match already injected is not repeated")
	}
	if lrPick(plans, nil, "S/question", lrRepoSig(b)) != nil {
		t.Fatal("other task class")
	}
	r := lrRender(*p)
	if !strings.Contains(r, "da última vez") || !strings.Contains(r, "PedidoA.calcular") || strings.Count(r, "\n") > 2 {
		t.Fatalf("render: %q", r)
	}
}

// The hint rides on the first user message of a session (never the system
// prompt, never a later turn) and only with lessons on.
func TestLessonReuseFirstTurnOnly(t *testing.T) {
	lsReset(t)
	h := newHarness(t, func(i int, r recReq) fakeResp { return fakeResp{Text: "feito"} }, nil)
	os.WriteFile(filepath.Join(h.root, "pedidos.py"), []byte("x = 1\n"), 0o644)
	req := "corrija o bug no cálculo de desconto em pedidos.py"
	plan := pcPlan{ID: "abc123", Time: 1, Task: "corrigir frete do PedidoB", Keys: []string{"frete"}, Kind: lrTaskClass(req), Repo: lrRepoSig(h.root),
		Steps: []string{"edit pedidos.py", "bash python3 -m unittest (exit 0)"}, Verify: "python3 -m unittest", Wins: 2, Session: "261020-000000-aaaa"}
	if err := pcSave(h.root, []pcPlan{plan}); err != nil {
		t.Fatal(err)
	}
	if err := h.run(t, req); err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	first := reqs[0]
	if strings.Contains(first.system(), "da última vez") {
		t.Fatal("the reuse hint must not go into the system prompt")
	}
	um := lastUser(first.Messages)
	if !strings.Contains(um, "da última vez") || !strings.Contains(um, "261020-000000-aaaa") || !strings.Contains(um, "python3 -m unittest") {
		t.Fatalf("first user message: %s", um)
	}
	if err := h.run(t, req); err != nil {
		t.Fatal(err)
	}
	reqs = h.fs.requests()
	if strings.Contains(lastUser(reqs[len(reqs)-1].Messages), "da última vez") {
		t.Fatal("hint repeated on a later turn")
	}

	lsState.Store(2) // --lessons off
	h2 := newHarness(t, func(i int, r recReq) fakeResp { return fakeResp{Text: "feito"} }, nil)
	os.WriteFile(filepath.Join(h2.root, "pedidos.py"), []byte("x = 1\n"), 0o644)
	plan.Repo = lrRepoSig(h2.root)
	pcSave(h2.root, []pcPlan{plan})
	if err := h2.run(t, req); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(lastUser(h2.fs.requests()[0].Messages), "da última vez") {
		t.Fatal("--lessons off still adds the reuse hint")
	}
}

func lastUser(msgs []map[string]any) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i]["role"] == "user" {
			switch c := msgs[i]["content"].(type) {
			case string:
				return c
			default:
				return jsonArgs(c)
			}
		}
	}
	return ""
}
