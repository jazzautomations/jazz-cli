package main

// askuser.go: the ask_user tool. The model may ask the user one question
// with 2-4 short options when the task is genuinely ambiguous and the answer
// changes the work. Interactive: the Nord numbered menu, the turn waits for a
// digit or free text. Headless (-p): never blocks; the result tells the model
// to choose the most reasonable default and state the assumption, unless
// --ask-stdin was given (then one line is read from stdin).
//
// The spec is always part of the tool list (byte-stable, cache-safe), and an
// ask_user call is not an action: it does not count for the anti-fake guard.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

var askUserSpec = ToolSpec{
	Name: "ask_user",
	Description: "Ask the user one multiple-choice question and wait for the answer. Use ONLY when the task is genuinely ambiguous and the answer changes the work (two incompatible designs, which of several targets). Never use it for confirmations the harness already handles (permissions, running commands, edits) or for things you can find out by reading the code. " +
		"Give 2-4 short options; the user may also type free text.",
	Params: obj(map[string]any{
		"question": str("The question, one sentence"),
		"options": map[string]any{"type": "array", "description": "2-4 options", "items": obj(map[string]any{
			"label":       str("Short label (a few words)"),
			"description": str("Optional one-line explanation"),
		}, "label")},
		"allow_free_text": map[string]any{"type": "boolean", "description": "Accept a typed answer besides the options (default true)"},
	}, "question", "options"),
}

// askOpt is one option of an ask_user call.
type askOpt struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type askArgs struct {
	Question string   `json:"question"`
	Options  []askOpt `json:"options"`
	Free     *bool    `json:"allow_free_text,omitempty"`
}

// askParse validates the arguments ("" error = fine).
func askParse(raw string) (askArgs, string) {
	var a askArgs
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		// tolerate options given as plain strings
		var b struct {
			Question string   `json:"question"`
			Options  []string `json:"options"`
			Free     *bool    `json:"allow_free_text,omitempty"`
		}
		if json.Unmarshal([]byte(raw), &b) != nil {
			return a, "arguments are not valid JSON: " + err.Error()
		}
		a.Question, a.Free = b.Question, b.Free
		for _, o := range b.Options {
			a.Options = append(a.Options, askOpt{Label: o})
		}
	}
	a.Question = strings.TrimSpace(a.Question)
	if a.Question == "" {
		return a, "missing required parameter 'question'"
	}
	var opts []askOpt
	for _, o := range a.Options {
		if o.Label = strings.TrimSpace(o.Label); o.Label != "" {
			opts = append(opts, o)
		}
	}
	if len(opts) < 2 || len(opts) > 4 {
		return a, fmt.Sprintf("give 2-4 options (got %d)", len(opts))
	}
	a.Options = opts
	return a, ""
}

func (a askArgs) free() bool { return a.Free == nil || *a.Free }

// uiAsker is implemented by UIs that can ask the user (TermUI).
type uiAsker interface {
	AskUser(ctx context.Context, q askArgs) (answer string, ok bool)
}

// askHeadlessNote is the tool result when nobody can answer.
const askHeadlessNote = "headless: no user available to answer. Choose the most reasonable default yourself, state the assumption explicitly in your final answer, and continue."

// askStdinMu serializes --ask-stdin reads.
var askStdinMu sync.Mutex

// askStdin reads the answer from r (--ask-stdin).
var askStdin io.Reader = os.Stdin

// runAskUser executes an ask_user call.
func (a *Agent) runAskUser(ctx context.Context, tc ToolCall) ToolOutput {
	q, bad := askParse(tc.Args)
	if bad != "" {
		return ToolOutput{IsErr: true, Text: "ask_user: " + bad, Display: Display{Summary: "pergunta inválida"}}
	}
	if a.sub {
		return ToolOutput{IsErr: true, Text: "ask_user is not available inside a subagent: report the open question instead."}
	}
	if asker, ok := a.ui.(uiAsker); ok {
		ans, ok := asker.AskUser(ctx, q)
		if !ok {
			return ToolOutput{Text: "The user skipped the question (Esc). Choose the most reasonable default, state the assumption, and continue.", Display: Display{Summary: "pergunta pulada"}}
		}
		return ToolOutput{Text: "The user answered: " + ans, Display: Display{Summary: "resposta: " + oneLine(ans, 80)}}
	}
	if a.AskStdin {
		fmt.Fprintf(os.Stderr, "\njazz pergunta: %s\n", q.Question)
		for i, o := range q.Options {
			fmt.Fprintf(os.Stderr, "  %d) %s", i+1, o.Label)
			if o.Description != "" {
				fmt.Fprintf(os.Stderr, " — %s", o.Description)
			}
			fmt.Fprintln(os.Stderr)
		}
		fmt.Fprint(os.Stderr, "> ")
		askStdinMu.Lock()
		line, err := bufio.NewReader(askStdin).ReadString('\n')
		askStdinMu.Unlock()
		if ans := askResolve(q, strings.TrimSpace(line)); ans != "" {
			return ToolOutput{Text: "The user answered: " + ans, Display: Display{Summary: "resposta: " + oneLine(ans, 80)}}
		}
		if err != nil {
			dbg("ask_user", map[string]any{"headless": true, "stdin_err": err.Error()})
		}
	}
	dbg("ask_user", map[string]any{"headless": true, "question": oneLine(q.Question, 200)})
	return ToolOutput{Text: askHeadlessNote, Display: Display{Summary: "sem usuário (headless): o modelo escolhe e explica"}}
}

// askResolve maps a reply to an answer: a digit picks that option's label,
// other text is a free answer (when allowed). "" = no answer.
func askResolve(q askArgs, reply string) string {
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return ""
	}
	items := make([]olAction, len(q.Options))
	for i, o := range q.Options {
		items[i] = olAction{Label: o.Label, Key: o.Label}
	}
	if i := olPick(items, reply); i >= 0 {
		return q.Options[i].Label
	}
	if q.free() {
		return reply
	}
	return ""
}

// AskUser renders the question as a menu (menu.go): ↑↓ + Enter or the digit
// picks an option; with free text allowed, "Outra resposta" (or simply
// starting to type) opens a line for the user's own words. Esc skips (TermUI).
func (u *TermUI) AskUser(ctx context.Context, q askArgs) (string, bool) {
	items := make([]mnItem, 0, len(q.Options)+1)
	for _, o := range q.Options {
		items = append(items, mnItem{Label: sanitizeTerm(o.Label), Hint: sanitizeTerm(o.Description)})
	}
	other := -100
	if q.free() {
		other = len(items)
		items = append(items, mnItem{Label: "Outra resposta (escrever)"})
	}
	t, g := u.T, u.T.G()
	hint := g.up + g.down + " enter escolhe " + g.dot + " esc pula"
	if q.free() {
		hint = g.up + g.down + " enter escolhe " + g.dot + " ou comece a digitar a resposta " + g.dot + " esc pula"
	}
	sp := &mnSpec{Tag: "pergunta", Title: sanitizeTerm(q.Question), Items: items, Text: q.free(), Keys: hint}
	if u.keys == nil {
		return "", false
	}
	question := oneLine(sanitizeTerm(q.Question), 120)
	u.StopThinking()
	keys, release := u.grabKeys(ctx)
	defer release()
	i := u.menuKeys(ctx, keys, sp)
	switch {
	case i >= 0 && i < len(q.Options):
		u.menuDone(true, question+"  "+t.fg(cGray, 90)+q.Options[i].Label)
		return q.Options[i].Label, true
	case i == other || i == mnText:
		var init []rune
		if i == mnText {
			init = []rune{sp.typed}
		}
		u.print("  " + t.fg(cAccent, 36) + t.sgr("1") + "?" + t.sgr("0") + " " + question + "\n")
		for {
			line, ok := u.readLine(ctx, keys, "  "+t.fg(cAccent, 36)+g.prompt+t.sgr("0")+" ", time.Time{}, init...)
			if !ok {
				return "", false
			}
			if ans := askResolve(q, line); ans != "" {
				return ans, true
			}
			init = nil
		}
	}
	u.menuDone(false, question+"  "+t.fg(cGray, 90)+"pulada")
	return "", false
}
