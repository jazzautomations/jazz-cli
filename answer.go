package main

// answer.go: the final answer of a -p run survives the harness gates.
//
// The quality checklist, the auto-review, the --verifier, the todo/verify/
// fake-action/stall nudges and Stop hooks run after the model already gave
// its final answer, and reopen the turn. The reply to the gate is often a
// short meta reply ("item 1 é falso positivo", "lista atualizada"), and the
// headless sinks used to print only the last message, so the real summary was
// lost. The agent now tells the sink (acAnswerKeeper) before each reopen; the
// sink keeps that answer and merges it with the reply at the end of the turn.

import (
	"strings"
	"unicode/utf8"
)

// acGateAnswerNote is appended to the gate reminders, so the reply after a
// gate is itself a complete answer for the user, not a note to the reviewer.
const acGateAnswerNote = "When you are done, end with a complete final answer to the user's original request (what you did, which files changed, test results), including anything this check made you change. Write it for the user; do not address the reviewer or the harness."

// acGateFollowLabel heads the reply to a gate when it is printed after the
// kept answer (it adds something, but is not a complete answer by itself).
const acGateFollowLabel = "Depois da verificação final:"

// acAnswerKeeper is implemented by the -p sinks (HeadlessUI, JSONUI).
type acAnswerKeeper interface{ KeepAnswer() }

// acKeepAnswer runs right before a harness gate reopens the turn after a final
// answer (text, non-empty). Subagents report to their parent, not to stdout.
func (a *Agent) acKeepAnswer(text string) {
	if a.sub || strings.TrimSpace(text) == "" {
		return
	}
	if k, ok := a.ui.(acAnswerKeeper); ok {
		k.KeepAnswer()
	}
}

// acGateFailer is implemented by the -p sinks: the reopened turn ended with
// the gate still failing (quality_failed …), so the reply is about the gate,
// not a new complete answer — the kept answer must be printed too.
type acGateFailer interface{ GateFailed() }

// acGateFailed runs when a harness gate ends the turn as failed after it had
// reopened it (answer.go acMergeFailed decides what is printed).
func (a *Agent) acGateFailed() {
	if a.sub {
		return
	}
	if f, ok := a.ui.(acGateFailer); ok {
		f.GateFailed()
	}
}

// acMergeFailed is acMergeAnswer for a turn a gate ended as failed: the kept
// answer is never replaced by the reply, however long; a reply that adds
// something follows it, labelled.
func acMergeFailed(kept, reply string) string {
	kept, reply = strings.TrimSpace(kept), strings.TrimSpace(reply)
	switch {
	case kept == "":
		return reply
	case reply == "", strings.Contains(kept, reply), acAckRE.MatchString(reply):
		return kept
	case strings.Contains(reply, kept):
		return reply
	}
	return kept + "\n\n" + acGateFollowLabel + "\n" + reply
}

// acAckRE: a reply to a gate that says nothing for the user.
var acAckRE = lazyRx(`(?i)^(ok(ay)?|certo|entendido|feito|pronto|beleza|blz|done|sure|understood|got it|nada (a|para|pra) (mudar|corrigir|fazer)|nada mais a fazer|nothing (else )?to (change|fix|do)|sem (mudan[çc]as|altera[çc][õo]es))[.!…]*$`)

// acMergeAnswer combines the answer kept before a gate (kept) with the reply
// that ended the reopened turn (reply):
//   - a reply that repeats or contains the kept answer, or is as substantial
//     as it, is the new complete answer;
//   - an empty, acknowledgement-only or already-contained reply adds nothing;
//   - otherwise the kept answer comes first and the reply follows, labelled.
func acMergeAnswer(kept, reply string) string {
	kept, reply = strings.TrimSpace(kept), strings.TrimSpace(reply)
	switch {
	case kept == "":
		return reply
	case reply == "", strings.Contains(kept, reply), acAckRE.MatchString(reply):
		return kept
	case strings.Contains(reply, kept):
		return reply
	}
	nk, nr := utf8.RuneCountInString(kept), utf8.RuneCountInString(reply)
	if nr*10 >= nk*8 && (nr >= 120 || nr >= nk) {
		return reply
	}
	return kept + "\n\n" + acGateFollowLabel + "\n" + reply
}
