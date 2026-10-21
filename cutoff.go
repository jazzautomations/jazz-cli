package main

// cutoff.go: continuation of a write/edit cut at the output token limit.
//
// Measured on the Dell (aider-polyglot-python, laguna free): the model writes a
// whole 200-400 line solution in one `write`, the reply hits max_tokens inside
// the `content` string, the call is refused (TOOL_NOT_EXECUTED) and the model
// tries the same giant write again until the 10 min run out. Here the partial
// arguments are kept: the model is asked, in a plain user message (the system
// prompt and the history prefix stay byte-identical, so the prompt cache
// holds), to output only the remaining lines after the tail it already wrote.
// The pieces are spliced and the tool runs once with the complete arguments.
// At most ccMaxConts continuation requests per call; after that (or when the
// cut is not inside write.content / edit.new_string) the old truncation path
// refuses the call as before.
//
// Also the adaptive output budget: after any cutoff the next request (and every
// continuation) asks for more output tokens, up to the provider's limit when it
// is known (models catalog, preset max_out or config max_output_tokens).
//
// Config `cutoff_continue: on|off` (default on), flag --cutoff-continue,
// env JAZZ_CUTOFF_CONTINUE.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// ccThinkCutReminder follows a reply that spent the whole output budget on
// reasoning without answering or calling a tool.
const ccThinkCutReminder = "<system-reminder>Your last reply used the whole output token limit on internal reasoning and produced no answer and no tool call, so it was discarded. Do not deliberate at length again: decide with what you already know and act now (one short sentence, then the tool call). If something is ambiguous, pick the most likely reading and move on.</system-reminder>"

const (
	ccMaxConts  = 3  // continuation requests per cut call
	ccTailLines = 20 // lines of the kept content shown back to the model
)

// ccEnabled: env wins over the run flag, which wins over the config.
func (a *Agent) ccEnabled() bool {
	if v := strings.TrimSpace(os.Getenv("JAZZ_CUTOFF_CONTINUE")); v != "" {
		return !olOffWord(v)
	}
	if a.cfg == nil {
		return true
	}
	if a.cfg.CutoffContinueRun != "" {
		return !olOffWord(a.cfg.CutoffContinueRun)
	}
	return !olOffWord(a.cfg.CutoffContinue)
}

// ccCut is a write/edit call whose JSON arguments end inside the string value
// of content (write) or new_string (edit, top level or the last edits[] item).
type ccCut struct {
	Call  ToolCall
	Field string // canonical field: content | new_string
	Path  string
	head  string // raw JSON up to the opening quote of the cut value
	close string // closers for the containers still open at the cut
	Value string // decoded value kept so far, ending at its last complete line
}

// Args rebuilds valid JSON arguments with value in the cut field.
func (c *ccCut) Args(value string) string {
	b, _ := json.Marshal(value)
	return c.head + string(b) + c.close
}

type ccFrame struct {
	kind      byte // '{' or '['
	key       string
	expectKey bool
}

// ccParse finds where a cut write/edit stopped. ok=false when the arguments
// are complete JSON, the cut is outside the content field, or the call lacks
// what is needed to finish it (path, old_string).
func ccParse(tc ToolCall) (*ccCut, bool) {
	if tc.Name != "write" && tc.Name != "edit" {
		return nil, false
	}
	raw := strings.TrimLeft(tc.Args, " \t\r\n")
	if !strings.HasPrefix(raw, "{") {
		return nil, false
	}
	var probe any
	if json.Unmarshal([]byte(raw), &probe) == nil {
		return nil, false // complete JSON: nothing to continue
	}
	var stack []*ccFrame
	in, esc, isKey := false, false, false
	strStart := -1
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if in {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				in = false
				if isKey && len(stack) > 0 {
					var k string
					if json.Unmarshal([]byte(escapeRawControl(raw[strStart:i+1])), &k) == nil {
						stack[len(stack)-1].key = k
					}
					stack[len(stack)-1].expectKey = false
				}
			}
			continue
		}
		switch c {
		case '"':
			in, strStart = true, i
			top := (*ccFrame)(nil)
			if len(stack) > 0 {
				top = stack[len(stack)-1]
			}
			isKey = top != nil && top.kind == '{' && top.expectKey
		case '{':
			stack = append(stack, &ccFrame{kind: '{', expectKey: true})
		case '[':
			stack = append(stack, &ccFrame{kind: '['})
		case '}', ']':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case ',':
			if len(stack) > 0 && stack[len(stack)-1].kind == '{' {
				stack[len(stack)-1].expectKey = true
			}
		}
	}
	if !in || isKey || len(stack) == 0 {
		return nil, false
	}
	alias := func(k string) string {
		if to, ok := paramAliases[tc.Name][k]; ok {
			return to
		}
		return k
	}
	top := stack[len(stack)-1]
	field := alias(top.key)
	switch {
	case tc.Name == "write" && len(stack) == 1 && field == "content":
	case tc.Name == "edit" && field == "new_string" && (len(stack) == 1 ||
		(len(stack) == 3 && stack[0].key == "edits" && stack[1].kind == '[')):
	default:
		return nil, false
	}
	cut := &ccCut{Call: tc, Field: field, head: raw[:strStart]}
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i].kind == '{' {
			cut.close += "}"
		} else {
			cut.close += "]"
		}
	}
	value, ok := ccDecodePartial(raw[strStart+1:])
	if !ok {
		return nil, false
	}
	nl := strings.LastIndexByte(value, '\n')
	if nl < 0 {
		return nil, false // not even one complete line: nothing worth keeping
	}
	cut.Value = value[:nl+1]
	if ccDegenerate(cut.Value) {
		return nil, false // a repetition loop ran into the limit: continuing it wastes the budget
	}
	// the rest of the arguments must be usable: path, and old_string for edit
	m, repaired, err := parseArgs(cut.Args(cut.Value))
	if err != nil || repaired {
		return nil, false
	}
	m = normalizeArgs(tc.Name, m)
	cut.Path, _ = argStr(m, "path")
	if strings.TrimSpace(cut.Path) == "" {
		return nil, false
	}
	if tc.Name == "edit" {
		if len(stack) == 3 {
			arr, _ := m["edits"].([]any)
			if len(arr) == 0 {
				return nil, false
			}
			last, _ := arr[len(arr)-1].(map[string]any)
			if o, ok := argStr(normalizeArgs("edit", last), "old_string"); !ok || o == "" {
				return nil, false
			}
		} else if o, ok := argStr(m, "old_string"); !ok || o == "" {
			return nil, false
		}
	}
	return cut, true
}

// ccDegenerate: the cut text ends in a repetition loop (weak models can emit
// the same lines until max_tokens), not in real content.
func ccDegenerate(s string) bool {
	l := ccLines(s)
	if len(l) > 40 {
		l = l[len(l)-40:]
	}
	if len(l) < 20 {
		return false
	}
	seen := map[string]bool{}
	for _, x := range l {
		if t := strings.TrimSpace(x); t != "" {
			seen[t] = true
		}
	}
	return len(seen) < 4
}

var ccPartialEscRE = regexp.MustCompile(`\\(u[0-9a-fA-F]{0,3})?$`)

// ccDecodePartial decodes the body of a JSON string cut before its closing
// quote, dropping an escape sequence cut in half.
func ccDecodePartial(s string) (string, bool) {
	// an odd run of trailing backslashes is a cut escape
	n := 0
	for i := len(s) - 1; i >= 0 && s[i] == '\\'; i-- {
		n++
	}
	if n%2 == 1 {
		s = s[:len(s)-1]
	} else if m := ccPartialEscRE.FindStringIndex(s); m != nil && m[1]-m[0] > 1 {
		// \u12 cut: make sure the backslash itself is not escaped
		k := 0
		for i := m[0] - 1; i >= 0 && s[i] == '\\'; i-- {
			k++
		}
		if k%2 == 0 {
			s = s[:m[0]]
		}
	}
	var v string
	if json.Unmarshal([]byte(escapeRawControl(`"`+s+`"`)), &v) != nil {
		return "", false
	}
	return v, true
}

// ccTail is the last n lines of s (s ends with a newline).
func ccTail(s string, n int) string {
	lines := strings.SplitAfter(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "")
}

func ccLines(s string) []string {
	l := strings.Split(s, "\n")
	if len(l) > 0 && l[len(l)-1] == "" {
		l = l[:len(l)-1]
	}
	return l
}

var ccFenceRE = regexp.MustCompile("^\\s*```[\\w.+-]*\\s*$")

// ccClean strips what models add around a continuation: a fenced block (with
// at most two lines of prose before it) and the lines of kept repeated at
// the start. restart=true when the model wrote the whole value again.
func ccClean(cont, kept string) (out string, restart bool) {
	lines := strings.Split(cont, "\n")
	for i := 0; i < len(lines) && i < 3; i++ {
		if ccFenceRE.MatchString(lines[i]) {
			lines = lines[i+1:]
			for j := len(lines) - 1; j >= 0; j-- {
				if strings.TrimSpace(lines[j]) == "" {
					continue
				}
				if ccFenceRE.MatchString(lines[j]) {
					lines = append(lines[:j], "") // the closing fence ended the last line
				}
				break
			}
			break
		}
	}
	cont = strings.Join(lines, "\n")
	kl, cl := ccLines(kept), ccLines(cont)
	// the whole file again: its first lines match the start of what was kept
	if n := min(3, len(kl)); n > 0 && len(cl) >= n && len(cl) >= len(kl)/2 {
		same := true
		for i := 0; i < n; i++ {
			if cl[i] != kl[i] {
				same = false
				break
			}
		}
		if same && strings.TrimSpace(strings.Join(kl[:n], "")) != "" {
			return cont, true
		}
	}
	// repeated tail: the longest suffix of kept that prefixes cont, when it
	// carries real text (a lone blank or "}" line may legitimately repeat)
	for k := min(len(kl), len(cl), 200); k >= 1; k-- {
		match := true
		for i := 0; i < k; i++ {
			if kl[len(kl)-k+i] != cl[i] {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		chars := 0
		for _, l := range cl[:k] {
			chars += len(strings.TrimSpace(l))
		}
		if chars >= 12 {
			return strings.Join(cl[k:], "\n") + ccTrailNL(cont, len(cl) > k), false
		}
		break
	}
	return cont, false
}

func ccTrailNL(s string, any bool) string {
	if any && strings.HasSuffix(s, "\n") {
		return "\n"
	}
	return ""
}

// ccPrompt is the continuation request (a user message; the system prompt
// never changes).
func ccPrompt(c *ccCut, maxOut int) string {
	lines := strings.Count(c.Value, "\n")
	return fmt.Sprintf("<system-reminder>Your %s call for %s was cut off at the output token limit (max_tokens=%d) inside %s, after %d complete lines. It has NOT run yet; the harness kept those lines. "+
		"Continue %s exactly from where it stopped: reply with ONLY the remaining lines, starting with the line right after the last line below. "+
		"No repetition of earlier lines, no commentary, no code fences, no tool call. The harness appends your reply and then runs the call.\n"+
		"Last lines you wrote:\n<tail>\n%s</tail></system-reminder>",
		c.Call.Name, c.Path, maxOut, c.Field, lines, c.Field, ccTail(c.Value, ccTailLines))
}

// ccContinue completes the cut last call of calls with continuation requests.
// n = requests made. ok=false: fall back to the truncation refusal.
func (a *Agent) ccContinue(ctx context.Context, model, text, reasoning string, calls []ToolCall) (full []ToolCall, n int, ok bool) {
	if len(calls) == 0 {
		return nil, 0, false
	}
	for _, tc := range calls[:len(calls)-1] {
		if _, rep, err := parseArgs(tc.Args); err != nil || rep {
			return nil, 0, false // only the last call can be cut
		}
	}
	if a.mcp.Has(calls[len(calls)-1].Name) {
		return nil, 0, false
	}
	cut, ok := ccParse(calls[len(calls)-1])
	if !ok {
		dbg("cutoff", map[string]any{"continue": false, "tool": calls[len(calls)-1].Name, "args_bytes": len(calls[len(calls)-1].Args)})
		return nil, 0, false
	}
	value := cut.Value
	for n < ccMaxConts {
		if ctx.Err() != nil {
			return nil, n, false
		}
		n++
		a.ui.Notice(fmt.Sprintf("%s de %s cortado no limite de saída — pedindo a continuação (%d/%d)", cut.Call.Name, cut.Path, n, ccMaxConts))
		partial := append([]ToolCall(nil), calls...)
		partial[len(partial)-1].Args = cut.Args(value)
		msgs := append([]Message(nil), a.Msgs...)
		msgs = append(msgs, Message{Role: "assistant", Content: text, Reasoning: reasoning, ToolCalls: partial})
		for _, tc := range partial {
			msgs = append(msgs, Message{Role: "tool", ToolCallID: tc.ID, ToolName: tc.Name, Content: "TOOL_PENDING: not run yet; it runs once the cut call is complete."})
		}
		c := *cut
		c.Value = value
		maxOut := a.outCap(model, acChatOpt{boost: true})
		msgs = append(msgs, Message{Role: "user", Content: ccPrompt(&c, maxOut)})
		resp, _, err := a.chatWithFallback(ctx, model, acChatOpt{msgs: msgs, noToolUse: true, boost: true, label: "continuando " + cut.Path})
		if err != nil || resp == nil {
			dbg("cutoff", map[string]any{"continue": "error", "n": n, "err": fmt.Sprint(err)})
			return nil, n, false
		}
		a.addUsage(resp.Usage)
		cont := resp.Text
		if strings.TrimSpace(cont) == "" {
			dbg("cutoff", map[string]any{"continue": "empty", "n": n, "calls": len(resp.ToolCalls)})
			return nil, n, false
		}
		cont, restart := ccClean(cont, value)
		cutAgain := resp.Truncated() || resp.Stop == acStopToolCut
		if cutAgain {
			nl := strings.LastIndexByte(cont, '\n')
			if nl < 0 {
				dbg("cutoff", map[string]any{"continue": "no_progress", "n": n})
				return nil, n, false
			}
			cont = cont[:nl+1]
		}
		if restart {
			value = cont
		} else {
			if value != "" && !strings.HasSuffix(value, "\n") {
				value += "\n"
			}
			value += cont
		}
		dbg("cutoff", map[string]any{"continue": "part", "n": n, "bytes": len(value), "cut_again": cutAgain, "restart": restart})
		if !cutAgain {
			partial[len(partial)-1].Args = cut.Args(value)
			a.ui.Notice(fmt.Sprintf("%s de %s completado em %d partes", cut.Call.Name, cut.Path, n+1))
			return partial, n, true
		}
	}
	dbg("cutoff", map[string]any{"continue": "cap", "n": n})
	return nil, n, false
}

// ccPartialNote is the history text of a reply whose stream failed: what
// arrived, the cut calls as text (never executed) and why it stopped.
func ccPartialNote(r *ChatResponse, err error) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(r.Text))
	for _, tc := range r.ToolCalls {
		args := tc.Args
		if len(args) > 32<<10 {
			args = truncateHeadTail(args, 32<<10)
		}
		fmt.Fprintf(&b, "\n\n[tool call %s cut while being written, not executed; partial arguments:]\n%s", tc.Name, args)
	}
	fmt.Fprintf(&b, "\n\n[the reply stream failed: %s]", oneLine(fmt.Sprint(err), 160))
	return strings.TrimSpace(b.String())
}

// ccResume: on the first request of a turn (--resume, -c, or the next prompt),
// a write/edit left cut by a failed or cancelled stream is continued from its
// partial arguments instead of being redone. It returns a reply carrying the
// completed call, or nil to ask the model normally.
func (a *Agent) ccResume(ctx context.Context, model string) (*ChatResponse, int) {
	if a.sub || !a.ccEnabled() {
		return nil, 0
	}
	idx := -1
	for i := len(a.Msgs) - 1; i >= 0; i-- {
		if a.Msgs[i].Role == "assistant" {
			if len(a.Msgs[i].PartialCalls) > 0 {
				idx = i
			}
			break // only the latest assistant message counts
		}
	}
	if idx < 0 {
		return nil, 0
	}
	calls := a.Msgs[idx].PartialCalls
	if _, ok := ccParse(calls[len(calls)-1]); !ok {
		return nil, 0
	}
	dbg("cutoff", map[string]any{"resume": calls[len(calls)-1].Name, "msg": idx})
	full, n, ok := a.ccContinue(ctx, model, "", "", calls)
	if !ok {
		return nil, n
	}
	return &ChatResponse{Text: "Continuing the cut " + full[len(full)-1].Name + " from where it stopped.", ToolCalls: full, Stop: "tool_calls"}, n
}
