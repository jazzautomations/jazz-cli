package main

// backchain.go: pure, deterministic backward-chaining planner over STRIPS
// operators (Fikes & Nilsson 1971) — the engine behind the offline "chain
// 2-4 steps" mode. An operator whose Add covers an open goal is a *means*;
// its Pre becomes the new ends: means-ends analysis in the sense of Newell
// & Simon's GPS. The search regresses one open goal at a time, commits the
// chosen operator, and re-scans the goals until every one of them holds.
//
// Cost model: firing an operator costs Cost (assumed >= 0). Plan returns
// the minimum-total-cost plan of at most maxDepth operators, exploring
// candidates cheapest-first with Name as the stable tie-break, so the
// result is deterministic on a 32-bit, 1-CPU box.
//
// The state is a single factSet mutated in place: each branch pushes its
// effects as a delta stack and pops it on backtrack — no map copies.

import (
	"sort"
	"strings"
)

// Operator is one STRIPS operator: applicable when every fact in Pre is
// true; firing it adds Add to and removes Del from the state (Add wins
// when a fact is in both).
type Operator struct {
	Name string   // unique operator id; what Plan returns
	Pre  []string // preconditions: facts that must hold to fire
	Add  []string // add effects: facts made true
	Del  []string // delete effects: facts made false
	Cost int      // non-negative step cost
}

// factSet is the planner's mutable state: the facts that currently hold.
type factSet map[string]bool

// delta records one reversible change to a factSet.
type delta struct {
	fact string
	was  bool // value before the change
}

// apply fires op's effects and returns the delta stack that undoes them.
// Del runs before Add, so a fact listed in both ends true.
func (s factSet) apply(op *Operator) []delta {
	var d []delta
	for _, f := range op.Del {
		if s[f] {
			delete(s, f)
			d = append(d, delta{f, true})
		}
	}
	for _, f := range op.Add {
		if !s[f] {
			s[f] = true
			d = append(d, delta{f, false})
		}
	}
	return d
}

// undo reverses a delta stack, newest change first.
func (s factSet) undo(d []delta) {
	for i := len(d) - 1; i >= 0; i-- {
		if d[i].was {
			s[d[i].fact] = true
		} else {
			delete(s, d[i].fact)
		}
	}
}

// planner carries the search state shared across recursion.
type planner struct {
	ops    []Operator           // sorted by (Cost, Name)
	byName map[string]*Operator // replay committed steps by name
	addIdx map[string][]int     // fact -> indices of ops adding it, in sorted order
	state  factSet              // mutable; unchanged when any solve returns
	active map[string]bool      // goals on the current regress stack (cycle cut)
}

// Plan finds a sequence of operator names that, fired in order from
// initial, reaches a state where every fact in goals holds. It returns
// (plan, true) — an empty plan when the goals already hold — or
// (nil, false) when no plan of at most maxDepth operators exists.
//
// Search: means-ends backward chaining. Take the first open goal, try
// every operator that adds it (cheapest Cost first, Name breaking ties),
// recursively achieve its Pre, fire it, then re-scan the goals. A goal
// already on the regress stack fails the branch (cycle detection), and
// branch-and-bound keeps the cheapest total plan found. Every iteration
// order is fixed, so the answer is deterministic.
func Plan(initial []string, goals []string, ops []Operator, maxDepth int) ([]string, bool) {
	sorted := make([]Operator, len(ops))
	copy(sorted, ops)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := &sorted[i], &sorted[j]
		if a.Cost != b.Cost {
			return a.Cost < b.Cost
		}
		return a.Name < b.Name
	})

	p := &planner{
		ops:    sorted,
		byName: make(map[string]*Operator, len(sorted)),
		addIdx: make(map[string][]int, len(sorted)),
		state:  make(factSet, len(initial)+len(goals)),
		active: make(map[string]bool),
	}
	for _, f := range initial {
		p.state[f] = true
	}
	for i := range p.ops {
		op := &p.ops[i]
		p.byName[op.Name] = op
		for _, f := range op.Add {
			p.addIdx[f] = append(p.addIdx[f], i)
		}
	}

	plan, _, ok := p.solve(goals, maxDepth)
	if !ok {
		return nil, false
	}
	return plan, true
}

// solve returns the cheapest plan that makes every fact in goals true from
// the current state, plus its total cost; ok=false when none fits budget.
// p.state is restored before every return.
func (p *planner) solve(goals []string, budget int) (plan []string, cost int, ok bool) {
	// First open goal, in list order, keeps the search deterministic.
	g := ""
	for _, f := range goals {
		if !p.state[f] {
			g = f
			break
		}
	}
	if g == "" {
		return nil, 0, true // every goal already holds
	}
	if p.active[g] || budget <= 0 {
		return nil, 0, false // regress cycle or out of depth
	}
	p.active[g] = true

	var best []string
	bestCost := 0
	found := false
	for _, oi := range p.addIdx[g] {
		op := &p.ops[oi]
		if found && op.Cost >= bestCost {
			break // candidates are cost-sorted: nothing cheaper ahead
		}
		// Achieve op's preconditions, then commit pre-plan + op so the
		// remaining goals are solved from the advanced state.
		prePlan, preCost, ok := p.solve(op.Pre, budget-1)
		if !ok || (found && preCost+op.Cost >= bestCost) {
			continue
		}
		var d []delta
		for _, n := range prePlan {
			d = append(d, p.state.apply(p.byName[n])...)
		}
		d = append(d, p.state.apply(op)...)
		// The remaining goals are a fresh problem: this branch's regress
		// stack is resolved, and re-achieving a clobbered goal is legal
		// (that is what lets fixed goal order survive Sussman anomalies).
		saved := p.active
		p.active = make(map[string]bool, len(saved))
		restPlan, restCost, ok := p.solve(goals, budget-1-len(prePlan))
		p.active = saved
		p.state.undo(d)
		if !ok {
			continue
		}
		cost := preCost + op.Cost + restCost
		if !found || cost < bestCost {
			plan := make([]string, 0, len(prePlan)+1+len(restPlan))
			plan = append(plan, prePlan...)
			plan = append(plan, op.Name)
			plan = append(plan, restPlan...)
			best, bestCost, found = plan, cost, true
		}
	}
	delete(p.active, g)
	return best, bestCost, found
}

// ---------- wiring: plan chains inside the brain ----------

// brPlanChain synthesizes a chained method when the intent's own methods
// need a slot the parse left empty (in.Missing). Every method in the
// library becomes an operator: the ask variants are just expensive ops,
// so when a producer chain (refill → script → take) can derive the slot
// cheaper than asking, the planner returns e.g.
// [_fill.by_find, delete_file.op] and the request runs to the end.
// The synthesized method rides the normal executor — same permissions,
// evidence and verification — and a failed step drops back to the
// intent's regular candidates (the ask variant) exactly like today.
func brPlanChain(cx *brCtx, in Intent, methods []mhMethod) *mhMethod {
	if len(methods) == 0 {
		return nil
	}
	lib, err := mhLibrary()
	if err != nil {
		return nil
	}
	// "described" slots: a producer's re-read of the request yields the
	// filter it needs ("o arquivo mais novo" is a description, not a name)
	// — the slot then comes from the chain, not from the parsed word, so
	// it leaves the initial state even when the parse filled it literally.
	described := map[string]bool{}
	for i := range lib["_fill"] {
		fm := &lib["_fill"][i]
		viable := false
		for _, st := range fm.Steps {
			if st.Cap == "refill" && brRefillViable(cx, st.In) {
				viable = true
			}
			if viable && strings.HasPrefix(st.Out, "slot.") {
				described[st.Out] = true
			}
		}
	}
	if len(in.Missing) == 0 && len(described) == 0 {
		return nil
	}
	// A described slot's literal parse value is the description's own noun
	// ("downloads" in "o arquivo mais novo da pasta downloads"), never the
	// target — hide it from every method so a failed or missing chain asks
	// instead of running `rm downloads`.
	for f := range described {
		delete(cx.task.Slots, strings.TrimPrefix(f, "slot."))
		delete(cx.facts, f)
	}
	goal := "done." + in.Name
	var ops []Operator
	byName := map[string]*mhMethod{}
	unsetFacts := map[string]bool{}
	for _, ms := range lib {
		for i := range ms {
			m := &ms[i]
			// producers and the intent's own methods; unrelated intents
			// only add done facts nobody asks for, but keeping them out
			// also keeps a foreign ask from hijacking the question text
			if m.Intent != in.Name && !strings.HasPrefix(m.Intent, "_") {
				continue
			}
			op := Operator{Name: m.Name, Cost: 10 * len(m.Steps)}
			ok := true
			for _, n := range m.Needs {
				op.Pre = append(op.Pre, n)
			}
			for _, c := range m.Pre {
				switch c.Op {
				case "set", "":
					op.Pre = append(op.Pre, c.Fact)
				case "unset":
					op.Pre = append(op.Pre, "no."+c.Fact)
					unsetFacts[c.Fact] = true
				default:
					ok = false // eq/ne: the planner's set model can't see values
				}
			}
			if !ok {
				continue
			}
			for _, st := range m.Steps {
				if st.Cap == "ask_slot" {
					op.Cost += 40 // asking the owner is the expensive way to fill a slot
				}
				if st.Out == "" {
					continue
				}
				if strings.HasPrefix(st.Out, "slot.") {
					op.Add = append(op.Add, st.Out)
					op.Del = append(op.Del, "no."+st.Out)
				} else {
					op.Add = append(op.Add, "have."+st.Out)
				}
			}
			op.Add = append(op.Add, "done."+m.Intent)
			op.Cost += m.CostMS / 500
			ops = append(ops, op)
			byName[m.Name] = m
		}
	}
	var initial []string
	for f := range cx.facts {
		if described[f] {
			continue
		}
		initial = append(initial, f)
	}
	for f := range unsetFacts {
		if cx.facts[f] == "" || described[f] {
			initial = append(initial, "no."+f)
		}
	}
	plan, ok := Plan(initial, []string{goal}, ops, 4)
	if !ok || len(plan) < 2 {
		return nil // no chain beats the intent's own single method
	}
	last := byName[plan[len(plan)-1]]
	var steps []mhStep
	cost := 0
	for _, n := range plan {
		m := byName[n]
		if m == nil {
			return nil
		}
		steps = append(steps, m.Steps...)
		cost += m.CostMS
	}
	return &mhMethod{
		Name:     "plan:" + strings.Join(plan, "+"),
		Intent:   in.Name,
		Doc:      "cadeia: " + strings.Join(plan, " → "),
		Priority: 99,
		Steps:    steps,
		Verify:   last.Verify,
		Risk:     last.Risk,
		CostMS:   cost,
	}
}

// brRefillViable reports whether a refill step would satisfy its need
// groups — the plan-time mirror of brRefill's runtime check. The producer
// only earns a place in the chain when the request actually carries the
// filter words ("mais novo", ".log", "10MB"); otherwise it stays a guess.
func brRefillViable(cx *brCtx, in map[string]string) bool {
	intent := in["intent"]
	w := cx.env.World
	if w == nil {
		w = nuRuntimeWorld(cx.env.Root, cx.env.Profile)
	}
	slots := nuGet().FillAs(intent, cx.task.Goal, w)
	have := func(k string) bool {
		if slots[k] != "" {
			return true
		}
		return cx.task.Slots[k] != ""
	}
	for _, grp := range strings.Split(in["need"], ",") {
		if grp == "" {
			continue
		}
		sat := false
		for _, k := range strings.Split(grp, "|") {
			if have(strings.TrimSpace(k)) {
				sat = true
			}
		}
		if !sat {
			return false
		}
	}
	return true
}
