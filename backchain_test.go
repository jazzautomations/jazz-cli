package main

// backchain_test.go: unit tests for the backward-chaining planner in
// backchain.go. Each test builds a tiny STRIPS domain and checks both the
// boolean result and the exact deterministic plan.

import (
	"reflect"
	"testing"
)

// TestPlanTrivial: the goal already holds in the initial state, so the
// empty plan solves it even with zero depth budget.
func TestPlanTrivial(t *testing.T) {
	plan, ok := Plan([]string{"a"}, []string{"a"}, nil, 0)
	if !ok || len(plan) != 0 {
		t.Fatalf("Plan = %v, %v; want empty plan, true", plan, ok)
	}
}

// TestPlanSingleStep: one operator whose Add covers the goal.
func TestPlanSingleStep(t *testing.T) {
	ops := []Operator{
		{Name: "make-b", Add: []string{"b"}, Cost: 1},
	}
	plan, ok := Plan(nil, []string{"b"}, ops, 4)
	if !ok || !reflect.DeepEqual(plan, []string{"make-b"}) {
		t.Fatalf("Plan = %v, %v; want [make-b], true", plan, ok)
	}
}

// TestPlanChain: goal b is added by X, which needs a; a is added by Y.
// The plan must order them [Y, X].
func TestPlanChain(t *testing.T) {
	ops := []Operator{
		{Name: "X", Pre: []string{"a"}, Add: []string{"b"}, Cost: 1},
		{Name: "Y", Add: []string{"a"}, Cost: 1},
	}
	plan, ok := Plan(nil, []string{"b"}, ops, 4)
	if !ok || !reflect.DeepEqual(plan, []string{"Y", "X"}) {
		t.Fatalf("Plan = %v, %v; want [Y X], true", plan, ok)
	}
}

// TestPlanUnreachable: no operator produces the goal.
func TestPlanUnreachable(t *testing.T) {
	ops := []Operator{
		{Name: "X", Add: []string{"a"}, Cost: 1},
	}
	if plan, ok := Plan(nil, []string{"z"}, ops, 4); ok {
		t.Fatalf("Plan = %v, true; want false", plan)
	}
}

// TestPlanPrefersCheaper: of two paths to the goal, the planner keeps the
// one with the lowest TOTAL cost, not the cheapest first step.
func TestPlanPrefersCheaper(t *testing.T) {
	// Direct costs 5; the chain costs 1+1=2, so the chain wins.
	ops := []Operator{
		{Name: "direct", Add: []string{"g"}, Cost: 5},
		{Name: "via-h", Pre: []string{"h"}, Add: []string{"g"}, Cost: 1},
		{Name: "make-h", Add: []string{"h"}, Cost: 1},
	}
	plan, ok := Plan(nil, []string{"g"}, ops, 4)
	if !ok || !reflect.DeepEqual(plan, []string{"make-h", "via-h"}) {
		t.Fatalf("Plan = %v, %v; want [make-h via-h], true", plan, ok)
	}

	// Same shape, but the helper now costs 10: chain totals 11 > 5, so the
	// cheap-looking first step must be rejected for the direct operator.
	ops[2].Cost = 10
	plan, ok = Plan(nil, []string{"g"}, ops, 4)
	if !ok || !reflect.DeepEqual(plan, []string{"direct"}) {
		t.Fatalf("Plan = %v, %v; want [direct], true", plan, ok)
	}
}

// TestPlanDepthLimit: a 3-operator chain fails with maxDepth 2 and
// succeeds once the budget reaches 3.
func TestPlanDepthLimit(t *testing.T) {
	ops := []Operator{
		{Name: "step3", Pre: []string{"f2"}, Add: []string{"g"}, Cost: 1},
		{Name: "step2", Pre: []string{"f1"}, Add: []string{"f2"}, Cost: 1},
		{Name: "step1", Add: []string{"f1"}, Cost: 1},
	}
	if plan, ok := Plan(nil, []string{"g"}, ops, 2); ok {
		t.Fatalf("Plan(maxDepth=2) = %v, true; want false", plan)
	}
	plan, ok := Plan(nil, []string{"g"}, ops, 3)
	if !ok || !reflect.DeepEqual(plan, []string{"step1", "step2", "step3"}) {
		t.Fatalf("Plan(maxDepth=3) = %v, %v; want [step1 step2 step3], true", plan, ok)
	}
}
