package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Automatic spec review (guard "review", JAZZ_REVIEW_GUARD=off disables it).
//
// A request written as a list of requirements is where weak models stop at
// "the tests I wrote pass" with one bullet quietly wrong (eval v2 task 02 on
// the d610: invalid priority not a usage error, ordering incomplete). Once
// per turn, when such a request changed code, a read-only subagent with a
// fresh context checks the change requirement by requirement — no git
// needed: the pre-images of edited files are put next to it for diffing.

// arMinReqs: requirement lines (bullets / numbered items) that make a
// request "spec-heavy".
const arMinReqs = 3

var arReqLineRE = lazyRx(`(?m)^\s*(?:[-*•]|\d{1,2}[.)])\s+\S`)

// arSpecHeavy: the request lists several requirements.
func arSpecHeavy(req string) bool {
	return len(arReqLineRE.FindAllStringIndex(req, -1)) >= arMinReqs
}

const arReviewPrompt = `Check the change another agent just made against the user's request; you have a fresh context and did not write it.

The user's request was:
<request>
%s
</request>

%s
Go through the request requirement by requirement. For each one, find the code that implements it and check it the way a strict test would: invalid input and its exact error path, defaults, ordering and tie-breaks, filters combined, output format character by character, old data still loading. Report only concrete defects: a requirement not done or done differently from what the request says, a bug, code that does not run. No style comments, no optional improvements. Do not change any file.

If every requirement holds, reply with exactly: LGTM
Otherwise reply with a short numbered list; each item quotes the requirement, names the file and line, and says what is wrong.`

// arAutoReview runs the spec review once. true = defects went back to the
// model (the turn continues).
func (a *Agent) arAutoReview(ctx context.Context, lt *acTurnLoop) bool {
	if a.sub || lt == nil || lt.reviewed || a.hg == nil || !a.hg.codeEdited || ctx.Err() != nil {
		return false
	}
	if !a.hgOn("review") || a.obDeadlineWarned() {
		return false
	}
	if !a.RunDeadline.IsZero() && time.Until(a.RunDeadline) < 4*time.Minute {
		return false
	}
	req := strings.TrimSpace(acStripReminders(lt.input))
	if !arSpecHeavy(req) {
		return false
	}
	bt := a.bgTurnState()
	if bt == nil {
		return false
	}
	files := a.qgChanges(bt)
	if len(files) == 0 {
		return false
	}
	lt.reviewed = true

	// pre-images inside the project (the sandbox hides a private /tmp)
	preDir, _ := os.MkdirTemp(filepath.Join(a.tools.Root, ".jazz"), "review-pre-")
	if preDir == "" {
		_ = os.MkdirAll(filepath.Join(a.tools.Root, ".jazz"), 0o755)
		preDir, _ = os.MkdirTemp(filepath.Join(a.tools.Root, ".jazz"), "review-pre-")
	}
	if preDir != "" {
		defer os.RemoveAll(preDir)
	}
	var changed, added []string
	for _, f := range files {
		rel := filepath.ToSlash(f.Rel)
		if !f.Existed {
			added = append(added, rel)
			continue
		}
		if preDir != "" {
			dst := filepath.Join(preDir, filepath.FromSlash(rel))
			if os.MkdirAll(filepath.Dir(dst), 0o755) == nil && os.WriteFile(dst, []byte(f.Old), 0o644) == nil {
				pre, _ := filepath.Rel(a.tools.Root, dst)
				changed = append(changed, fmt.Sprintf("%s (before: %s — diff -u %s %s)", rel, filepath.ToSlash(pre), filepath.ToSlash(pre), rel))
				continue
			}
		}
		changed = append(changed, rel)
	}
	var what strings.Builder
	if len(changed) > 0 {
		fmt.Fprintf(&what, "Changed files:\n- %s\n", strings.Join(acClipList(changed, 30), "\n- "))
	}
	if len(added) > 0 {
		fmt.Fprintf(&what, "New files (read them): %s\n", strings.Join(acClipList(added, 30), ", "))
	}
	prompt := fmt.Sprintf(arReviewPrompt, acClip(req, 3000), what.String())
	args, _ := json.Marshal(map[string]string{"description": "conferir o pedido item por item", "prompt": prompt})
	tc := ToolCall{ID: "autoreview", Name: "task", Args: string(args)}
	a.ui.ToolStart(tc.ID, "task", "revisão automática: pedido item por item")
	t0 := time.Now()
	out := a.runTask(ctx, tc)
	a.ui.ToolEnd(tc.ID, "task", out, time.Since(t0))
	if ctx.Err() != nil {
		return false
	}
	report := acReviewReport(out.Text)
	dbg("autoreview", map[string]any{"files": len(files), "lgtm": acReviewLGTM(report), "err": out.IsErr})
	switch {
	case out.IsErr || report == "":
		return false // a failed review never blocks the answer
	case acReviewLGTM(report):
		a.ui.Notice("revisão automática: todos os itens do pedido conferem")
		return false
	}
	if preDir != "" {
		report = strings.ReplaceAll(report, filepath.ToSlash(mustRel(a.tools.Root, preDir))+"/", "(antes) ")
	}
	a.ui.Notice("revisão automática apontou itens do pedido — o modelo vai conferir")
	a.append(Message{Role: "user", Content: "<system-reminder>A reviewer with a fresh context checked your change against the request, requirement by requirement, and reported:\n" + acClip(report, 4000) + "\nFor each point: check it against the request text and the code. Fix the real ones, add or adjust a test that pins the behavior, re-run the tests, then give your final answer. If a point is wrong, say why in one line. This review runs only once.</system-reminder>"})
	return true
}

func mustRel(root, p string) string {
	r, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return r
}
