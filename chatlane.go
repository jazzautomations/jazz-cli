package main

// chatlane.go: conversation lane. "oi", "valeu", "tudo bem?" with no task
// pending are answered by the model with the tools still available, but
// without the per-turn machinery that only pays on a task: plan cache and
// "da última vez" lessons, name index, docs hints, the turn checkpoint
// (a git scan) and the LLM route classifier. v0.13: 21 small talks had a
// p50 of 46 s and one "oi" ran 81 tools in 751 s.
// "bora", "marcha" and "continua" are go-aheads, never chat: brSmallTalk
// does not match them.

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
	return a.openTodos() == ""
}
