package main

// agent_loop.go: objective "done" checks for a turn (gaps #5 lite and #6 of
// docs/research/bob-kiro-devin.md).
//
//   - --until "<cmd>" / /until: when the model would stop, run cmd in the
//     persistent shell; a non-zero exit sends its output back and the turn
//     goes on (bounded by --max-steps and LoopOpts.UntilRounds).
//   - /goal <objective>: a standing objective stored in the session JSONL and
//     re-injected as a short reminder at turn start and after compaction.
//   - --review / /review: after a turn that changed files, a read-only task
//     subagent with a fresh context reviews `git diff` once; concrete defects
//     go back to the model.
//
// Everything here only appends messages, so the prompt cache stays valid.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// acUntilDefaultRounds is how many times --until may run in one turn.
const acUntilDefaultRounds = 5

// acUntilTimeout caps one run of the --until command.
const acUntilTimeout = 10 * time.Minute

// acUntilMaxOut is how much of the until output goes back to the model.
const acUntilMaxOut = 6000

// LoopOpts are the per-agent completion checks set by flags or slash commands.
// They survive /clear (main copies them to the new Agent); the goal does not,
// because it belongs to the session.
type LoopOpts struct {
	Until       string // shell command that must exit 0 for a turn to stop ("" = off)
	UntilRounds int    // max runs of Until per turn (<= 0: acUntilDefaultRounds)
	Review      bool   // review the turn's diff once with a read-only subagent
	Budget      string // --budget lean|normal|off ("" = config, then lean; budget.go)
	Quality     string // --quality off|standard|strict ("" = config, then standard; quality.go)
	Guards      string // --guards on|off ("" = JAZZ_GUARDS, config, on; guard_honest.go)
	Recover     string // --recover: on | off | handoff | discard ("" = JAZZ_RECOVER, config, on) (agent_recover.go)
}

// acLoop is the Agent's completion-check state.
type acLoop struct {
	opts        LoopOpts
	goal        string
	lastRecover string // reason of the last handoff (agent_recover.go)
}

// acTurnLoop is the per-turn state of the checks.
type acTurnLoop struct {
	untilRuns int
	reviewed  bool
	input     string          // the user's request, for the reviewer
	gitBase   string          // tree-ish of the worktree before the turn ("" = no review)
	untracked map[string]bool // untracked files before the turn
	rc        *rcTurn         // recovery and plan cache state (agent_recover.go)
}

// LoopOpts returns the current completion checks.
func (a *Agent) LoopOpts() LoopOpts { return a.loop.opts }

// SetLoopOpts replaces the completion checks.
func (a *Agent) SetLoopOpts(o LoopOpts) {
	o.Until = strings.TrimSpace(o.Until)
	a.loop.opts = o
}

// Goal is the standing objective of this session ("" = none).
func (a *Agent) Goal() string { return a.loop.goal }

// SetGoal sets (or with "" clears) the standing objective and records it in
// the session file, so -c / /resume bring it back.
func (a *Agent) SetGoal(obj string) {
	obj = strings.TrimSpace(obj)
	a.loop.goal = obj
	a.EnsureSystem()
	acWriteGoal(a.sess, obj)
}

func acWriteGoal(s *Session, goal string) {
	s.write(sessEntry{T: "goal", Summary: goal})
}

// acLoadGoal returns the last goal recorded in a session file ("" = none or cleared).
func acLoadGoal(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	goal := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		b := sc.Bytes()
		if !strings.Contains(string(b[:min(len(b), 32)]), `"goal"`) {
			continue // cheap filter: msg lines can be huge
		}
		var se sessEntry
		if json.Unmarshal(b, &se) == nil && se.T == "goal" {
			goal = se.Summary
		}
	}
	return goal
}

// acGoalReminder is the short reminder that carries the goal ("" = no goal).
func (a *Agent) acGoalReminder() string {
	if a.sub || a.loop.goal == "" {
		return ""
	}
	return "<system-reminder>Standing goal for this session (set by the user with /goal): " + a.loop.goal +
		"\nEvery turn should move toward it. When it is fully achieved and verified, say so explicitly.</system-reminder>"
}

// acTurnInput prefixes the goal reminder to a turn's input, unless the input
// already is the goal (the first turn after /goal).
func (a *Agent) acTurnInput(input string) string {
	r := a.acGoalReminder()
	if r == "" || strings.Contains(input, a.loop.goal) {
		return input
	}
	return r + "\n" + input
}

// acBeginTurnLoop prepares the per-turn state; for --review it snapshots the
// worktree so the review sees only this turn's changes.
func (a *Agent) acBeginTurnLoop(ctx context.Context, input string) *acTurnLoop {
	lt := &acTurnLoop{input: input}
	if a.sub || !a.loop.opts.Review || a.mode() == "plan" {
		return lt
	}
	base, untracked, err := acGitSnapshot(ctx, a.tools.Root)
	if err != nil {
		dbg("review", map[string]any{"snapshot_error": err.Error()})
		return lt
	}
	lt.gitBase, lt.untracked = base, untracked
	return lt
}

// acStopGate runs when the model gave a final answer. again=true means a
// reminder was appended and the turn must continue; otherwise why is the stop
// reason to return ("" = keep the caller's).
func (a *Agent) acStopGate(ctx context.Context, lt *acTurnLoop) (again bool, why StopReason) {
	if a.sub || lt == nil || a.mode() == "plan" {
		return false, ""
	}
	// old-coder checklist first (quality.go): one feedback round, then a
	// stronger reason (--until) wins over quality_failed.
	if hAgain, hWhy := a.hgDoneGate(ctx); hAgain { // honest-completion guards (guard_honest.go)
		return true, ""
	} else if hWhy != "" {
		return false, hWhy
	}
	qAgain, qFailed := a.acQualityGate(ctx)
	if qAgain {
		return true, ""
	}
	if cmd := a.loop.opts.Until; cmd != "" {
		again, why = a.acUntilCheck(ctx, lt, cmd)
		if again || why != "" {
			return again, why
		}
	}
	if a.ojVerifyGate(ctx) { // --verifier: a second model reviews the diff (judge_hedge.go)
		return true, ""
	}
	if a.loop.opts.Review && !lt.reviewed && lt.gitBase != "" && ctx.Err() == nil {
		lt.reviewed = true
		if a.acReview(ctx, lt) {
			return true, ""
		}
	}
	if qFailed {
		return false, StopQualityFailed
	}
	return false, ""
}

// acUntilCheck runs the --until command in the persistent shell (in a
// subshell at the project root, so the model's cwd and env are untouched).
func (a *Agent) acUntilCheck(ctx context.Context, lt *acTurnLoop, cmd string) (bool, StopReason) {
	rounds := a.loop.opts.UntilRounds
	if rounds <= 0 {
		rounds = acUntilDefaultRounds
	}
	lt.untilRuns++
	id := fmt.Sprintf("until-%d", lt.untilRuns)
	a.ui.ToolStart(id, "until", cmd)
	a.ui.Thinking("verificando --until")
	line := a.tools.Shell.Dialect().subshellAt(a.tools.Root, cmd)
	r := a.tools.Shell.Run(ctx, line, acUntilTimeout, nil)
	if ctx.Err() != nil {
		a.ui.ToolEnd(id, "until", ToolOutput{Text: "interrompido", IsErr: true, Display: Display{Summary: "interrompido"}}, r.Duration)
		return true, "" // the loop top records the interruption
	}
	out := strings.TrimSpace(redactSecrets(r.Output))
	lines := nonEmptyLines(out)
	prev := lines
	if len(prev) > 4 {
		prev = prev[len(prev)-4:]
	}
	ok := r.ExitCode == 0 && !r.Reset
	sum := fmt.Sprintf("exit %d · rodada %d/%d", r.ExitCode, lt.untilRuns, rounds)
	if r.Reset {
		sum = firstSentence(strings.Trim(r.Note, "[]"))
	}
	a.ui.ToolEnd(id, "until", ToolOutput{Text: out, IsErr: !ok, Display: Display{Summary: sum, Preview: prev, Lines: len(lines)}}, r.Duration)
	dbg("until", map[string]any{"cmd": cmd, "exit": r.ExitCode, "round": lt.untilRuns, "reset": r.Reset})
	bgScoreRecord(a.TurnModel(), "until", ok) // measured quality for the fallback order (scores.go)
	if ok {
		a.rcUntilPassed(lt)
		a.ui.Notice("--until passou: " + oneLine(cmd, 120))
		return false, ""
	}
	body := truncateHeadTail(out, acUntilMaxOut)
	if r.Note != "" {
		body += "\n" + r.Note
	}
	if a.rcAfterUntil(ctx, lt, cmd, body, r.ExitCode, rounds) {
		return true, "" // handed off to a clean context (agent_recover.go)
	}
	if lt.untilRuns >= rounds {
		a.ui.Warn(fmt.Sprintf("--until ainda falha depois de %d %s: %s", rounds, uiPl(rounds, "rodada", "rodadas"), oneLine(cmd, 120)))
		// the next turn (interactive) should know the check is still red
		a.Note(fmt.Sprintf("<system-reminder>When the previous turn ended, the completion check `%s` still failed (exit %d).</system-reminder>", cmd, r.ExitCode))
		return false, StopUntilFailed
	}
	a.append(Message{Role: "user", Content: fmt.Sprintf("<system-reminder>You stopped, but the task is not done yet: the user defined done as this command exiting 0, and it failed (exit %d, check %d of %d).\n$ %s\n%s\nKeep working until it passes. Fix the cause; do not edit or weaken the check or its tests just to make it pass.</system-reminder>", r.ExitCode, lt.untilRuns, rounds, cmd, body)})
	return true, ""
}

// acGitSnapshot records the worktree state without touching refs, the index
// contents or files: `git stash create` writes a commit object for tracked
// changes (or prints nothing when clean, then HEAD is the base).
func acGitSnapshot(ctx context.Context, root string) (string, map[string]bool, error) {
	if _, err := acGit(ctx, root, "rev-parse", "--is-inside-work-tree"); err != nil {
		return "", nil, err
	}
	base := "HEAD"
	if _, err := acGit(ctx, root, "rev-parse", "--verify", "-q", "HEAD"); err != nil {
		base = acEmptyTree // repository without commits yet
	} else if s, err := acGit(ctx, root, "stash", "create"); err == nil && strings.TrimSpace(s) != "" {
		base = strings.TrimSpace(s)
	}
	return base, acUntracked(ctx, root), nil
}

// acEmptyTree is git's well-known empty tree object.
const acEmptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

func acUntracked(ctx context.Context, root string) map[string]bool {
	out := map[string]bool{}
	s, err := acGit(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return out
	}
	for _, p := range strings.Split(s, "\x00") {
		if p != "" {
			out[p] = true
		}
	}
	return out
}

// acTurnChanges lists tracked files changed since base and files that became
// untracked-new during the turn.
func acTurnChanges(ctx context.Context, root string, lt *acTurnLoop) (changed, added []string) {
	if s, err := acGit(ctx, root, "diff", "--name-only", lt.gitBase); err == nil {
		changed = nonEmptyLines(s)
	}
	for p := range acUntracked(ctx, root) {
		if !lt.untracked[p] {
			added = append(added, p)
		}
	}
	sort.Strings(added)
	return changed, added
}

func acGit(ctx context.Context, root string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.quotepath=off"}, args...)...)
	cmd.Dir = root
	cmd.Env = append(scrubEnv(os.Environ()), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	b, err := cmd.Output()
	return string(b), err
}

const acReviewPrompt = `Review the changes another agent just made in this repository; you have a fresh context and did not write them.

The user's request was:
<request>
%s
</request>

%s
Look only for concrete defects introduced by these changes: bugs, wrong logic, code that does not build, a part of the request that was not done, leftover debug code, unrelated changes. Do not comment on style and do not suggest optional improvements. Do not run commands that change anything.

If you find no concrete defect, reply with exactly: LGTM
Otherwise reply with a short numbered list; each item names the file and line, says what is wrong and why.`

// acReview runs the read-only review subagent once over the turn's diff. It
// reports whether defects were fed back (the turn continues).
func (a *Agent) acReview(ctx context.Context, lt *acTurnLoop) bool {
	changed, added := acTurnChanges(ctx, a.tools.Root, lt)
	if len(changed)+len(added) == 0 {
		return false
	}
	var what strings.Builder
	if len(changed) > 0 {
		fmt.Fprintf(&what, "Changed tracked files: %s\nSee the changes with: git diff %s\n(%s is a snapshot of the worktree before the changes.)\n", strings.Join(acClipList(changed, 30), ", "), lt.gitBase, lt.gitBase)
	}
	if len(added) > 0 {
		fmt.Fprintf(&what, "New files (read them): %s\n", strings.Join(acClipList(added, 30), ", "))
	}
	req := strings.TrimSpace(acStripReminders(lt.input))
	if req == "" {
		req = "(not available)"
	}
	prompt := fmt.Sprintf(acReviewPrompt, acClip(req, 3000), what.String())
	args, _ := json.Marshal(map[string]string{"description": "revisar o diff", "prompt": prompt})
	tc := ToolCall{ID: "review", Name: "task", Args: string(args)}
	a.ui.ToolStart(tc.ID, "task", "revisão do diff")
	t0 := time.Now()
	out := a.runTask(ctx, tc)
	a.ui.ToolEnd(tc.ID, "task", out, time.Since(t0))
	if ctx.Err() != nil {
		return true // the loop top records the interruption
	}
	report := acReviewReport(out.Text)
	switch {
	case out.IsErr || report == "":
		a.ui.Warn("revisão falhou: " + oneLine(firstSentence(out.Text), 160))
		return false
	case acReviewLGTM(report):
		a.ui.Notice("revisão: LGTM")
		return false
	}
	a.ui.Notice("revisão apontou problemas — o modelo vai corrigir")
	a.append(Message{Role: "user", Content: "<system-reminder>A reviewer with a fresh context checked your changes (git diff) and reported these possible defects:\n" + report + "\nFix the ones that are real and verify the fix. If a point is wrong, say why in one line. Then give your final answer. This review runs only once.</system-reminder>"})
	return true
}

func acClipList(l []string, n int) []string {
	if len(l) <= n {
		return l
	}
	return append(append([]string(nil), l[:n]...), fmt.Sprintf("… (+%d)", len(l)-n))
}

// acReviewReport drops runTask's footers from the subagent output.
func acReviewReport(s string) string {
	var keep []string
	for _, l := range strings.Split(s, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[subagent used ") || strings.HasPrefix(t, "[subagent stopped early") {
			continue
		}
		keep = append(keep, l)
	}
	return strings.TrimSpace(strings.Join(keep, "\n"))
}

// acReviewLGTM: the reviewer found nothing ("LGTM", maybe with markdown or a
// short remark, but no numbered or bulleted list of defects).
func acReviewLGTM(r string) bool {
	t := strings.TrimSpace(strings.Trim(strings.TrimSpace(r), "*_`\"'#> "))
	if !strings.HasPrefix(strings.ToUpper(t), "LGTM") {
		return false
	}
	for _, l := range strings.Split(t, "\n")[1:] {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "- ") || strings.HasPrefix(l, "* ") {
			return false
		}
		if i := strings.IndexAny(l, ".)"); i > 0 && i <= 3 && strings.Trim(l[:i], "0123456789") == "" {
			return false
		}
	}
	return true
}
