package main

// chatlane.go: conversation lane. "oi", "valeu", "tudo bem?" with no task
// pending are answered by the model with the tools still available, but
// without the per-turn machinery that only pays on a task: plan cache and
// "da última vez" lessons, name index, docs hints, the turn checkpoint
// (a git scan) and the LLM route classifier. v0.13: 21 small talks had a
// p50 of 46 s and one "oi" ran 81 tools in 751 s.
// "bora", "marcha" and "continua" are go-aheads, never chat: brSmallTalk
// does not match them.

import "strings"

// acChatOut caps the output of a conversation reply (no boost after a cut).
const acChatOut = 2048

// acChatLane reports whether this request is small talk with nothing pending.
func (a *Agent) acChatLane(request string) bool {
	if a.sub || a.mode() == "plan" {
		return false
	}
	if _, ok := brSmallTalk(acStripReminders(request)); !ok {
		return false
	}
	if a.openTodos() != "" {
		return false
	}
	// "show", "perfeito", "boa" right after the model asked something are an
	// answer (a go-ahead), not small talk: the work that follows needs the
	// checkpoint and the full output
	for i := len(a.Msgs) - 1; i >= 0; i-- {
		if m := a.Msgs[i]; m.Role == "assistant" {
			return !strings.HasSuffix(strings.TrimSpace(m.Content), "?")
		}
	}
	return true
}
