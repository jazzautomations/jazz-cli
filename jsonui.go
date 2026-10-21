package main

// jsonui.go: machine-readable headless output for --output-format json|stream-json (G5). Owner: ui.
// Contract surface (CONTRACT.md): JSONUI, NewJSONUI; implements HeadlessSink.
//
// stream-json writes one JSON object per line as things happen:
//   {"type":"init","session_id","model","cwd","tools"}
//   {"type":"text","delta"}
//   {"type":"tool_start","id","name","args"}
//   {"type":"tool_end","id","name","is_error","summary","output"}   (output ≤ 4 KB)
//   {"type":"notice"|"warn"|"error","text"}
//   {"type":"denied","name","args","reason"}
//   {"type":"result","status","text","error","usage","cost","turns","tools","duration_ms","session_id","model","recover"?}
// json writes only the final result object.

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// jsonOutputMax caps tool output in tool_end events.
const jsonOutputMax = 4 << 10

// JSONUI writes one JSON object per line. stream=false buffers everything and
// writes only the final result object; stream=true writes every event.
type JSONUI struct {
	w      io.Writer
	stream bool
	mu     sync.Mutex // guards w and the fields below
	cur    strings.Builder
	last   string
	steps  int
	tools  int
	// Extra: fields added to the result object (outcome, checkpoint,
	// handoff, resume; obs.go). Set before Finish.
	Extra map[string]any
}

func NewJSONUI(w io.Writer, stream bool) *JSONUI { return &JSONUI{w: w, stream: stream} }

// emit writes one event line (j.mu held).
func (j *JSONUI) emit(v map[string]any) {
	b, err := json.Marshal(v)
	if err != nil {
		b, _ = json.Marshal(map[string]any{"type": "error", "text": "json: " + err.Error()})
	}
	_, _ = j.w.Write(append(b, '\n'))
}

func (j *JSONUI) event(v map[string]any) {
	if !j.stream {
		return
	}
	j.mu.Lock()
	j.emit(v)
	j.mu.Unlock()
}

func (j *JSONUI) StreamText(s string) {
	if s == "" {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.cur.WriteString(s)
	if j.stream {
		j.emit(map[string]any{"type": "text", "delta": jsonValidUTF8(sanitizeTerm(s))})
	}
}
func (j *JSONUI) StreamReasoning(string) {}
func (j *JSONUI) Thinking(string)        {}
func (j *JSONUI) StopThinking()          {}

// flushText ends the current assistant message (j.mu held).
func (j *JSONUI) flushText() {
	if t := strings.TrimSpace(j.cur.String()); t != "" {
		j.last = t
	}
	j.cur.Reset()
}

func (j *JSONUI) ToolStart(id, name, detail string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.flushText()
	if j.stream {
		j.emit(map[string]any{"type": "tool_start", "id": id, "name": name, "args": jsonValidUTF8(detail)})
	}
}

func (j *JSONUI) ToolEnd(id, name string, out ToolOutput, d time.Duration) {
	if !j.stream {
		return
	}
	sum := out.Display.Summary
	if sum == "" {
		sum = firstSentence(out.Text)
	}
	j.event(map[string]any{"type": "tool_end", "id": id, "name": name, "is_error": out.IsErr,
		"summary": jsonValidUTF8(sum), "output": jsonClip(out.Text, jsonOutputMax), "duration_ms": d.Milliseconds()})
}

func (j *JSONUI) Notice(m string) { j.event(map[string]any{"type": "notice", "text": m}) }
func (j *JSONUI) Warn(m string)   { j.event(map[string]any{"type": "warn", "text": m}) }
func (j *JSONUI) Error(m string)  { j.event(map[string]any{"type": "error", "text": m}) }

// Approve never asks: headless runs deny anything that needs confirmation
// (use --allow-tools or --yolo) and report it as a "denied" event.
func (j *JSONUI) Approve(ctx context.Context, tool, detail, reason string) (Approval, string) {
	j.event(map[string]any{"type": "denied", "name": tool, "args": jsonValidUTF8(detail), "reason": reason})
	return Deny, ""
}

func (j *JSONUI) TurnEnd(st TurnStats) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.flushText()
	j.steps += st.Steps
	j.tools += st.Tools
}

func (j *JSONUI) SetMode(string) {}
func (j *JSONUI) Jobs(int)       {}
func (j *JSONUI) Bell()          {}

func (j *JSONUI) Begin(info RunInfo) {
	tools := info.Tools
	if tools == nil {
		tools = []string{}
	}
	j.event(map[string]any{"type": "init", "session_id": info.SessionID, "model": info.Model, "cwd": info.Cwd, "tools": tools})
}

// Finish writes the result object (both modes). Turns and Tools fall back to
// the totals seen in TurnEnd when the result leaves them at zero.
func (j *JSONUI) Finish(res RunResult) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.flushText()
	text := res.Text
	if text == "" {
		text = j.last
	}
	if res.Turns == 0 {
		res.Turns = j.steps
	}
	if res.Tools == 0 {
		res.Tools = j.tools
	}
	status := string(res.Status)
	if status == "" {
		status = string(StopDone)
	}
	obj := map[string]any{"type": "result", "status": status, "text": jsonValidUTF8(sanitizeTerm(text)), "error": res.Err,
		"usage": res.Usage, "cost": res.Cost, "turns": res.Turns, "tools": res.Tools,
		"duration_ms": res.Duration.Milliseconds(), "session_id": res.SessionID, "model": res.Model}
	for k, v := range j.Extra {
		if _, taken := obj[k]; !taken {
			obj[k] = v
		}
	}
	if res.Recover != "" {
		obj["recover"] = res.Recover // --recover handed the turn off: loop | until_repeat | tool_errors
	}
	j.emit(obj)
}

// jsonClip keeps at most max bytes, cut on a rune boundary, with a marker.
func jsonClip(s string, max int) string {
	s = jsonValidUTF8(s)
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n…[truncated]"
}

func jsonValidUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, "�")
}
