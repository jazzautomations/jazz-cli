package main

import "testing"

func TestRecoverClassify(t *testing.T) {
	o := func(f func(*rvObs)) rvObs { x := rvNoCmd(); f(&x); return x }
	cases := []struct {
		obs  rvObs
		want rvCat
	}{
		{o(func(x *rvObs) { x.Cands = 0 }), rvNameNotFound},
		{o(func(x *rvObs) { x.Cands, x.Tied = 3, 2 }), rvAmbiguous},
		{o(func(x *rvObs) { x.Exit = 127; x.Output = "sh: pytest: not found" }), rvCommandMissing},
		{o(func(x *rvObs) { x.Exit = 1; x.Output = "bash: cargo: command not found" }), rvCommandMissing},
		{o(func(x *rvObs) { x.Exit = 1; x.Parsed, x.Failures = true, 2 }), rvTestFailed},
		{o(func(x *rvObs) { x.TimedOut = true }), rvTimeout},
		{o(func(x *rvObs) { x.Exit = 124 }), rvTimeout},
		{o(func(x *rvObs) { x.Denied = true }), rvPermission},
		{o(func(x *rvObs) { x.Exit = 1; x.Output = "open /etc/x: permission denied" }), rvPermission},
		{o(func(x *rvObs) { x.EditStale = true }), rvEditConflict},
		{o(func(x *rvObs) { x.NoMethod = true }), rvPlanImpossible},
		{o(func(x *rvObs) { x.BudgetLeft = 0; x.Exit = 1 }), rvBudgetExhausted},
		{o(func(x *rvObs) { x.NoVerifier = true }), rvVerifierUnavailable},
		{o(func(x *rvObs) { x.Exit = 2 }), rvUnknown},
	}
	for i, c := range cases {
		if got := rvClassify(c.obs); got != c.want {
			t.Errorf("case %d: %s, want %s", i, got, c.want)
		}
	}
}

// Every category has a row, and the decisions follow its limits.
func TestRecoverTable(t *testing.T) {
	for _, c := range []rvCat{rvNameNotFound, rvAmbiguous, rvCommandMissing, rvTestFailed, rvTimeout, rvPermission, rvEditConflict, rvPlanImpossible, rvBudgetExhausted, rvVerifierUnavailable} {
		if rvRuleOf(c).Cat != c {
			t.Errorf("no row for %s", c)
		}
	}
	steps := []struct {
		cat     rvCat
		attempt int
		act     rvAction
		stop    bool
		then    tkStatus
	}{
		{rvNameNotFound, 0, rvAsk, true, tkNeedsInput},
		{rvCommandMissing, 0, rvNextMethod, false, ""},
		{rvCommandMissing, 1, rvSuggestInstall, true, tkBlocked},
		{rvCommandMissing, 3, rvSuggestInstall, true, tkBlocked},
		{rvTimeout, 0, rvRetrySmaller, false, ""},
		{rvTimeout, 2, rvRetrySmaller, true, tkBlocked},
		{rvPermission, 0, rvNoRetry, false, ""},
		{rvEditConflict, 0, rvRereadReapply, false, ""},
		{rvEditConflict, 2, rvRereadReapply, true, tkBlocked},
		{rvPlanImpossible, 0, rvExplainStop, true, tkBlocked},
		{rvBudgetExhausted, 0, rvHandoff, true, tkBlocked},
		{rvVerifierUnavailable, 0, rvMarkUnverified, true, tkUnverified},
		{rvTestFailed, 0, rvFixFailingTest, true, tkFailed},
	}
	for _, s := range steps {
		act, stop, then := rvDecide(s.cat, s.attempt)
		if act != s.act || stop != s.stop || then != s.then {
			t.Errorf("%s#%d = %s %v %s, want %s %v %s", s.cat, s.attempt, act, stop, then, s.act, s.stop, s.then)
		}
	}
	// "unverified" is never "succeeded"
	if rvRuleOf(rvVerifierUnavailable).Then == tkSucceeded {
		t.Fatal("verifier_unavailable must not end in succeeded")
	}
}

// The exploration spiral: N looks with nothing new → stop and ask; a new
// fact or a change resets the count.
func TestRecoverExplorationBudget(t *testing.T) {
	e := newRvExplore(3)
	if e.Observe("ls", true, "a.go\nb.go") {
		t.Fatal("a first look brings a fact")
	}
	if e.Observe("ls -la", true, "b.go\na.go") { // same fact, other order
		t.Fatal("stopped too early")
	}
	if e.Observe("find / -name x", true, "") {
		t.Fatal("stopped too early")
	}
	if !e.Observe("find / -name y", true, "   ") {
		t.Fatal("3 fruitless looks in a row must stop")
	}
	e2 := newRvExplore(2)
	e2.Observe("cat x", true, "")
	e2.Observe("go build", false, "") // a change resets
	if e2.Observe("cat y", true, "") {
		t.Fatal("a change resets the streak")
	}
	if e2.Observe("cat z", true, "new content") {
		t.Fatal("a new fact resets the streak")
	}
	if q := rvExploreQuestion(3); q == "" {
		t.Fatal("no question")
	}
}
