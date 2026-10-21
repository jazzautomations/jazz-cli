package main

// obs.go: observability and zero-work-loss (prefix "ob").
//
//   - One append-only event stream per session, <session>.events.jsonl next
//     to the session file: requests, tool calls, guard decisions, checkpoints,
//     budget, skills, compactions, recoveries, errors, run end. One small JSON
//     line per event, no fsync except on an abnormal end. Schema:
//     docs/observability.md (stable: "v" is bumped on a breaking change).
//   - Write-ahead tool records in the session JSONL (Session.ToolStart /
//     ToolResult): nothing the model produced or a tool returned lives only in
//     memory.
//   - Abnormal end of a headless run (timeout, provider error, 429, Ctrl+C,
//     crash, spiral breaker, budget cap): shadow-git checkpoint, handoff note,
//     end record; `jazz -p -r <id>` / -c / --resume-last-in picks it up.
//   - Deadline grace for -p --timeout: past timeout-grace no new request
//     starts and the turn ends cleanly as an incomplete run.
//   - `jazz trace`, `jazz sessions` and --metrics-out read the event stream.
//
// Nothing here touches the system prompt: per-turn data only goes to files
// and, on resume, into a user message (prompt cache stays intact).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	obEventsSuffix = ".events.jsonl"
	obHandoffExt   = ".handoff.md"
	obSchema       = 1
)

// Outcomes of a run (exit codes: 0 completed, 5 denied, anything else incomplete).
const (
	obCompleted  = "completed"
	obIncomplete = "incomplete"
	obDenied     = "denied"
)

// obLog is one session's event stream. Safe for concurrent use.
type obLog struct {
	mu    sync.Mutex
	f     *os.File
	path  string
	sid   string
	seq   int64
	n     int64 // events written by this process
	bytes int64
	ox    *otlpExp // OTLP exporter (nil = off); fed under mu, never blocks
}

// obCur is the event stream of the main session of this process (nil = none):
// code without an Agent handle (checkpoints, audit, dbg tee) writes here.
var obCur atomic.Pointer[obLog]

func obEventsPath(sessPath string) string {
	return strings.TrimSuffix(sessPath, ".jsonl") + obEventsSuffix
}

func obHandoffPath(sessPath string) string {
	return strings.TrimSuffix(sessPath, ".jsonl") + obHandoffExt
}

// obOpen opens (appending) the event stream of a session; seq continues
// from the last event already in the file.
func obOpen(sessPath, sid string) (*obLog, error) {
	p := obEventsPath(sessPath)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	l := &obLog{f: f, path: p, sid: sid, ox: otlpGet()}
	if last := obTailLine(f); len(last) > 0 {
		var ev struct {
			Seq int64 `json:"seq"`
		}
		if json.Unmarshal(last, &ev) == nil {
			l.seq = ev.Seq
		}
	}
	return l, nil
}

// obTailLine returns the last complete line of f (nil when empty). A torn
// last line (crash mid-write) is skipped and the file gets a fresh newline.
func obTailLine(f *os.File) []byte {
	fi, err := f.Stat()
	if err != nil || fi.Size() == 0 {
		return nil
	}
	size := fi.Size()
	for chunk := int64(4 << 10); ; chunk *= 4 {
		off := max(0, size-chunk)
		buf := make([]byte, size-off)
		if _, err := f.ReadAt(buf, off); err != nil && !errors.Is(err, io.EOF) {
			return nil
		}
		torn := buf[len(buf)-1] != '\n'
		lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
		if torn && off == 0 && len(lines) == 1 {
			_, _ = f.Write([]byte{'\n'})
			return nil
		}
		if len(lines) >= 2 || off == 0 {
			i := len(lines) - 1
			if torn {
				_, _ = f.Write([]byte{'\n'})
				i--
			}
			if i < 0 || (off > 0 && i == 0) {
				if off == 0 {
					return nil
				}
				continue
			}
			return []byte(lines[i])
		}
		if off == 0 {
			return nil
		}
	}
}

// emit appends one event. Field names "v", "seq", "ts", "sid" and "kind" are
// reserved. redact runs redactSecrets over the line (error payloads, tees).
func (l *obLog) emit(kind string, f map[string]any, redact bool) {
	if l == nil {
		return
	}
	ev := make(map[string]any, len(f)+5)
	for k, v := range f {
		ev[k] = v
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return
	}
	l.seq++
	ev["v"], ev["seq"], ev["sid"], ev["kind"] = obSchema, l.seq, l.sid, kind
	ev["ts"] = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	b, err := json.Marshal(ev)
	if err != nil {
		b, _ = json.Marshal(map[string]any{"v": obSchema, "seq": l.seq, "sid": l.sid, "kind": kind, "ts": ev["ts"], "marshal_error": err.Error()})
	}
	if redact {
		b = []byte(redactSecrets(string(b)))
	}
	l.ox.feed(ev) // exporter redacts its own string attributes
	n, _ := l.f.Write(append(b, '\n'))
	l.n++
	l.bytes += int64(n)
}

func (l *obLog) sync() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		_ = l.f.Sync()
	}
}

func (l *obLog) close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		l.f.Close()
		l.f = nil
	}
}

// obEmit writes to the process's current stream (no-op without one).
func obEmit(kind string, f map[string]any) { obCur.Load().emit(kind, f, false) }

// obTeeKinds are the dbg kinds copied into the event stream even without
// --debug (small payloads only; request bodies and SSE stay in the debug log).
var obTeeKinds = map[string]string{
	"budget": "budget", "skill": "skill", "compact": "compact", "recover": "recover",
	"guard": "guard", "injection": "guard", "supply": "guard", "smart": "guard", "blocked": "guard",
	"stall": "provider", "netwait": "provider", "sticky": "provider", "repair": "repair",
	"nudge": "nudge", "quality": "quality", "until": "until", "review": "review",
	"checkpoint": "checkpoint", "plan_cache": "plan_cache", "fastpath": "fastpath",
}

// obTee is called by dbg for every debug event.
func obTee(kind string, v any) {
	ev, ok := obTeeKinds[kind]
	if !ok {
		return
	}
	l := obCur.Load()
	if l == nil {
		return
	}
	f := map[string]any{"src": kind}
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if s, ok := val.(string); ok && len(s) > 400 {
				val = oneLine(s, 400)
			}
			f[k] = val
		}
	case string:
		f["detail"] = oneLine(x, 400)
	default:
		f["detail"] = oneLine(fmt.Sprint(x), 400)
	}
	if ev == "guard" {
		f["guard"] = kind
	}
	l.emit(ev, f, true)
}

func obSHA(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// obShort is the 12-char prefix used in human output.
func obShort(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// ---------- agent side ----------

// obState is the per-agent observability state.
type obState struct {
	mu        sync.Mutex
	step      int
	promptSHA string
	model     string
	guard     string       // which guard refused the last call (set in execTool)
	meta      *sessMsgMeta // last response, consumed by the next assistant append
	wrote     bool         // a file may have changed in this step
	soft      atomic.Bool  // deadline grace reached: no new request starts
	softWhy   string
	reqT0     time.Time
}

// obs returns the agent's observability state (created on first use).
func (a *Agent) obs() *obState {
	if p := a.obst.Load(); p != nil {
		return p
	}
	a.obst.CompareAndSwap(nil, &obState{})
	return a.obst.Load()
}

// obEv writes an event tagged with the current step (and sub for subagents).
func (a *Agent) obEv(kind string, f map[string]any) {
	l := obCur.Load()
	if l == nil {
		return
	}
	if f == nil {
		f = map[string]any{}
	}
	st := a.obs()
	st.mu.Lock()
	if st.step > 0 {
		f["step"] = st.step
	}
	st.mu.Unlock()
	if a.sub {
		f["sub"] = true
	}
	if a.sa.id != "" {
		f["sub_id"] = a.sa.id
	}
	l.emit(kind, f, false)
}

// obAttach opens the event stream of the agent's session and makes it the
// process stream (source: startup | resume | fork).
func (a *Agent) obAttach(source string) {
	if a == nil || a.sub || a.sess == nil {
		return
	}
	l, err := obOpen(a.sess.Path, a.sess.ID)
	if err != nil {
		dbg("obs", map[string]any{"open_error": err.Error()})
		return
	}
	if old := obCur.Swap(l); old != nil && old != l {
		old.close()
	}
	auSetSession(a.sess.ID)
	l.emit("session.start", map[string]any{"source": source, "model": a.Model, "cwd": a.tools.Root,
		"version": version, "pid": os.Getpid(), "os": runtime.GOOS, "arch": runtime.GOARCH, "leaf": a.sess.Leaf()}, false)
}

// obTurnStart records a new turn; input is the user's request as typed.
func (a *Agent) obTurnStart(input string) {
	st := a.obs()
	st.mu.Lock()
	st.step, st.promptSHA, st.model, st.wrote = 0, obSHA(input), a.Model, false
	sha := st.promptSHA
	st.mu.Unlock()
	if !a.sub {
		a.tools.obSetTrace(a.SessionID, a.Model, sha)
	}
	a.obEv("turn.start", map[string]any{"prompt_sha": sha, "input_bytes": len(input), "model": a.Model, "msgs": len(a.Msgs)})
}

func (a *Agent) obStep(step int, model string) {
	st := a.obs()
	st.mu.Lock()
	st.step, st.model = step, model
	st.mu.Unlock()
	if !a.sub {
		a.tools.obSetStep(step, model)
		auSetStep(step)
	}
}

func (a *Agent) obTurnEnd(stop StopReason, steps, tools int, d time.Duration) {
	u := a.turnUsage
	a.obEv("turn.end", map[string]any{"stop": string(stop), "steps": steps, "tools": tools, "dur_ms": d.Milliseconds(),
		"in": u.In, "out": u.Out, "cache_read": u.CacheRead, "cache_write": u.CacheWrite, "cost": u.Cost, "ctx_tokens": a.estTokens()})
}

func (a *Agent) obReqStart(ref string, cand, attempt int) {
	st := a.obs()
	st.mu.Lock()
	st.reqT0 = time.Now()
	st.mu.Unlock()
	p, _ := SplitModel(ref)
	a.obEv("request.start", map[string]any{"model": ref, "provider": p, "cand": cand, "attempt": attempt, "msgs": len(a.Msgs), "est_tokens": a.estTokens()})
}

// obReqEnd records the end of one provider request (err != nil = failed attempt).
func (a *Agent) obReqEnd(ref string, cand, attempt int, resp *ChatResponse, err error) {
	st := a.obs()
	st.mu.Lock()
	lat := time.Since(st.reqT0).Milliseconds()
	st.mu.Unlock()
	p, _ := SplitModel(ref)
	f := map[string]any{"model": ref, "provider": p, "cand": cand, "attempt": attempt, "latency_ms": lat}
	if resp != nil {
		f["in"], f["out"], f["cache_read"], f["cache_write"], f["cost"] = resp.Usage.In, resp.Usage.Out, resp.Usage.CacheRead, resp.Usage.CacheWrite, resp.Usage.Cost
		f["finish"], f["tool_calls"], f["text_bytes"] = resp.Stop, len(resp.ToolCalls), len(resp.Text)
	}
	if err != nil {
		f["error"] = oneLine(err.Error(), 300)
		var ae *APIError
		if errors.As(err, &ae) {
			f["status"], f["daily"] = ae.Status, ae.Daily
			f["payload"] = oneLine(ae.Body, 300)
			if ae.RetryAfter > 0 {
				f["retry_after_ms"] = ae.RetryAfter.Milliseconds()
			}
		}
		obCur.Load().emit("request.end", a.obTag(f), true)
		return
	}
	a.obEv("request.end", f)
	if resp != nil {
		st.mu.Lock()
		st.meta = &sessMsgMeta{Model: ref, Provider: p, Stop: resp.Stop, Usage: resp.Usage, LatencyMS: lat}
		st.mu.Unlock()
	}
}

func (a *Agent) obTag(f map[string]any) map[string]any {
	st := a.obs()
	st.mu.Lock()
	if st.step > 0 {
		f["step"] = st.step
	}
	st.mu.Unlock()
	if a.sub {
		f["sub"] = true
	}
	if a.sa.id != "" {
		f["sub_id"] = a.sa.id
	}
	return f
}

// obTakeMeta returns (once) what produced the next assistant message.
func (a *Agent) obTakeMeta(m Message) *sessMsgMeta {
	if m.Role != "assistant" {
		return nil
	}
	st := a.obs()
	st.mu.Lock()
	defer st.mu.Unlock()
	meta := st.meta
	st.meta = nil
	return meta
}

// obSetGuard names the guard that refused the current call (execTool).
func (a *Agent) obSetGuard(g string) {
	st := a.obs()
	st.mu.Lock()
	st.guard = g
	st.mu.Unlock()
}

var obWriteTools = map[string]bool{"write": true, "edit": true}

// obExecTool runs execTool with the write-ahead records, events and audit
// records around it. step is the model request the call came from.
func (a *Agent) obExecTool(ctx context.Context, step int, tc ToolCall) (out ToolOutput, ran, denied bool) {
	w := a.obToolBegin(step, tc)
	out, ran, denied = a.execTool(ctx, tc)
	a.obToolEnd(ctx, step, tc, w, out, ran, denied)
	return out, ran, denied
}

// obToolRec is what obToolBegin noted for obToolEnd.
type obToolRec struct {
	argsSHA, path, before string
	t0                    time.Time
}

// obToolBegin writes the write-ahead record and the tool.start event.
func (a *Agent) obToolBegin(step int, tc ToolCall) obToolRec {
	a.sess.ToolStart(tc, step)
	w := obToolRec{argsSHA: obSHA(tc.Args)}
	a.obEv("tool.start", map[string]any{"id": tc.ID, "name": tc.Name, "args_sha": w.argsSHA, "args_bytes": len(tc.Args), "lane": a.ptLaneOf(a.ptIdx, tc)})
	if obWriteTools[tc.Name] {
		if p := argPath(tc.Args); p != "" {
			w.path = a.obAbs(p)
			w.before = obFileSHA(w.path)
		}
	}
	a.obSetGuard("")
	a.ptLane = ptLaneSeq // execTool switches it when it takes an early result (partools.go)
	w.t0 = time.Now()
	return w
}

// obToolEnd writes the tool_result record, the tool.end event and the audit
// records of a call that obToolBegin opened.
func (a *Agent) obToolEnd(ctx context.Context, step int, tc ToolCall, w obToolRec, out ToolOutput, ran, denied bool) {
	argsSHA, path, before := w.argsSHA, w.path, w.before
	d := time.Since(w.t0)
	a.sess.ToolResult(tc, step, out, d)
	f := map[string]any{"id": tc.ID, "name": tc.Name, "dur_ms": d.Milliseconds(), "bytes": len(out.Text), "is_err": out.IsErr, "ran": ran, "lane": a.ptLane}
	if denied {
		f["denied"] = true
	}
	if out.Blocked != "" {
		f["blocked"] = true
	}
	if strings.Contains(out.Text, spillDir()) {
		f["truncated"] = true
	}
	a.obEv("tool.end", f)
	if !ran {
		st := a.obs()
		st.mu.Lock()
		g := st.guard
		st.mu.Unlock()
		if g == "" {
			g = "permission"
		}
		if ctx.Err() != nil && !denied {
			g = "cancel"
		}
		outcome := "refused"
		if denied {
			outcome = "denied"
		}
		reason := out.Blocked
		if reason == "" {
			reason = oneLine(out.Text, 200)
		}
		target := toolDetail(tc.Name, tc.Args)
		a.obEv("guard", map[string]any{"guard": g, "tool": tc.Name, "target": oneLine(redactSecrets(target), 200), "reason": reason, "outcome": outcome})
		if g != "cancel" {
			auditRec("action.refused", map[string]any{"guard": g, "tool": tc.Name, "target": oneLine(redactSecrets(target), 200), "reason": oneLine(reason, 300), "outcome": outcome, "args_sha": argsSHA})
		}
		return
	}
	switch {
	case path != "":
		after := obFileSHA(path)
		if after != before {
			op := "edit"
			if before == "" {
				op = "create"
			} else if tc.Name == "write" {
				op = "write"
			}
			a.obMarkWrote()
			auditRec("file.change", map[string]any{"path": path, "op": op, "via": tc.Name, "before": obPrefixed(before), "after": obPrefixed(after), "args_sha": argsSHA})
		}
	case tc.Name == "bash":
		cmd, _ := argStr(acArgsMap(tc.Name, tc.Args), "command")
		auditRec("shell", map[string]any{"cmd_sha": obSHA(cmd), "cmd": oneLine(redactSecrets(cmd), 200), "is_err": out.IsErr, "dur_ms": d.Milliseconds()})
		if !IsReadOnly(cmd) {
			a.obMarkWrote()
		}
	}
}

func obPrefixed(h string) string {
	if h == "" {
		return ""
	}
	return "sha256:" + h
}

func (a *Agent) obMarkWrote() {
	st := a.obs()
	st.mu.Lock()
	st.wrote = true
	st.mu.Unlock()
	if a.sa.parent != nil { // a worker subagent's change is checkpointed by the parent's step
		a.sa.parent.obMarkWrote()
	}
}

func (a *Agent) obAbs(p string) string {
	if !filepath.IsAbs(p) {
		p = filepath.Join(a.tools.Root, p)
	}
	return filepath.Clean(p)
}

// obFileSHA is the sha256 of a file ("" = missing or unreadable).
func obFileSHA(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// obAfterStep commits a traced checkpoint when the step may have changed files.
func (a *Agent) obAfterStep(step int) {
	if a.sub {
		return
	}
	st := a.obs()
	st.mu.Lock()
	wrote := st.wrote
	st.wrote = false
	st.mu.Unlock()
	if wrote {
		a.tools.obCheckpoint("step")
	}
}

// SoftStop asks the running turn to stop before its next model request
// (deadline grace). The tool in flight finishes; later calls of the batch
// do not start.
func (a *Agent) SoftStop(why string) {
	st := a.obs()
	st.mu.Lock()
	st.softWhy = why
	st.mu.Unlock()
	st.soft.Store(true)
	a.obEv("deadline.soft", map[string]any{"why": why})
}

// obSoftStopped: the deadline grace was reached for this agent or, for a
// parallel subagent, for the agent that started it.
func (a *Agent) obSoftStopped() bool {
	return a.obs().soft.Load() || (a.sa.parent != nil && a.sa.parent.obSoftStopped())
}

// obApproval records a confirmation the user answered (overrides).
func (a *Agent) obApproval(tool, detail, reason string, ap Approval) {
	ans := "deny"
	switch ap {
	case Allow:
		ans = "allow"
	case AllowAlways:
		ans = "allow_always"
	}
	f := map[string]any{"tool": tool, "target": oneLine(redactSecrets(detail), 200), "reason": reason, "answer": ans}
	a.obEv("approval", f)
	auditRec("approval", f)
}

// ---------- abnormal end ----------

// obOutcome classifies a stop reason.
func obOutcome(s StopReason) string {
	switch s {
	case StopDone, "":
		return obCompleted
	case StopDenied:
		return obDenied
	}
	return obIncomplete
}

// obRunEnd is what obFinishRun did.
type obRunEnd struct {
	Outcome    string
	Checkpoint string
	Handoff    string // file
	Note       string
}

// obStopWhy is the reason text (English, for the model) of a stop.
func obStopWhy(s StopReason, errText string) string {
	switch s {
	case StopTimeout:
		return "the run deadline (--timeout) was reached"
	case StopInterrupted:
		return "it was interrupted (Ctrl+C / SIGTERM)"
	case StopError:
		if errText != "" {
			return "a provider error ended it (" + oneLine(errText, 200) + ")"
		}
		return "a provider error ended it"
	case StopStepLimit:
		return "the step or budget limit of the turn was reached"
	case StopLoop:
		return "the repetition guard stopped a loop"
	case StopDenied:
		return "a guard refused repeated attempts (spiral breaker) or an action was denied"
	case StopTruncated:
		return "the model's output kept hitting the output token limit"
	case StopUnverified:
		return "the final answer claimed actions that never ran"
	case StopUntilFailed:
		return "the --until check was still failing"
	case StopQualityFailed:
		return "the quality check was still failing"
	case "crash":
		return "jazz crashed (internal error)"
	}
	return "it stopped (" + string(s) + ")"
}

// obHandoff builds the deterministic resume note (no LLM call). It is meant
// for the model (English) and is also saved for humans next to the session.
func (a *Agent) obHandoff(stop StopReason, errText, checkpoint string) string {
	var b strings.Builder
	b.WriteString("<system-reminder>Resume note from the harness. The previous run of this session stopped before finishing: " + obStopWhy(stop, errText) + ". ")
	b.WriteString("All of its work is kept: the conversation above and the files on disk")
	if checkpoint != "" {
		b.WriteString(" (checkpoint " + obShort(checkpoint) + ")")
	}
	b.WriteString(". Continue exactly where it stopped; do not redo finished work. A result marked TOOL_NOT_STARTED did not run; one marked TOOL_OUTCOME_UNKNOWN may have run partly: check the state before repeating it.</system-reminder>\n\n")
	task := ""
	for _, m := range a.Msgs {
		if m.Role == "user" && !strings.HasPrefix(m.Content, "<summary>") {
			if t := strings.TrimSpace(acStripReminders(m.Content)); t != "" {
				task = t
				break
			}
		}
	}
	if task != "" {
		b.WriteString("## Task\n" + acClip(task, 1200) + "\n\n")
	}
	if files := a.tools.rcTurnFiles(); len(files) > 0 {
		b.WriteString("## Files changed in the last turn\n")
		for _, f := range acClipList(files, 30) {
			b.WriteString("- " + f + "\n")
		}
		b.WriteString("\n")
	}
	// the last tool calls and how they ended
	var trail []string
	pending := map[string]ToolCall{}
	lastText, lastVerify := "", ""
	for i, m := range a.Msgs {
		switch m.Role {
		case "assistant":
			if t := strings.TrimSpace(m.Content); t != "" {
				lastText = t
			}
			for _, tc := range m.ToolCalls {
				pending[tc.ID] = tc
			}
		case "tool":
			tc, ok := pending[m.ToolCallID]
			delete(pending, m.ToolCallID)
			if !ok {
				continue
			}
			state := "ok"
			switch {
			case strings.Contains(m.Content, "TOOL_NOT_STARTED") || strings.Contains(m.Content, "TOOL_NOT_EXECUTED"):
				state = "did not run"
			case strings.Contains(m.Content, "TOOL_OUTCOME_UNKNOWN"):
				state = "outcome unknown"
			case rcExitCode(m.Content) > 0:
				state = fmt.Sprintf("exit %d", rcExitCode(m.Content))
			}
			line := fmt.Sprintf("%s %s → %s", tc.Name, oneLine(toolDetail(tc.Name, tc.Args), 120), state)
			trail = append(trail, line)
			if tc.Name == "bash" && rcIsVerifyCmd(rcArg(tc, "command")) {
				lastVerify = fmt.Sprintf("$ %s → %s\n%s", oneLine(rcArg(tc, "command"), 160), state, truncateHeadTail(strings.TrimSpace(m.Content), 800))
			}
		}
		_ = i
	}
	if len(trail) > 0 {
		b.WriteString("## Last tool calls (oldest first)\n")
		for i, t := range trail[max(0, len(trail)-8):] {
			fmt.Fprintf(&b, "%d. %s\n", i+1, t)
		}
		b.WriteString("\n")
	}
	if lastVerify != "" {
		b.WriteString("## Last verification\n" + lastVerify + "\n\n")
	}
	if lastText != "" {
		b.WriteString("## Last thing the assistant said\n" + acClip(lastText, 600) + "\n\n")
	}
	open := a.openTodos()
	if open != "" {
		b.WriteString("## Open todo items\n" + acClip(open, 800) + "\n\n")
	}
	b.WriteString("## Next step\n")
	switch {
	case open != "":
		b.WriteString("Continue with the open todo items above.")
	case lastVerify != "":
		b.WriteString("Re-run the last verification, fix what still fails, then give the final answer.")
	default:
		b.WriteString("Check the current state of the files, finish the task and verify it.")
	}
	b.WriteString("\n")
	return b.String()
}

// obFinishRun closes a headless run: on anything but a completed run it
// takes a checkpoint, writes the handoff note and fsyncs the logs. It always
// writes the end record and the run.end event.
func obFinishRun(ag *Agent, tools *Toolbox, res RunResult, exit int) obRunEnd {
	r := obRunEnd{Outcome: obOutcome(res.Status)}
	if r.Outcome != obCompleted {
		r.Checkpoint = tools.obCheckpoint("abnormal-end:" + string(res.Status))
		if ag.sess != nil {
			r.Note = ag.obHandoff(res.Status, res.Err, r.Checkpoint)
			p := obHandoffPath(ag.sess.Path)
			head := fmt.Sprintf("# Nota de passagem — sessão %s\n\n- status: %s (%s)\n- checkpoint: %s\n- retomar: jazz -p -r %s   (ou: jazz -p --resume-last-in %s)\n\n",
				ag.SessionID, res.Status, r.Outcome, orDash(r.Checkpoint), ag.SessionID, tools.Root)
			if os.WriteFile(p, []byte(head+redactSecrets(r.Note)), 0o600) == nil {
				r.Handoff = p
			}
		}
	}
	ag.sess.End(sessEnd{Status: string(res.Status), Outcome: r.Outcome, Checkpoint: r.Checkpoint, Handoff: r.Handoff, Note: redactSecrets(r.Note)})
	f := map[string]any{"status": string(res.Status), "outcome": r.Outcome, "exit": exit, "dur_ms": res.Duration.Milliseconds(),
		"in": res.Usage.In, "out": res.Usage.Out, "cache_read": res.Usage.CacheRead, "cost": res.Cost, "model": res.Model}
	if r.Checkpoint != "" {
		f["checkpoint"] = r.Checkpoint
	}
	if r.Handoff != "" {
		f["handoff"] = r.Handoff
	}
	if res.Err != "" {
		f["error"] = oneLine(res.Err, 300)
	}
	obCur.Load().emit("run.end", f, true)
	auditRec("run.end", map[string]any{"status": string(res.Status), "outcome": r.Outcome, "exit": exit, "checkpoint": r.Checkpoint})
	if r.Outcome != obCompleted {
		obSyncAll(ag)
	}
	return r
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// obSyncAll fsyncs the session, its events and the audit log.
func obSyncAll(ag *Agent) {
	if ag != nil {
		ag.sess.Sync()
	}
	obCur.Load().sync()
	auSync()
}

// obCrash is the crash cleanup: checkpoint, handoff, end record, fsync.
func obCrash(ag *Agent, tools *Toolbox) {
	if ag == nil || ag.sess == nil {
		return
	}
	obFinishRun(ag, tools, RunResult{Status: "crash", SessionID: ag.SessionID, Usage: ag.Usage, Model: ag.TurnModel()}, 70)
}

// obResumeNote returns the handoff note when the session's last run ended
// incomplete ("" = nothing to resume).
func obResumeNote(sessPath string) string {
	e := sessLastEnd(sessPath)
	if e == nil || e.Outcome == obCompleted {
		return ""
	}
	if e.Note != "" {
		return e.Note
	}
	return "<system-reminder>The previous run of this session stopped before finishing (" + e.Status + "). Continue exactly where it stopped; do not redo finished work.</system-reminder>"
}

// obGrace is how long before --timeout the run stops issuing requests:
// explicit flag, else 10% of the timeout clamped to [5s, 60s].
func obGrace(timeout, flag time.Duration) time.Duration {
	if timeout <= 0 {
		return 0
	}
	g := flag
	if g <= 0 {
		g = min(max(timeout/10, 5*time.Second), 60*time.Second)
	}
	if g >= timeout {
		g = timeout / 2
	}
	return g
}

// ---------- reading the stream: summaries, trace, sessions ----------

type obEvent map[string]any

func (e obEvent) str(k string) string {
	s, _ := e[k].(string)
	return s
}

func (e obEvent) num(k string) float64 {
	f, _ := e[k].(float64)
	return f
}

func (e obEvent) int(k string) int { return int(e.num(k)) }

func (e obEvent) flag(k string) bool {
	b, _ := e[k].(bool)
	return b
}

func (e obEvent) time() time.Time {
	t, _ := time.Parse(time.RFC3339Nano, e.str("ts"))
	return t
}

// obReadEvents loads an events file (torn lines skipped).
func obReadEvents(p string) ([]obEvent, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var out []obEvent
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e obEvent
		if json.Unmarshal([]byte(line), &e) == nil {
			out = append(out, e)
		}
	}
	return out, nil
}

// obSummary is the totals of one session (--metrics-out, jazz sessions).
type obSummary struct {
	SessionID   string         `json:"session_id"`
	Status      string         `json:"status"`
	Outcome     string         `json:"outcome"`
	Exit        int            `json:"exit_code"`
	Start       string         `json:"start,omitempty"`
	End         string         `json:"end,omitempty"`
	DurationMS  int64          `json:"duration_ms"` // wall time, first to last event
	ActiveMS    int64          `json:"active_ms"`   // sum of turn durations (resumes add up, idle gaps do not)
	Runs        int            `json:"runs"`
	Turns       int            `json:"turns"`
	Requests    int            `json:"requests"`
	ReqErrors   int            `json:"request_errors"`
	In          int            `json:"tokens_in"`
	Out         int            `json:"tokens_out"`
	CacheRead   int            `json:"tokens_cache_read"`
	CacheWrite  int            `json:"tokens_cache_write"`
	Cost        float64        `json:"cost_usd"`
	ReqMS       int64          `json:"request_ms"`
	Tools       int            `json:"tool_calls"`
	ToolErrors  int            `json:"tool_errors"`
	ToolMS      int64          `json:"tool_ms"`
	Refused     int            `json:"refused"`
	Denied      int            `json:"denied"`
	Checkpoints int            `json:"checkpoints"`
	Compactions int            `json:"compactions"`
	Recoveries  int            `json:"recoveries"`
	Truncations int            `json:"truncated_responses"`
	ByTool      map[string]int `json:"by_tool"`
	Models      map[string]int `json:"models"`
	Handoff     string         `json:"handoff,omitempty"`
	Checkpoint  string         `json:"checkpoint,omitempty"`
	Events      int            `json:"events"`
}

func obSummarize(evs []obEvent) obSummary {
	s := obSummary{ByTool: map[string]int{}, Models: map[string]int{}, Status: "aberta"}
	var t0, t1 time.Time
	for _, e := range evs {
		if s.SessionID == "" {
			s.SessionID = e.str("sid")
		}
		if t := e.time(); !t.IsZero() {
			if t0.IsZero() {
				t0 = t
			}
			t1 = t
		}
		switch e.str("kind") {
		case "turn.start":
			s.Turns++
		case "turn.end":
			s.ActiveMS += int64(e.num("dur_ms"))
			s.Status = e.str("stop")
			s.Outcome = obOutcome(StopReason(s.Status))
		case "request.end":
			s.Requests++
			s.ReqMS += int64(e.num("latency_ms"))
			if e.str("error") != "" {
				s.ReqErrors++
				continue
			}
			s.In += e.int("in")
			s.Out += e.int("out")
			s.CacheRead += e.int("cache_read")
			s.CacheWrite += e.int("cache_write")
			s.Cost += e.num("cost")
			s.Models[e.str("model")]++
			switch e.str("finish") {
			case "length", "max_tokens", "max_output_tokens", "incomplete", acStopToolCut:
				s.Truncations++
			}
		case "tool.end":
			s.Tools++
			s.ToolMS += int64(e.num("dur_ms"))
			s.ByTool[e.str("name")]++
			if e.flag("is_err") {
				s.ToolErrors++
			}
		case "guard":
			if e.str("outcome") == "denied" {
				s.Denied++
			} else if e.str("guard") != "cancel" && e.str("src") == "" {
				s.Refused++
			}
		case "checkpoint":
			if e.str("tree") != "" {
				s.Checkpoints++
			}
		case "compact":
			s.Compactions++
		case "recover":
			s.Recoveries++
		case "run.end":
			s.Runs++
			s.Status, s.Outcome, s.Exit = e.str("status"), e.str("outcome"), e.int("exit")
			s.Handoff, s.Checkpoint = e.str("handoff"), e.str("checkpoint")
		}
	}
	s.Events = len(evs)
	if !t0.IsZero() {
		s.Start, s.End = t0.Format(time.RFC3339), t1.Format(time.RFC3339)
		s.DurationMS = t1.Sub(t0).Milliseconds()
	}
	if s.Outcome == "" && s.Status != "aberta" {
		s.Outcome = obOutcome(StopReason(s.Status))
	}
	return s
}

// obWriteMetrics writes the summary of a session's events to path.
func obWriteMetrics(path, sessPath string) error {
	evs, err := obReadEvents(obEventsPath(sessPath))
	if err != nil {
		return err
	}
	b, _ := json.MarshalIndent(obSummarize(evs), "", "  ")
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func obFmtDur(ms float64) string {
	d := time.Duration(ms) * time.Millisecond
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return d.Round(time.Second).String()
}

func obFmtTok(n int) string {
	if n >= 10000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprint(n)
}

// obTraceText renders the events of a session as a PT timeline.
func obTraceText(w io.Writer, evs []obEvent) {
	if len(evs) == 0 {
		fmt.Fprintln(w, "sem eventos")
		return
	}
	t0 := evs[0].time()
	reqN := 0
	for _, e := range evs {
		rel := e.time().Sub(t0)
		pre := fmt.Sprintf("%9s ", "+"+obFmtDur(float64(rel.Milliseconds())))
		step := ""
		if n := e.int("step"); n > 0 {
			step = fmt.Sprintf("[%d] ", n)
		}
		if e.flag("sub") {
			step += "(sub) "
		}
		var line string
		switch e.str("kind") {
		case "session.start":
			line = fmt.Sprintf("sessão %s (%s) · %s · %s", e.str("sid"), e.str("source"), e.str("model"), e.str("cwd"))
		case "turn.start":
			line = fmt.Sprintf("turno começou · prompt sha %s · %d mensagens no histórico", obShort(e.str("prompt_sha")), e.int("msgs"))
		case "turn.end":
			line = fmt.Sprintf("turno terminou: %s · %d passos · %d ferramentas · %s · in %s out %s", e.str("stop"), e.int("steps"), e.int("tools"), obFmtDur(e.num("dur_ms")), obFmtTok(e.int("in")), obFmtTok(e.int("out")))
		case "request.start":
			reqN++
			line = fmt.Sprintf("→ requisição #%d %s (tentativa %d)", reqN, e.str("model"), e.int("attempt")+1)
		case "request.end":
			if er := e.str("error"); er != "" {
				line = fmt.Sprintf("✗ requisição falhou em %s: %s", obFmtDur(e.num("latency_ms")), oneLine(er, 160))
				if st := e.int("status"); st > 0 {
					line += fmt.Sprintf(" (HTTP %d)", st)
				}
			} else {
				line = fmt.Sprintf("← resposta em %s · in %s out %s cache %s · fim %q · %d chamada(s)", obFmtDur(e.num("latency_ms")), obFmtTok(e.int("in")), obFmtTok(e.int("out")), obFmtTok(e.int("cache_read")), e.str("finish"), e.int("tool_calls"))
				if c := e.num("cost"); c > 0 {
					line += " · " + fmtCost(c)
				}
			}
		case "tool.start":
			continue // tool.end carries the duration
		case "tool.end":
			mark := "⎿"
			if e.flag("is_err") {
				mark = "✗"
			}
			if !e.flag("ran") {
				mark = "⊘"
			}
			line = fmt.Sprintf("%s %s %s · %d B", mark, e.str("name"), obFmtDur(e.num("dur_ms")), e.int("bytes"))
			if e.flag("truncated") {
				line += " · cortado"
			}
		case "guard":
			g := e.str("guard")
			if e.str("src") != "" {
				line = fmt.Sprintf("guarda %s: %s", g, obCompact(e, "src", "guard"))
			} else {
				line = fmt.Sprintf("bloqueio [%s] %s %s: %s (%s)", g, e.str("tool"), oneLine(e.str("target"), 80), oneLine(e.str("reason"), 120), e.str("outcome"))
			}
		case "approval":
			line = fmt.Sprintf("aprovação %s: %s %s", e.str("answer"), e.str("tool"), oneLine(e.str("target"), 100))
		case "checkpoint":
			line = fmt.Sprintf("checkpoint %s (%s) commit %s", obShort(e.str("tree")), e.str("reason"), obShort(e.str("commit")))
		case "deadline.soft":
			line = "prazo quase no fim: nenhuma requisição nova (" + e.str("why") + ")"
		case "run.end":
			line = fmt.Sprintf("execução terminou: %s (%s) · saída %d", e.str("status"), obOutcomePT(e.str("outcome")), e.int("exit"))
			if h := e.str("handoff"); h != "" {
				line += " · nota " + h
			}
		default:
			line = e.str("kind") + " " + obCompact(e, "src")
		}
		fmt.Fprintln(w, pre+step+line)
	}
	s := obSummarize(evs)
	fmt.Fprintf(w, "\ntotais: %d requisições (%d com erro, %s) · in %s out %s cache %s", s.Requests, s.ReqErrors, obFmtDur(float64(s.ReqMS)), obFmtTok(s.In), obFmtTok(s.Out), obFmtTok(s.CacheRead))
	if s.Cost > 0 {
		fmt.Fprintf(w, " · %s", fmtCost(s.Cost))
	}
	fmt.Fprintf(w, "\n        %d ferramentas (%d com erro, %s) · %d recusadas · %d negadas · %d checkpoints · %d compactações · %d recuperações\n",
		s.Tools, s.ToolErrors, obFmtDur(float64(s.ToolMS)), s.Refused, s.Denied, s.Checkpoints, s.Compactions, s.Recoveries)
	fmt.Fprintf(w, "        tempo ativo %s (de %s no relógio) · %d execução(ões) · status %s (%s)\n", obFmtDur(float64(s.ActiveMS)), obFmtDur(float64(s.DurationMS)), s.Runs, s.Status, obOutcomePT(s.Outcome))
}

// obCompact renders the non-reserved fields of an event as k=v.
func obCompact(e obEvent, skip ...string) string {
	res := map[string]bool{"v": true, "seq": true, "ts": true, "sid": true, "kind": true, "step": true, "sub": true}
	for _, k := range skip {
		res[k] = true
	}
	var keys []string
	for k := range e {
		if !res[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, e[k]))
	}
	return oneLine(strings.Join(parts, " "), 200)
}

func obOutcomePT(o string) string {
	switch o {
	case obCompleted:
		return "concluída"
	case obIncomplete:
		return "incompleta"
	case obDenied:
		return "negada"
	}
	return "aberta"
}

// obFindSessionPath resolves a session id (prefix ok, "" = newest) in root.
func obFindSessionPath(root, id string) (SessionInfo, error) {
	return pfFindSession(root, id)
}

// cmdTrace: jazz trace [sessão] [--json]
func cmdTrace(root string, args []string, w io.Writer) int {
	id, raw := "", false
	for _, a := range args {
		switch a {
		case "--json", "-json":
			raw = true
		default:
			if strings.HasPrefix(a, "-") {
				fmt.Fprintln(os.Stderr, "uso: jazz trace [id-da-sessão] [--json]")
				return 2
			}
			id = a
		}
	}
	si, err := obFindSessionPath(root, id)
	if err != nil {
		fmt.Fprintln(os.Stderr, "jazz:", err)
		return 1
	}
	p := obEventsPath(si.Path)
	if raw {
		f, err := os.Open(p)
		if err != nil {
			fmt.Fprintln(os.Stderr, "jazz: sessão sem eventos:", err)
			return 1
		}
		defer f.Close()
		_, _ = io.Copy(w, f)
		return 0
	}
	evs, err := obReadEvents(p)
	if err != nil {
		fmt.Fprintln(os.Stderr, "jazz: sessão sem eventos (anterior à v0.6?):", si.ID)
		return 1
	}
	obTraceText(w, evs)
	return 0
}

// cmdSessions: jazz sessions [--json] [-n N]
func cmdSessions(root string, args []string, w io.Writer) int {
	raw, limit := false, 20
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json", "-json":
			raw = true
		case "-n", "--limit":
			if i+1 < len(args) {
				fmt.Sscan(args[i+1], &limit)
				i++
			}
		case "--all", "-a":
			limit = 1 << 30
		}
	}
	list := ListSessions(root)
	if len(list) > limit {
		list = list[:limit]
	}
	type row struct {
		obSummary
		Title string `json:"title"`
		Model string `json:"model"`
	}
	var rows []row
	for _, si := range list {
		evs, _ := obReadEvents(obEventsPath(si.Path))
		s := obSummarize(evs)
		s.SessionID = si.ID
		if e := sessLastEnd(si.Path); e != nil && s.Runs == 0 {
			s.Status, s.Outcome = e.Status, e.Outcome
		}
		rows = append(rows, row{obSummary: s, Title: si.Title, Model: si.Model})
	}
	if raw {
		b, _ := json.MarshalIndent(rows, "", "  ")
		fmt.Fprintln(w, string(b))
		return 0
	}
	if len(rows) == 0 {
		fmt.Fprintln(w, "nenhuma sessão nesta pasta")
		return 0
	}
	fmt.Fprintf(w, "%-19s %-11s %-12s %8s %8s %8s %9s  %s\n", "SESSÃO", "SITUAÇÃO", "STATUS", "IN", "OUT", "CUSTO", "DURAÇÃO", "PEDIDO")
	for _, r := range rows {
		cost := "—"
		if r.Cost > 0 {
			cost = fmtCost(r.Cost)
		}
		dur := "—"
		if r.ActiveMS > 0 {
			dur = obFmtDur(float64(r.ActiveMS))
		}
		fmt.Fprintf(w, "%-19s %-11s %-12s %8s %8s %8s %9s  %s\n", r.SessionID, obOutcomePT(r.Outcome), oneLine(r.Status, 12), obFmtTok(r.In), obFmtTok(r.Out), cost, dur, oneLine(r.Title, 50))
	}
	return 0
}

// obPartialText is the content kept for a reply cut while streaming: its
// text plus any tool call that was still being written, as text (its
// arguments may be cut, so it never becomes a real call), so a resume sees
// the work instead of losing it.
func (a *Agent) obPartialText(resp *ChatResponse) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(resp.Text))
	for _, tc := range resp.ToolCalls {
		args := tc.Args
		if len(args) > 32<<10 {
			args = truncateHeadTail(args, 32<<10)
		}
		fmt.Fprintf(&b, "\n\n[tool call %s cut while being written, not executed; partial arguments:]\n%s", tc.Name, args)
	}
	if a.obSoftStopped() {
		b.WriteString("\n\n[cut by the run deadline]")
	} else {
		b.WriteString("\n\n[interrupted by the user]")
	}
	return strings.TrimSpace(b.String())
}
