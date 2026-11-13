package main

// argfix.go: mechanical repair of weak-model tool arguments, applied inside
// normalizeArgs so permissions, capabilities, sandbox and execution all see
// the SAME string (a policy that checks one shape while the shell runs
// another is a hole).
//
//   - bash command sent as an argv array (`{"cmd":["bash","-lc","ls -R"]}`,
//     ~19 times in the v0.13 sessions, ran as the word `[bash,-lc,ls -R]`):
//     [sh|bash|zsh, -c|-lc, X] becomes X; any other array is joined with
//     POSIX quoting; a command string that is itself a JSON array too;
//   - a dirty key: "path Metall", "path:", " command" — an unknown key that
//     starts with a required name followed by a non-letter is renamed to it,
//     only when the match is unique and the name is missing. No edit
//     distance: "paths" never becomes "path".
//
// validationErr gets one example line per built-in tool, and the Run loop
// short-circuits an identical call that already failed validation in the turn
// (the model got the same error 6 times before the loop detector stopped it).

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// afRequired: the arguments a built-in tool cannot run without (bash also
// takes job/action, but an empty bash call is always a missing command).
var afRequired = map[string][]string{
	"read":  {"path"},
	"write": {"path", "content"},
	"edit":  {"path"},
	"bash":  {"command"},
	"todo":  {"todos"},
}

// afKnown: the schema properties of each built-in with required arguments
// (a known key is never renamed).
var afKnown = map[string]map[string]bool{
	"read":  {"path": true, "offset": true, "limit": true},
	"write": {"path": true, "content": true},
	"edit":  {"path": true, "old_string": true, "new_string": true, "replace_all": true, "edits": true},
	"bash":  {"command": true, "timeout": true, "background": true, "job": true, "action": true},
	"todo":  {"todos": true},
}

// afExample is one correct call per built-in, shown in validation errors.
var afExample = map[string]string{
	"read":  `{"path":"src/main.go"}`,
	"write": `{"path":"notas.txt","content":"texto completo do arquivo"}`,
	"edit":  `{"path":"src/main.go","old_string":"texto exato atual","new_string":"texto novo"}`,
	"bash":  `{"command":"ls -la"}`,
	"todo":  `{"todos":[{"content":"rodar os testes","status":"in_progress"}]}`,
}

// afExampleLine is the example suffix of a validation error ("" for tools
// without one).
func afExampleLine(tool string) string {
	if ex := afExample[tool]; ex != "" {
		return " Correct format: " + tool + " " + ex
	}
	return ""
}

// afRepair applies the repairs above to already alias-normalized arguments.
func afRepair(tool string, m map[string]any) map[string]any {
	if req := afRequired[tool]; len(req) > 0 {
		for _, r := range req {
			if _, ok := m[r]; ok {
				continue
			}
			var hit string
			n := 0
			for k := range m {
				if afKnown[tool][k] || !afDirtyKey(k, r) {
					continue
				}
				hit = k
				n++
			}
			if n == 1 {
				m[r] = m[hit]
				delete(m, hit)
			}
		}
	}
	if tool == "bash" {
		if c, ok := afArgvCommand(m["command"]); ok {
			m["command"] = c
		}
	}
	return m
}

// afDirtyKey: k is name r with junk after it ("path Metall", "path:") or
// around it (" path "). "paths" and "pathname" are not.
func afDirtyKey(k, r string) bool {
	k = strings.TrimSpace(k)
	if k == r {
		return true
	}
	if !strings.HasPrefix(k, r) {
		return false
	}
	next, _ := utf8.DecodeRuneInString(k[len(r):])
	return !unicode.IsLetter(next) && !unicode.IsDigit(next) && next != '_'
}

// afArgvCommand turns an argv-shaped command into one command line.
// ok=false: v is not argv-shaped (leave it alone).
func afArgvCommand(v any) (string, bool) {
	var argv []any
	switch t := v.(type) {
	case []any:
		argv = t
	case string:
		s := strings.TrimSpace(t)
		// at least two elements: `[ "$x" ]` is a real test(1) call and also
		// valid JSON; argv sent as a string always has a comma
		if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") || json.Unmarshal([]byte(s), &argv) != nil || len(argv) < 2 {
			return "", false
		}
	default:
		return "", false
	}
	if len(argv) == 0 {
		return "", false
	}
	parts := make([]string, len(argv))
	for i, a := range argv {
		switch x := a.(type) {
		case string:
			parts[i] = x
		case float64, bool:
			parts[i] = fmt.Sprint(x)
		default:
			return "", false // nested arrays/objects: not argv
		}
	}
	if len(parts) == 3 && afIsShell(parts[0]) && (parts[1] == "-c" || parts[1] == "-lc" || parts[1] == "-ic" || parts[1] == "-cl") {
		return parts[2], true
	}
	if len(parts) == 1 {
		return parts[0], true // ["ls -la"]: a one-element list of a command line
	}
	for i, p := range parts {
		if !afShellOp[p] {
			parts[i] = sbShQuote(p)
		}
	}
	return strings.Join(parts, " "), true
}

func afIsShell(s string) bool {
	switch s[strings.LastIndex(s, "/")+1:] {
	case "sh", "bash", "zsh", "dash":
		return true
	}
	return false
}

// afShellOp: argv elements kept unquoted when joining (the model meant them
// as shell syntax: ["ls","-la","|","wc","-l"]).
var afShellOp = map[string]bool{"|": true, "||": true, "&&": true, ";": true, ">": true, ">>": true, "<": true, "2>&1": true, "2>/dev/null": true}

// afEmptyRequired: the call carries no argument at all for a built-in tool
// that needs some (north-mini's `bash {}`, 13 times in v0.13).
func afEmptyRequired(tc ToolCall) bool {
	if len(afRequired[tc.Name]) == 0 {
		return false
	}
	m, _, err := parseArgs(tc.Args)
	return err == nil && len(m) == 0
}

// afCallKey identifies a call byte for byte.
func afCallKey(tc ToolCall) string { return tc.Name + "\x00" + strings.TrimSpace(tc.Args) }

// afIsValidationErr: the tool refused the arguments themselves (the same
// arguments always get the same refusal).
func afIsValidationErr(out ToolOutput) bool {
	return out.IsErr && strings.HasPrefix(out.Text, "Validation failed for tool ")
}

// acBadCallShort: an identical call already failed validation in this turn:
// do not run it again, say so with the right format.
func (a *Agent) acBadCallShort(tc ToolCall) (ToolOutput, bool) {
	if _, ok := a.badCalls[afCallKey(tc)]; !ok {
		return ToolOutput{}, false
	}
	dbg("repair", map[string]any{"repeat_invalid": tc.Name})
	return ToolOutput{IsErr: true, Text: "TOOL_NOT_EXECUTED: this exact call already failed validation in this turn; the result would be the same. Fix the arguments." + afExampleLine(tc.Name),
		Display: Display{Summary: "chamada inválida repetida — não executada"}}, true
}

// acBadCallSeen records a call whose arguments the tool refused.
func (a *Agent) acBadCallSeen(tc ToolCall, out ToolOutput) {
	if !afIsValidationErr(out) {
		return
	}
	if a.badCalls == nil {
		a.badCalls = map[string]bool{}
	}
	a.badCalls[afCallKey(tc)] = true
}

// afAllEmpty: every call of the response is an empty call to a built-in that
// needs arguments.
func afAllEmpty(calls []ToolCall) bool {
	for _, c := range calls {
		if !afEmptyRequired(c) {
			return false
		}
	}
	return len(calls) > 0
}
