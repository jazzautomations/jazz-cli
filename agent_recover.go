package main

// agent_recover.go: deterministic handoff and discard when a turn stalls
// (docs/research/shepherd-meta-agents.md A1, Shepherd's escalation ladder
// past "inject").
//
// Triggers (at most one handoff per turn):
//   - the repetition guard would stop the turn (same call 6×, StopLoop);
//   - --until failed 3 rounds in a row with the same failure signature;
//   - 8 tool errors in a row.
//
// The handoff builds a fixed-format note with no LLM call (request, /goal,
// files changed, last 15 tool summaries, last --until output, what failed),
// replaces the whole history with it (acHardReset), keeps the files on disk
// and lets the turn go on inside the remaining --max-steps. Discard: when the
// latest --until output counts more failures than the first round, the
// turn's file changes are reverted first. The system prompt is untouched, so
// the cost is one prompt-cache miss for the message prefix.
//
// Mode: --recover / JAZZ_RECOVER / config "recover": on (default: handoff,
// discard when failures grew) | handoff (never revert) | discard (always
// revert) | off.

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	rcTrailLen      = 15   // tool summaries in the handoff note
	rcLogCap        = 80   // tool events kept per turn (plan cache, hindsight)
	rcErrStreak     = 8    // consecutive tool errors that trigger a handoff
	rcSameUntil     = 3    // identical --until failures in a row that trigger one
	rcMinStepsLeft  = 3    // a handoff needs at least this many steps left
	rcSumRunes      = 150  // one tool summary (Shepherd's ~150-byte format)
	rcUntilOutBytes = 1500 // --until output kept in the note
	rcReqBytes      = 1500 // user request kept in the note
)

// Handoff reasons (recorded in the session JSONL and the debug log).
const (
	rcWhyLoop   = "loop"
	rcWhyUntil  = "until_repeat"
	rcWhyErrors = "tool_errors"
)

// rcEvent is one tool call of the turn, already reduced to what a handoff
// note or a plan template needs.
type rcEvent struct {
	tool   string
	target string // path or command (one line)
	exit   int    // bash exit code (-1 = none: reset or other tool)
	isErr  bool
	errTxt string // short error line
	mut    bool   // changed something (write/edit, bash that is not read-only)
	verify bool   // bash that looks like a build/test/lint run
}

// summary is the ~150-byte line Shepherd's supervisor reads: tool, target, outcome.
func (e rcEvent) summary() string {
	s := e.tool
	if e.target != "" {
		s += " " + e.target
	}
	switch {
	case e.tool == "bash" && e.exit >= 0:
		s += fmt.Sprintf(" → exit %d", e.exit)
	case e.isErr:
		s += " → error"
	default:
		s += " → ok"
	}
	if e.isErr && e.errTxt != "" {
		s += ": " + e.errTxt
	}
	return oneLine(s, rcSumRunes)
}

// rcTurn is the per-turn state of recovery and of the plan cache.
type rcTurn struct {
	log    []rcEvent
	errRun int // consecutive tool errors

	sig      string // failure signature of the last --until run
	sigRun   int    // how many --until runs in a row had sig
	hasFirst bool
	first    int // failure count of the first --until run

	untilCmd  string
	untilOut  string
	untilExit int

	fired    string // handoff reason ("" = none yet this turn)
	reverted bool   // the handoff reverted the turn's files
	reset    bool   // history was replaced; Run must clear its per-context state
	step     int    // current step of Run

	untilPassed bool   // --until exited 0 in this turn
	gateOK      bool   // another verifier (quality gate) passed: MarkVerified
	verifyOK    bool   // the last build/test bash exited 0 and nothing changed after it
	verifyCmd   string // that command

	plans []string // plan cache entries injected in this turn
}

// rcState returns the turn's recovery state, creating it on first use.
func (lt *acTurnLoop) rcState() *rcTurn {
	if lt.rc == nil {
		lt.rc = &rcTurn{}
	}
	return lt.rc
}

// MarkVerified lets another objective check (e.g. a quality gate) count the
// turn as verified for the plan cache, like a passing --until.
func (lt *acTurnLoop) MarkVerified() {
	if lt != nil {
		lt.rcState().gateOK = true
	}
}

// LastRecovery is the reason of the last handoff in this session ("" = none).
func (a *Agent) LastRecovery() string { return a.loop.lastRecover }

// rcMode is the effective recovery mode: flag > JAZZ_RECOVER > config > on.
func (a *Agent) rcMode() string {
	m := a.loop.opts.Recover
	if m == "" {
		m = os.Getenv("JAZZ_RECOVER")
	}
	if m == "" && a.cfg != nil {
		m = a.cfg.Recover
	}
	return rcNormMode(m)
}

// rcNormMode maps user spellings to on | off | handoff | discard ("" = on).
func rcNormMode(m string) string {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case "off", "0", "false", "no", "none":
		return "off"
	case "handoff":
		return "handoff"
	case "discard":
		return "discard"
	}
	return "on"
}

// rcValidMode reports whether s is an accepted --recover value.
func rcValidMode(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "off", "handoff", "discard", "true", "false", "1", "0", "yes", "no", "none":
		return true
	}
	return false
}

func (a *Agent) rcEnabled() bool {
	return !a.sub && a.mode() != "plan" && a.rcMode() != "off"
}

var rcExitRE = regexp.MustCompile(`\[exit code (-?\d+)\]`)

// rcExitCode reads the last "[exit code N]" marker of a bash result (-1 = none).
func rcExitCode(text string) int {
	m := rcExitRE.FindAllStringSubmatch(text, -1)
	if len(m) == 0 {
		return -1
	}
	n, err := strconv.Atoi(m[len(m)-1][1])
	if err != nil {
		return -1
	}
	return n
}

// rcFailRE counts failure markers in test/build output (Shepherd discard rule).
var rcFailRE = regexp.MustCompile(`--- FAIL|\bFAIL(ED)?\b|\bfailed\b|\bError\b|\bERROR\b|\berror:`)

func rcCountFails(out string) int { return len(rcFailRE.FindAllStringIndex(out, -1)) }

var rcDigitsRE = regexp.MustCompile(`[0-9]+(\.[0-9]+)?`)

// rcUntilSig is the failure signature of a --until run: its failure lines
// (or its tail) with numbers masked, so timings and addresses do not count.
func rcUntilSig(out string, exit int) string {
	var keep []string
	lines := nonEmptyLines(out)
	for _, l := range lines {
		if rcFailRE.MatchString(l) {
			keep = append(keep, strings.TrimSpace(l))
		}
	}
	if len(keep) == 0 {
		keep = lines[max(0, len(lines)-10):]
	}
	return fmt.Sprintf("%d\x00%s", exit, rcDigitsRE.ReplaceAllString(strings.Join(keep, "\n"), "#"))
}

// rcErrLine picks the most telling line of a failed tool result.
func rcErrLine(text string) string {
	lines := nonEmptyLines(text)
	for _, l := range lines {
		if rcFailRE.MatchString(l) && !strings.HasPrefix(strings.TrimSpace(l), "[exit code") {
			return oneLine(l, 100)
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		t := strings.TrimSpace(rcExitRE.ReplaceAllString(lines[i], ""))
		if t != "" && !strings.HasPrefix(t, "[cwd:") {
			return oneLine(t, 100)
		}
	}
	return ""
}

// rcVerifyRE: bash commands that check work (build, test, lint, type-check).
var rcVerifyRE = regexp.MustCompile(`\b(go (test|build|vet)|cargo (test|build|check|clippy)|pytest|py\.test|python[0-9.]* -m (pytest|unittest)|unittest|npm (test|run (test|build|lint|check|typecheck))|pnpm (test|run|build|lint)|yarn (test|build|lint)|bun test|tsc\b|eslint|ruff|mypy|make( |$)|ctest|mvn (test|verify|package)|gradle(w)? (test|build|check)|rspec|phpunit|dotnet (test|build)|zig build|mix test|deno test|jest|vitest)`)

func rcIsVerifyCmd(cmd string) bool { return rcVerifyRE.MatchString(cmd) }

func rcArg(tc ToolCall, key string) string {
	m, _, err := parseArgs(tc.Args)
	if err != nil {
		return ""
	}
	m = normalizeArgs(CanonicalTool(tc.Name), m)
	s, _ := argStr(m, key)
	return s
}

// rcObserve records one tool result of the turn (called by Run after each call).
func (a *Agent) rcObserve(lt *acTurnLoop, tc ToolCall, out ToolOutput, denied bool) {
	if a.sub || lt == nil {
		return
	}
	rc := lt.rcState()
	e := rcEvent{tool: tc.Name, target: oneLine(toolDetail(tc.Name, tc.Args), 100), exit: -1, isErr: out.IsErr}
	neutral := denied
	switch tc.Name {
	case "bash":
		cmd := rcArg(tc, "command")
		e.exit = rcExitCode(out.Text)
		e.mut = cmd != "" && !IsReadOnly(cmd)
		if cmd != "" && rcIsVerifyCmd(cmd) && e.exit >= 0 {
			e.verify = true
			rc.verifyOK, rc.verifyCmd = e.exit == 0, cmd
		}
		// grep/rg with no match: exit 1 and no output is not a tool error
		if e.exit == 1 && strings.TrimSpace(rcExitRE.ReplaceAllString(out.Text, "")) == "" {
			neutral = true
		}
	case "write", "edit":
		e.mut = !out.IsErr
		if e.mut {
			rc.verifyOK = false // the change after the check is not verified
		}
	}
	if out.IsErr {
		e.errTxt = rcErrLine(out.Text)
	}
	rc.log = append(rc.log, e)
	if len(rc.log) > rcLogCap {
		rc.log = rc.log[len(rc.log)-rcLogCap:]
	}
	switch {
	case neutral:
	case out.IsErr:
		rc.errRun++
	default:
		rc.errRun = 0
	}
}

// rcTakeReset is called at the top of every step: it tells Run whether a
// handoff replaced the history, so its one-shot nudges start over.
func (a *Agent) rcTakeReset(lt *acTurnLoop, step int) bool {
	if lt == nil {
		return false
	}
	rc := lt.rcState()
	rc.step = step
	if rc.reset {
		rc.reset = false
		return true
	}
	return false
}

// rcOnLoop: the repetition guard is about to stop the turn. detail is the
// repeated call. true = the history was handed off, continue the turn.
func (a *Agent) rcOnLoop(ctx context.Context, lt *acTurnLoop, detail string) bool {
	return a.rcFire(ctx, lt, rcWhyLoop, detail)
}

// rcAfterBatch fires a handoff after rcErrStreak tool errors in a row.
func (a *Agent) rcAfterBatch(ctx context.Context, lt *acTurnLoop) bool {
	if lt == nil || lt.rc == nil || lt.rc.errRun < rcErrStreak {
		return false
	}
	last := ""
	if n := len(lt.rc.log); n > 0 {
		last = lt.rc.log[n-1].tool + " " + lt.rc.log[n-1].target
	}
	return a.rcFire(ctx, lt, rcWhyErrors, last)
}

// rcUntilPassed records a passing --until run.
func (a *Agent) rcUntilPassed(lt *acTurnLoop) {
	if lt != nil {
		lt.rcState().untilPassed = true
	}
}

// rcAfterUntil records a failing --until run and fires a handoff when it
// failed rcSameUntil times in a row the same way and rounds are left.
func (a *Agent) rcAfterUntil(ctx context.Context, lt *acTurnLoop, cmd, out string, exit, rounds int) bool {
	if lt == nil {
		return false
	}
	rc := lt.rcState()
	rc.untilCmd, rc.untilOut, rc.untilExit = cmd, out, exit
	rc.untilPassed = false
	if !rc.hasFirst {
		rc.hasFirst, rc.first = true, rcCountFails(out)
	}
	if sig := rcUntilSig(out, exit); sig == rc.sig {
		rc.sigRun++
	} else {
		rc.sig, rc.sigRun = sig, 1
	}
	if rc.sigRun < rcSameUntil || lt.untilRuns >= rounds {
		return false
	}
	detail := ""
	for i := len(rc.log) - 1; i >= 0; i-- {
		if rc.log[i].mut {
			detail = rc.log[i].tool + " " + rc.log[i].target
			break
		}
	}
	return a.rcFire(ctx, lt, rcWhyUntil, detail)
}

// rcFire performs the handoff (and the discard when it applies).
func (a *Agent) rcFire(ctx context.Context, lt *acTurnLoop, why, detail string) bool {
	if lt == nil || !a.rcEnabled() || ctx.Err() != nil {
		return false
	}
	rc := lt.rcState()
	if rc.fired != "" {
		return false // one handoff per turn
	}
	if rc.step > 0 && a.stepLimit()-rc.step < rcMinStepsLeft {
		return false
	}
	mode := a.rcMode()
	grew := rc.hasFirst && rc.untilOut != "" && rcCountFails(rc.untilOut) > rc.first
	discard := mode == "discard" || (mode == "on" && why == rcWhyUntil && grew)
	files := a.tools.rcTurnFiles()
	var reverted []string
	if discard {
		var err error
		reverted, err = a.tools.rcRevertTurn()
		if err != nil {
			a.ui.Warn("não deu pra desfazer tudo antes de recomeçar: " + err.Error())
		}
	}
	rc.fired, rc.reverted = why, discard
	note := a.acHandoffNote(lt, why, detail, files, discard)
	a.acHardReset(note, why)
	rc.reset, rc.errRun, rc.sig, rc.sigRun = true, 0, "", 0
	if discard {
		rc.hasFirst, rc.verifyOK = false, false
	}
	a.loop.lastRecover = why
	dbg("recover", map[string]any{"why": why, "mode": mode, "discard": discard, "reverted": len(reverted), "files": len(files), "step": rc.step, "note_bytes": len(note)})
	msg := map[string]string{
		rcWhyLoop:   "o modelo repetiu a mesma chamada",
		rcWhyUntil:  fmt.Sprintf("o --until falhou %d vezes do mesmo jeito", rcSameUntil),
		rcWhyErrors: fmt.Sprintf("%d ferramentas seguidas deram erro", rcErrStreak),
	}[why]
	if discard {
		a.ui.Warn(msg + " — desfiz as mudanças do turno e recomecei com contexto limpo (nota de passagem)")
	} else {
		a.ui.Warn(msg + " — recomecei com contexto limpo (nota de passagem), arquivos mantidos")
	}
	return true
}

// acHandoffNote builds the deterministic handoff note (no LLM call, ≲1.5K tokens).
func (a *Agent) acHandoffNote(lt *acTurnLoop, why, detail string, files []string, reverted bool) string {
	rc := lt.rcState()
	var b strings.Builder
	whyText := map[string]string{
		rcWhyLoop:   "it kept repeating the same tool call with the same result",
		rcWhyUntil:  fmt.Sprintf("the completion check failed %d times in a row with the same failures", rcSameUntil),
		rcWhyErrors: fmt.Sprintf("its last %d tool calls all failed", rcErrStreak),
	}[why]
	b.WriteString("<system-reminder>Handoff note from the harness. A previous attempt at this task got stuck (" + whyText + "), so its conversation was cleared to give you a clean context. ")
	if reverted {
		b.WriteString("Previous attempt reverted: the files it changed were restored to how they were before it started. ")
	} else {
		b.WriteString("The files on disk keep its changes. ")
	}
	b.WriteString("You do not remember any file contents: read files again before editing them. Do not repeat the approach that failed.</system-reminder>\n\n")

	req := strings.TrimSpace(acStripReminders(lt.input))
	if req == "" {
		req = "(not available)"
	}
	b.WriteString("## Task (the user's request)\n" + acClip(req, rcReqBytes) + "\n")
	if g := a.loop.goal; g != "" && !a.sub {
		b.WriteString("\n## Standing goal (/goal)\n" + acClip(g, 400) + "\n")
	}

	b.WriteString("\n## Files changed by the previous attempt")
	if reverted {
		b.WriteString(" (now reverted)")
	}
	b.WriteString("\n")
	if len(files) == 0 {
		b.WriteString("(none)\n")
	}
	for _, f := range acClipList(files, 30) {
		b.WriteString("- " + f + "\n")
	}

	b.WriteString("\n## Last tool calls of the previous attempt (oldest first)\n")
	trail := rc.log[max(0, len(rc.log)-rcTrailLen):]
	if len(trail) == 0 {
		b.WriteString("(none)\n")
	}
	for i, e := range trail {
		fmt.Fprintf(&b, "%d. %s\n", i+1, e.summary())
	}

	if rc.untilOut != "" {
		fmt.Fprintf(&b, "\n## Last output of the completion check (must exit 0)\n$ %s  (exit %d)\n%s\n", rc.untilCmd, rc.untilExit, truncateHeadTail(rc.untilOut, rcUntilOutBytes))
	}
	if open := a.openTodos(); open != "" {
		b.WriteString("\n## Open todo items\n" + acClip(open, 800))
	}

	b.WriteString("\n## What failed\n")
	d := oneLine(detail, 160)
	switch why {
	case rcWhyLoop:
		b.WriteString("Approach failed: the same call kept returning the same result.")
		if d != "" {
			b.WriteString(" Do not repeat `" + d + "`.")
		}
	case rcWhyUntil:
		b.WriteString("Approach failed: the changes so far did not fix the failures above.")
		if d != "" && !reverted {
			b.WriteString(" The last change was `" + d + "`; reconsider it instead of tweaking it again.")
		}
	case rcWhyErrors:
		b.WriteString("Approach failed: every recent call errored. Check paths, arguments and the working directory before retrying.")
		if d != "" {
			b.WriteString(" Do not repeat `" + d + "`.")
		}
	}
	b.WriteString("\nThink about a different approach, continue the task from here and verify the result.")
	return b.String()
}

// acHardReset replaces the whole history (after the frozen system prompt)
// with one handoff note. Files stay as they are; loop hashes are cleared and
// the session file gets the note as a compaction, so -c resumes from it.
func (a *Agent) acHardReset(note, why string) {
	old := len(a.Msgs)
	a.Msgs = []Message{{Role: "user", Content: note}}
	if !a.sub {
		a.tools.RebaseTurns(old+1, old) // every checkpoint now points at the note
	}
	a.sess.write(sessEntry{T: "recover", Summary: why})
	a.sess.Compact("handoff ("+why+")", a.Msgs)
	a.recent = nil
	a.lastPrompt, a.countedIdx = 0, 0
}

// rcTurnFiles lists (relative, sorted) the files the current turn changed:
// write/edit snapshots plus, with checkpoints, paths changed through bash.
func (t *Toolbox) rcTurnFiles() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.turns) == 0 {
		return nil
	}
	cur := t.turns[len(t.turns)-1]
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		r := t.rel(p)
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	for p := range cur.files {
		add(p)
	}
	for p := range cur.bash {
		add(p)
	}
	sort.Strings(out)
	return out
}

// rcRevertTurn restores what the current turn changed (like /undo, without
// dropping the turn) and starts tracking it afresh from the restored state.
func (t *Toolbox) rcRevertTurn() ([]string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(t.turns)
	if n == 0 {
		return nil, nil
	}
	cur := t.turns[n-1]
	paths, err := t.restore(cur)
	t.turns[n-1] = turnSnap{msgIndex: cur.msgIndex, files: map[string]*snapshot{}, cpStart: cur.cpStart, cpLast: cur.cpStart}
	return paths, err
}
