package main

// approve_batch.go: confirmations of one model response, asked once. When a
// response carries several calls that each need the user's OK (ask mode, a
// dangerous command next to an edit, …), a dry pass of permit collects them
// and the user answers one numbered prompt: Enter = yes to all, n = no to
// all, u = one by one (the old behaviour; JAZZ_APPROVE_BATCH=off always). The answers are then consumed by
// the normal permit of each call, so every rule, guard and audit record
// still applies call by call; only the question is merged.

import (
	"context"
	"os"
)

// apBatchItem is one call that would ask.
type apBatchItem struct {
	ID, Tool, Detail, Reason string
}

// apBatch is the state of one response's merged confirmation.
type apBatch struct {
	collect   bool
	answering bool   // permitAnswer runs for a merged answer (obs: no prompt of its own)
	cur       string // call being collected
	items     []apBatchItem
	answers   map[string]Approval // call id -> answer
}

// uiBatchApprover is implemented by UIs that can ask several confirmations at
// once (TermUI). oneByOne: ask each call on its own.
type uiBatchApprover interface {
	ApproveBatch(ctx context.Context, items []apBatchItem) (answers []Approval, oneByOne bool)
}

// apBatchAsk runs before the calls of a response: it collects the calls that
// would ask and, with two or more, asks them together.
func (a *Agent) apBatchAsk(ctx context.Context, calls []ToolCall) {
	a.apb = nil
	if len(calls) < 2 || a.sub || a.Headless || olOffWord(os.Getenv("JAZZ_APPROVE_BATCH")) {
		return
	}
	ba, ok := a.ui.(uiBatchApprover)
	if !ok {
		return
	}
	switch a.mode() {
	case "smart", "yolo", "plan": // smart asks a model per call; yolo and plan never batch
		return
	}
	seen := map[string]bool{}
	for _, tc := range calls {
		if tc.ID == "" || seen[tc.ID] {
			return // answers are keyed by call id
		}
		seen[tc.ID] = true
	}
	b := &apBatch{collect: true}
	a.apb = b
	why := a.blockWhy
	for _, tc := range calls {
		if ctx.Err() != nil {
			break
		}
		b.cur = tc.ID
		a.permit(ctx, tc, toolDetail(tc.Name, tc.Args), a.mcp.Has(tc.Name), acHookVerdict{})
	}
	a.blockWhy = why
	b.collect = false
	if len(b.items) < 2 || ctx.Err() != nil {
		a.apb = nil
		return
	}
	answers, oneByOne := ba.ApproveBatch(ctx, b.items)
	if oneByOne || len(answers) != len(b.items) {
		a.apb = nil
		return
	}
	b.answers = map[string]Approval{}
	yes := 0
	for i, it := range b.items {
		b.answers[it.ID] = answers[i]
		if answers[i] != Deny {
			yes++
		}
	}
	a.obEv("approval.batch", map[string]any{"n": len(b.items), "allowed": yes})
}

// apBatchCollecting: the dry pass is running (permit must not prompt, record
// or hint anything).
func (a *Agent) apBatchCollecting() bool { return a.apb != nil && a.apb.collect }

// apBatchAdd records a call that would ask (dry pass).
func (a *Agent) apBatchAdd(tool, detail, reason string) {
	a.apb.items = append(a.apb.items, apBatchItem{ID: a.apb.cur, Tool: tool, Detail: detail, Reason: reason})
}

// apBatchAnswer is the merged answer for call id, once.
func (a *Agent) apBatchAnswer(id string) (Approval, bool) {
	if a.apb == nil || a.apb.answers == nil || id == "" {
		return Deny, false
	}
	ap, ok := a.apb.answers[id]
	delete(a.apb.answers, id)
	return ap, ok
}
