package main

import (
	"context"
	"errors"
	"strings"
)

// /btw <question>: a side question answered over the current conversation
// (Devin /btw, Kiro tangent). One request, no tools, the cached prefix is
// reused, and neither the question nor the answer enters the history — the
// task's context does not grow.

const btwNote = "<system-reminder>Side question from the user, outside the current task. Answer it briefly from what is already in this conversation, without calling tools. The task itself continues afterwards unchanged.</system-reminder>\n"

// Side answers q without changing a.Msgs.
func (a *Agent) Side(ctx context.Context, q string) (string, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return "", errors.New("uso: /btw <pergunta>")
	}
	a.EnsureSystem()
	msgs := append(append([]Message{}, a.Msgs...), Message{Role: "user", Content: btwNote + nlCredScrub(q)})
	resp, _, err := a.chatWithFallback(ctx, a.TurnModel(), acChatOpt{msgs: msgs, noToolUse: true, maxOut: 2000, quiet: true, label: "pergunta paralela", role: "main"})
	if err != nil {
		return "", err
	}
	a.addUsage(resp.Usage)
	s := strings.TrimSpace(resp.Text)
	if s == "" {
		return "", errEmptySummary
	}
	return s, nil
}
