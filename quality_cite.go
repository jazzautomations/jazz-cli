package main

// quality_cite.go: the quality checklist's test item ("new logic but no test
// file was touched") invites the model to "say which existing test already
// covers it". A reply that does so — naming a test file or a test function
// that really exists in the project — settles that item on the second pass,
// instead of ending a correct bugfix as quality_failed (QA lane "skills").
// A name that exists nowhere settles nothing.

import (
	"os"
	"path/filepath"
	"strings"
)

// qgCiteMaxFiles bounds the test files read when looking up a cited name.
const qgCiteMaxFiles = 400

var (
	// a cited file: tests/test_calc.py, calc_test.go, src/a.spec.ts …
	qgCitedFileRE = lazyRx(`[\w./-]*[\w-]+\.(?:py|go|[cm]?[jt]sx?|rb|java|kt|cs|swift|rs)\b`)
	// a cited test function: test_media_vazia, TestSlugify
	qgCitedFuncRE = lazyRx(`\b(test_\w+|Test[A-Z_]\w*)\b`)
	// a test function definition (Python def, Go func)
	qgDefinedTestRE = lazyRx(`(?m)^\s*(?:async\s+)?(?:def|func)\s+(test_\w+|Test\w*)\s*\(`)
)

// qgRepliesSinceFeedback: the assistant text written after the checklist's
// feedback round (the model's answer to it).
func (a *Agent) qgRepliesSinceFeedback(bt *bgTurn) string {
	if bt.qg.fedAt <= 0 || bt.qg.fedAt > len(a.Msgs) {
		return ""
	}
	var b strings.Builder
	for _, m := range a.Msgs[bt.qg.fedAt:] {
		if m.Role == "assistant" && m.Content != "" {
			b.WriteString(m.Content)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// qgDropAnsweredTestGap removes the test item from issues when reply names a
// test file or test function that exists under root; otherwise issues come
// back unchanged.
func qgDropAnsweredTestGap(issues []qgIssue, reply, root string) []qgIssue {
	gap := false
	for _, is := range issues {
		gap = gap || is.TestGap
	}
	if !gap || strings.TrimSpace(reply) == "" || !qgCitesExistingTest(reply, root) {
		return issues
	}
	var left []qgIssue
	for _, is := range issues {
		if !is.TestGap {
			left = append(left, is)
		}
	}
	return left
}

// qgCitesExistingTest: text names a test file present under root, or a test
// function defined in one of the project's test files.
func qgCitesExistingTest(text, root string) bool {
	for _, m := range qgCitedFileRE.FindAllString(text, 32) {
		rel := filepath.FromSlash(strings.TrimPrefix(m, "./"))
		if !filepath.IsLocal(rel) || !qgIsTestFile(filepath.ToSlash(rel)) {
			continue
		}
		if fi, err := os.Lstat(filepath.Join(root, rel)); err == nil && fi.Mode().IsRegular() {
			return true
		}
	}
	names := map[string]bool{}
	for _, m := range qgCitedFuncRE.FindAllString(text, 32) {
		names[m] = true
	}
	if len(names) == 0 {
		return false
	}
	found, read := false, 0
	hgWalk(root, func(abs, rel string) {
		if found || read >= qgCiteMaxFiles || hgKind(rel) != "test" {
			return
		}
		read++
		b, err := os.ReadFile(abs)
		if err != nil || len(b) > 512<<10 {
			return
		}
		for _, m := range qgDefinedTestRE.FindAllStringSubmatch(string(b), -1) {
			if names[m[1]] {
				found = true
				return
			}
		}
	})
	return found
}
