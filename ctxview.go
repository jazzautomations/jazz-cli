package main

// ctxview.go: what the model sees of the history, as a pure function of the
// message list. The first policy is the tiered view: the agent's own turns
// and the last few tool results stay verbatim, older big tool results become
// a one-line stub that says what was there and how to get it back. Nothing
// here changes Agent.Msgs; the view is built per request, so the same policy
// can be replayed over recorded sessions (jazz eval context, evalctx.go).

import (
	"encoding/json"
	"fmt"
	"strings"
)

// cvPolicy is the tiered view: Keep = how many of the newest tool results stay
// verbatim; MinBytes = results at or under this size are never touched.
type cvPolicy struct {
	Keep     int
	MinBytes int
	// Spare: results of these tools are never stubbed.
	Spare []string
	// Reads: how results of the read tool are treated. "" = like any other
	// tool. "stale" = a read is stubbed only when the same file was read
	// again later (the text is still in the history, further down), whatever
	// its age. "edited" = stale, plus reads of a file that was edited or
	// rewritten afterwards (the text no longer matches the file).
	Reads string
}

// cvDefault is the policy measured in docs (keep 3, stub results over 1 KB).
func cvDefault() cvPolicy { return cvPolicy{Keep: 3, MinBytes: 1000} }

func cvValidMode(s string) bool {
	return s == "off" || s == "tier" || s == "stale" || s == "edited"
}

// cvPolicyFor maps the context_view setting to a policy (false = off).
// tier stubs every old big result; stale and edited do the same but keep a
// file read until a later read or edit made it redundant.
func cvPolicyFor(mode string) (cvPolicy, bool) {
	p := cvDefault()
	switch mode {
	case "tier":
	case "stale", "edited":
		p.Reads = mode
	default:
		return p, false
	}
	return p, true
}

// cvApply is the view of the history under the current setting.
func cvApply(msgs []Message) []Message {
	if p, ok := cvPolicyFor(opSettingsNow().CtxView); ok {
		msgs, _ = cvMask(msgs, p)
	}
	return msgs
}

// cvStats is what a view saved.
type cvStats struct {
	Masked int // tool results replaced by a stub
	Before int // bytes of message content before
	After  int // and after
}

const cvStubHead = 160 // bytes of the first line kept in a stub

var cvSpillRx = lazyRx(`Full output saved to (\S+)`)

// cvCall is the call a tool result answers, as far as the view cares.
type cvCall struct {
	name, path string
	whole      bool // a read without offset/limit
	args       string
}

func cvCalls(msgs []Message) map[string]cvCall {
	out := map[string]cvCall{}
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			c := cvCall{name: tc.Name, args: tc.Args}
			var a map[string]any
			if json.Unmarshal([]byte(tc.Args), &a) == nil {
				c.path, _ = a["path"].(string)
				_, off := a["offset"]
				_, lim := a["limit"]
				c.whole = !off && !lim
			}
			out[tc.ID] = c
		}
	}
	return out
}

// cvLater is what happened to a file after the message being looked at.
type cvLater struct {
	whole, edited bool
	args          map[string]bool
}

// cvStaleReads returns, for each read result that a later message made
// redundant, why.
func cvStaleReads(msgs []Message, mode string) map[int]string {
	if mode != "stale" && mode != "edited" {
		return nil
	}
	calls := cvCalls(msgs)
	out := map[int]string{}
	seen := map[string]*cvLater{} // by path, walking from the newest message back
	at := func(path string) *cvLater {
		l := seen[path]
		if l == nil {
			l = &cvLater{args: map[string]bool{}}
			seen[path] = l
		}
		return l
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role == "tool" {
			c := calls[m.ToolCallID]
			if c.name != "read" || c.path == "" {
				continue
			}
			l := at(c.path)
			switch {
			case l.whole || l.args[c.args]:
				out[i] = "the same file was read again later"
			case mode == "edited" && l.edited:
				out[i] = "the file was changed after this read"
			}
			if c.whole {
				l.whole = true
			}
			l.args[c.args] = true
			continue
		}
		for _, tc := range m.ToolCalls {
			if tc.Name != "edit" && tc.Name != "write" {
				continue
			}
			if c := calls[tc.ID]; c.path != "" {
				// a read newer than the edit shows other text: it does not
				// stand in for reads older than the edit
				l := at(c.path)
				l.edited, l.whole, l.args = true, false, map[string]bool{}
			}
		}
	}
	return out
}

// cvMask returns the tiered view of msgs. The input is never modified; when
// nothing is masked the same slice comes back.
func cvMask(msgs []Message, p cvPolicy) ([]Message, cvStats) {
	var st cvStats
	keep := map[int]bool{}
	for i, n := len(msgs)-1, 0; i >= 0 && n < p.Keep; i-- {
		if msgs[i].Role == "tool" {
			keep[i] = true
			n++
		}
	}
	spare := map[string]bool{}
	for _, n := range p.Spare {
		spare[n] = true
	}
	stale := cvStaleReads(msgs, p.Reads)
	var out []Message
	for i, m := range msgs {
		st.Before += len(m.Content)
		why := stale[i]
		mask := m.Role == "tool" && len(m.Content) > p.MinBytes && !strings.HasPrefix(m.Content, cvStubMark) && !spare[m.ToolName]
		if mask && p.Reads != "" && m.ToolName == "read" {
			mask = why != "" // a read goes only when a later message made it redundant
		} else if mask {
			mask = !keep[i]
		}
		if !mask {
			st.After += len(m.Content)
			continue
		}
		if out == nil {
			out = append([]Message(nil), msgs...)
		}
		out[i].Content = cvStub(m, why)
		out[i].Images = nil
		st.Masked++
		st.After += len(out[i].Content)
	}
	if out == nil {
		return msgs, st
	}
	return out, st
}

const cvStubMark = "[old "

// cvStub is the one-line stand-in for an old tool result: its size, its first
// line and, when the full text was spilled to a file, where it is.
func cvStub(m Message, why string) string {
	c := m.Content
	first := strings.TrimSpace(c)
	if i := strings.IndexByte(first, '\n'); i >= 0 {
		first = first[:i]
	}
	first = oneLine(first, cvStubHead)
	name := m.ToolName
	if name == "" {
		name = "tool"
	}
	if why == "" {
		why = "to save context"
	} else {
		why = "(" + why + ")"
	}
	s := fmt.Sprintf("%s%s result elided %s: %d bytes, %d lines. First line: %q.", cvStubMark, name, why, len(c), strings.Count(c, "\n")+1, first)
	if mm := cvSpillRx.R().FindStringSubmatch(c); mm != nil {
		s += " Full output: " + strings.TrimRight(mm[1], ".,;]") + "."
	}
	return s + " Run the call again if you need it.]"
}
