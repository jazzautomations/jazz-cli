package main

import (
	"regexp"
	"strconv"
	"strings"
)

// mechrepair.go — mechanical build/lint repair for Go.
// Deterministic operators named by the compiler error; the offline
// mode applies them in a loop (apply -> rebuild -> recheck) with no
// LLM. Kinds with no mechanical recipe escalate to the model.

// mrGoError is one file:line:col diagnostic from compiler output.
// Alias for the literal anonymous struct in the spec signature.
type mrGoError = struct {
	File string
	Line int
	Col  int
	Msg  string
}

// mrGoDiag matches "file.go:LINE:COL: msg" from `go build`/`go vet`
// (optional "vet: " prefix), same shape as outparse's goDiag.
var mrGoDiag = regexp.MustCompile(`^(?:vet: )?(\S+\.go):(\d+):(\d+): (.*)$`)

// mrParseGoErrors extracts file:line:col errors from go build/vet text.
// Continuation lines ("have ...", "want ...") and headers are skipped.
func mrParseGoErrors(out string) []mrGoError {
	var errs []mrGoError
	for _, l := range strings.Split(out, "\n") {
		m := mrGoDiag.FindStringSubmatch(strings.TrimRight(l, "\r"))
		if m == nil {
			continue
		}
		ln, _ := strconv.Atoi(m[2])
		col, _ := strconv.Atoi(m[3])
		errs = append(errs, mrGoError{File: m[1], Line: ln, Col: col, Msg: m[4]})
	}
	return errs
}

// mrClassify maps a compiler message to a repair recipe name,
// or "" when nothing mechanical applies.
func mrClassify(msg string) string {
	switch {
	case strings.Contains(msg, "imported and not used"):
		return "remove-import"
	case strings.Contains(msg, "declared and not used"):
		return "remove-var"
	case strings.Contains(msg, "undefined:"):
		return "missing-decl"
	case strings.Contains(msg, "syntax error"):
		return "syntax"
	case strings.Contains(msg, "missing go.sum entry"):
		return "go-mod-tidy"
	case strings.Contains(msg, "not enough arguments"),
		strings.Contains(msg, "too many arguments"):
		return "signature"
	}
	return ""
}

// mrRecipe returns the deterministic command that fixes a kind, or ""
// when the fix is a manual/logical edit (residue -> escalate to model).
func mrRecipe(kind string) string {
	switch kind {
	case "remove-import":
		return "goimports -w ." // drops unused imports mechanically
	case "go-mod-tidy":
		return "go mod tidy" // refreshes go.sum entries
	}
	// remove-var, missing-decl, syntax, signature: need an edit, not a tool.
	return ""
}
