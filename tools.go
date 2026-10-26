package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Todo struct {
	Content string `json:"content"`
	Status  string `json:"status"` // pending | in_progress | completed
}

// Display is what the UI shows about a tool run (the model gets Text).
type Display struct {
	Summary string
	Preview []string
	Diff    []editHunk
	Todos   []Todo
	Path    string
	Lines   int // total lines the Preview was taken from (0 = unknown)
}

type ToolOutput struct {
	Text    string
	IsErr   bool
	Image   *ImagePart // attached image (read on a picture, MCP image content)
	Display Display
	Suspect string // injection label found in content the model does not see (hidden HTML); guardrails.go
	// Blocked: a guard refused this call; the PT reason for the user's report
	// ("" = not blocked). The spiral breaker counts these (blockux.go).
	Blocked string
}

type fileStamp struct {
	mod  time.Time
	size int64
	hash [32]byte
}

type snapshot struct {
	existed bool
	data    []byte
	mode    os.FileMode
	// afterBash: taken after a bash call of the same turn had already changed
	// the file, so it is not the turn-start state; the shadow-git checkpoint
	// restores such a file instead (checkpoint.go)
	afterBash bool
}

type Toolbox struct {
	Root  string
	Shell *Shell
	Web   bool // expose the fetch tool
	// MaxJobs caps concurrent background jobs (0 = defaultMaxJobs): a model in a
	// loop must not fill a 2 GB machine with servers (review T12).
	MaxJobs int
	// Checkpoints enables shadow-git snapshots (checkpoint.go) so /undo and
	// /rewind also cover files changed through bash. Off by default.
	Checkpoints bool
	// OnShellOutput gets live bash output (for the spinner); may be nil.
	OnShellOutput func(chunk []byte)
	// Window returns the current model's context window in tokens; it sizes
	// the bash result budget (toolBudget, outshape.go). nil = 30 KB.
	Window func() int
	// Scripts exposes the script tool (scripts.go); set before the first Specs().
	Scripts bool
	// UILint runs ui_lint after each write/edit of a UI file (uilint.go).
	UILint bool

	mu        sync.Mutex
	readState map[string]fileStamp
	Todos     []Todo
	turns     []turnSnap
	seenInstr map[string]bool
	specs     []ToolSpec
	hasRg     bool
	jobs      map[string]*Job
	nextJob   int
	written   int64       // bytes the write tool wrote in the current turn (per-turn cap)
	cp        *cpStore    // shadow-git checkpoints, created lazily
	sc        *scRegistry // script tool registry, loaded once (scripts.go)
	fenceOnce sync.Once   // work-dir fence roots, computed once (fence.go)
	fenceDirs []string
	obTrace   obTraceCtx // who is changing files: session, step, model, prompt (checkpoint_trace.go)
}

type turnSnap struct {
	msgIndex int // len(Agent.Msgs) when the turn began (for /rewind)
	files    map[string]*snapshot
	// shadow-git checkpoint (checkpoint.go): tree at turn start, tree after
	// the last bash call, and the absolute paths bash changed this turn
	cpStart, cpLast string
	bash            map[string]bool
}

func NewToolbox(root string) *Toolbox {
	t := &Toolbox{Root: root, Shell: NewShell(root), readState: map[string]fileStamp{}, seenInstr: map[string]bool{}, jobs: map[string]*Job{}, Web: true}
	_, err := exec.LookPath("rg")
	t.hasRg = err == nil
	return t
}

// Specs returns the built-in tool specs; computed once so the prompt stays byte-stable.
func (t *Toolbox) Specs() []ToolSpec {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.specs == nil {
		t.specs = buildToolSpecs(t.hasRg, t.Web)
		if t.Scripts {
			t.specs = append(t.specs, t.scSpecLocked()...)
		}
	}
	return t.specs
}

func buildToolSpecs(hasRg, web bool) []ToolSpec {
	search := "Use rg to search code and fd or find to locate files."
	if !hasRg {
		search = "Use grep -rn to search code (rg is not installed) and find to locate files."
	}
	specs := []ToolSpec{
		{Name: "read", Description: "Read a text file. Lines are returned as `<line number>\\t<text>`. Shows up to 2000 lines by default; for big files use offset (1-based start line) and limit. On a directory, lists its entries. On an image (png/jpg/gif/webp) attaches the picture.",
			Params: obj(map[string]any{
				"path":   str("File path (relative to the project root, absolute, or ~/path; ~ resolves to this process user's HOME)"),
				"offset": integer("1-based line number to start from"),
				"limit":  integer("Maximum number of lines to return"),
			}, "path")},
		{Name: "write", Description: "Create a file, or overwrite it completely. Parent directories are created. An existing file must be read before it can be overwritten. Prefer edit for changes to existing files. Write complete content: never use placeholders like '// rest of the code unchanged'.",
			Params: obj(map[string]any{
				"path":    str("File path (~/path resolves to this process user's HOME)"),
				"content": str("Full file content"),
			}, "path", "content")},
		{Name: "edit", Description: "Replace exact text in a file. old_string must match the file exactly, including whitespace and indentation, and must be unique unless replace_all is true: include enough surrounding lines to make it unique. Never include the line-number prefixes from read output. For several changes to one file pass edits:[{old_string,new_string}], applied atomically. Don't re-read the file afterwards to check: the tool fails if the edit did not apply.",
			Params: obj(map[string]any{
				"path":        str("File path (~/path resolves to this process user's HOME)"),
				"old_string":  str("Exact text to find"),
				"new_string":  str("Replacement text"),
				"replace_all": map[string]any{"type": "boolean", "description": "Replace every occurrence (default false)"},
				"edits": map[string]any{"type": "array", "description": "Multiple replacements in the same file", "items": obj(map[string]any{
					"old_string":  str("Exact text to find"),
					"new_string":  str("Replacement text"),
					"replace_all": map[string]any{"type": "boolean"},
				}, "old_string", "new_string")},
			}, "path")},
		{Name: "bash", Description: "Run a command in a persistent bash shell: cwd, exported variables and activated environments persist between calls. Returns stdout+stderr and the exit code; very long output is cut in the middle and saved to a file. " + search + " stdin is /dev/null, so never run interactive programs (editors, pagers, REPLs, password prompts). For servers, watchers or anything long-running set background:true (returns a job id; later use job with action output/kill) instead of appending &. Default timeout 120 s, max 600.",
			Params: obj(map[string]any{
				"command":    str("The command to run"),
				"timeout":    integer("Timeout in seconds (default 120, max 600)"),
				"background": map[string]any{"type": "boolean", "description": "Start as a detached background job and return immediately"},
				"job":        str("Job id (from a background run) to act on instead of running a command"),
				"action":     map[string]any{"type": "string", "enum": []string{"output", "kill", "list"}, "description": "With job: output = new log lines since last check, kill = stop it, list = all jobs"},
			})},
		{Name: "todo", Description: "Keep a short plan for multi-step tasks (3+ steps). Always send the complete list; keep exactly one item in_progress and mark items completed as soon as they are done.",
			Params: obj(map[string]any{
				"todos": map[string]any{"type": "array", "items": obj(map[string]any{
					"content": str("Step description"),
					"status":  map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "completed"}},
				}, "content", "status")},
			}, "todos")},
	}
	if web {
		specs = append(specs, fetchSpec)
	}
	if dcToolOn() {
		specs = append(specs, dcSpec) // docs.go
	}
	return specs
}

func obj(props map[string]any, required ...string) map[string]any {
	o := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		o["required"] = required
	}
	return o
}
func str(d string) map[string]any     { return map[string]any{"type": "string", "description": d} }
func integer(d string) map[string]any { return map[string]any{"type": "integer", "description": d} }

// ---------- robust argument handling (weak models) ----------

var toolAliases = map[string]string{
	"read_file": "read", "view": "read", "cat": "read", "open": "read", "view_file": "read", "readfile": "read", "list_dir": "read", "ls": "read", "list_directory": "read", "view_image": "read", "read_image": "read",
	"write_file": "write", "create_file": "write", "create": "write", "writefile": "write", "save_file": "write",
	"edit_file": "edit", "str_replace": "edit", "replace": "edit", "str_replace_editor": "edit", "multiedit": "edit", "multi_edit": "edit", "apply_edit": "edit", "search_replace": "edit",
	"shell": "bash", "run": "bash", "exec": "bash", "execute": "bash", "run_command": "bash", "terminal": "bash", "sh": "bash", "command": "bash", "execute_command": "bash", "run_shell_command": "bash", "run_terminal_cmd": "bash", "exec_command": "bash",
	"todowrite": "todo", "todo_write": "todo", "update_plan": "todo", "write_todos": "todo", "plan": "todo", "todos": "todo",
	"web_fetch": "fetch", "webfetch": "fetch", "web_search": "fetch", "websearch": "fetch", "search": "fetch", "browse": "fetch", "http_get": "fetch", "curl": "fetch",
	"get_docs": "docs", "library_docs": "docs", "lookup_docs": "docs", "docs_lookup": "docs", "get-library-docs": "docs", "query-docs": "docs", "query_docs": "docs",
	"agent": "task", "subagent": "task", "spawn_agent": "task", "delegate": "task", "explore": "task",
}

var paramAliases = map[string]map[string]string{
	"read":  {"file_path": "path", "file": "path", "filename": "path", "filepath": "path", "absolute_path": "path", "target_file": "path", "line": "offset", "start_line": "offset", "start": "offset", "lines": "limit", "max_lines": "limit", "count": "limit", "end_line": "limit"},
	"write": {"file_path": "path", "file": "path", "filename": "path", "filepath": "path", "absolute_path": "path", "target_file": "path", "text": "content", "contents": "content", "data": "content", "code": "content", "body": "content", "file_text": "content"},
	"edit": {"file_path": "path", "file": "path", "filename": "path", "filepath": "path", "absolute_path": "path", "target_file": "path",
		"old": "old_string", "oldText": "old_string", "old_str": "old_string", "old_text": "old_string", "search": "old_string", "find": "old_string", "oldString": "old_string", "target": "old_string", "original": "old_string",
		"new": "new_string", "newText": "new_string", "new_str": "new_string", "new_text": "new_string", "replace": "new_string", "replacement": "new_string", "newString": "new_string", "replace_with": "new_string",
		"replaceAll": "replace_all", "all": "replace_all", "allow_multiple": "replace_all", "expected_replacements": "replace_all"},
	"bash":  {"cmd": "command", "script": "command", "code": "command", "shell": "command", "commands": "command", "timeout_seconds": "timeout", "timeout_sec": "timeout", "timeout_ms": "timeout", "run_in_background": "background", "is_background": "background", "detach": "background", "job_id": "job", "id": "job", "shell_id": "job", "bash_id": "job"},
	"todo":  {"items": "todos", "tasks": "todos", "list": "todos", "plan": "todos", "steps": "todos"},
	"fetch": {"uri": "url", "link": "url", "address": "url", "q": "query", "search": "query", "search_query": "query", "max_length": "max_chars", "limit": "max_chars", "prompt": "query"},
	"docs":  {"lib": "library", "package": "library", "pkg": "library", "module": "library", "name": "library", "libraryName": "library", "q": "query", "topic": "query", "symbol": "query", "search": "query", "ver": "version"},
	"task":  {"task": "prompt", "instructions": "prompt", "query": "prompt", "goal": "prompt", "desc": "description", "title": "description"},
}

// escapeRawControl escapes literal newlines/tabs that appear inside JSON strings.
func escapeRawControl(s string) string {
	var b strings.Builder
	in, esc := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if in {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				in = false
			case c == '\n':
				b.WriteString(`\n`)
				continue
			case c == '\r':
				b.WriteString(`\r`)
				continue
			case c == '\t':
				b.WriteString(`\t`)
				continue
			}
		} else if c == '"' {
			in = true
		}
		b.WriteByte(c)
	}
	return b.String()
}

// stripTrailingCommas removes ", }" / ", ]" only outside of strings.
func stripTrailingCommas(s string) string {
	var b strings.Builder
	in, esc := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if in {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				in = false
			}
			b.WriteByte(c)
			continue
		}
		if c == '"' {
			in = true
		} else if c == ',' {
			j := i + 1
			for j < len(s) && (s[j] == ' ' || s[j] == '\n' || s[j] == '\t' || s[j] == '\r') {
				j++
			}
			if j < len(s) && (s[j] == '}' || s[j] == ']') {
				continue
			}
		}
		b.WriteByte(c)
	}
	return b.String()
}

// closeTruncated appends the closers a cut-off JSON object needs.
func closeTruncated(s string) string {
	var stack []byte
	in, esc := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if in {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				in = false
			}
			continue
		}
		switch c {
		case '"':
			in = true
		case '{', '[':
			stack = append(stack, c)
		case '}', ']':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	out := s
	if in {
		out += `"`
	}
	out = regexp.MustCompile(`[,:]\s*$`).ReplaceAllString(out, "")
	if regexp.MustCompile(`"\s*$`).MatchString(out) && !in && len(stack) > 0 && stack[len(stack)-1] == '{' {
		// a dangling key without value: {"a":1,"b"
		if m := regexp.MustCompile(`[{,]\s*"[^"]*"\s*$`).FindStringIndex(out); m != nil {
			out = strings.TrimRight(out[:m[0]+1], ", \n")
		}
	}
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i] == '{' {
			out += "}"
		} else {
			out += "]"
		}
	}
	return out
}

var fenceRe = lazyRx("(?is)^```[a-z_-]*\\s*")

// parseArgs parses tool-call arguments, repairing common weak-model mistakes.
// repaired=true means the JSON was truncated and closed heuristically: such
// arguments must never be used for write/edit content.
func parseArgs(raw string) (m map[string]any, repaired bool, err error) {
	s := strings.TrimSpace(raw)
	if s == "" || s == "null" {
		return map[string]any{}, false, nil
	}
	if strings.HasPrefix(s, "```") {
		s = fenceRe.ReplaceAllString(s, "")
		s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "```"))
	}
	try := func(x string) (map[string]any, bool) {
		var v any
		if json.Unmarshal([]byte(x), &v) != nil {
			return nil, false
		}
		if inner, ok := v.(string); ok { // double-encoded
			if json.Unmarshal([]byte(inner), &v) != nil {
				return nil, false
			}
		}
		mm, ok := v.(map[string]any)
		return mm, ok
	}
	for _, cand := range []string{s, stripTrailingCommas(s), stripTrailingCommas(escapeRawControl(s))} {
		if mm, ok := try(cand); ok {
			return mm, false, nil
		}
	}
	if strings.HasPrefix(s, "{") { // truncated JSON (output limit hit)
		if mm, ok := try(closeTruncated(escapeRawControl(s))); ok {
			return mm, true, nil
		}
	}
	return nil, false, fmt.Errorf("arguments are not valid JSON")
}

func normalizeArgs(tool string, m map[string]any) map[string]any {
	al := paramAliases[tool]
	out := make(map[string]any, len(m))
	for k, v := range m {
		if to, ok := al[k]; ok {
			if _, exists := m[to]; !exists {
				k = to
			}
		}
		out[k] = v
	}
	return out
}

func argStr(m map[string]any, k string) (string, bool) {
	v, ok := m[k]
	if !ok || v == nil {
		return "", false
	}
	switch t := v.(type) {
	case string:
		return t, true
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(t), true
	default:
		b, _ := json.Marshal(t)
		return string(b), true
	}
}

func argInt(m map[string]any, k string) (int, bool) {
	v, ok := m[k]
	if !ok || v == nil {
		return 0, false
	}
	switch t := v.(type) {
	case float64:
		return int(t), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(t))
		return n, err == nil
	}
	return 0, false
}

func argBool(m map[string]any, k string) bool {
	switch t := m[k].(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(strings.TrimSpace(t), "true") || strings.TrimSpace(t) == "1"
	case float64:
		return t != 0
	}
	return false
}

func validationErr(tool, msg, raw string) ToolOutput {
	if len(raw) > 600 {
		raw = raw[:600] + "…"
	}
	return ToolOutput{IsErr: true, Text: fmt.Sprintf("Validation failed for tool %q: %s. Received arguments: %s", tool, msg, raw),
		Display: Display{Summary: "argumentos inválidos: " + msg}}
}

// editOpsArgs reads the edit operations (edits[] or old_string/new_string).
// A stringified edits array is decoded leniently: models often send it with
// raw newlines inside the strings (invalid JSON), and refusing it costs a
// whole round trip that re-emits the same big call (eval v2, 03-arquivo-grande).
func editOpsArgs(m map[string]any, raw string) ([]editOp, *ToolOutput) {
	if s, ok := m["edits"].(string); ok { // stringified array
		for _, cand := range []string{s, stripTrailingCommas(escapeRawControl(s))} {
			var x any
			if json.Unmarshal([]byte(cand), &x) == nil {
				m["edits"] = x
				break
			}
		}
		if _, still := m["edits"].(string); still {
			o := validationErr("edit", "'edits' must be an array of {old_string, new_string} objects; the string sent is not valid JSON", raw)
			return nil, &o
		}
	}
	var ops []editOp
	if arr, ok := m["edits"].([]any); ok && len(arr) > 0 {
		for i, e := range arr {
			em, ok := e.(map[string]any)
			if !ok {
				o := validationErr("edit", fmt.Sprintf("edits[%d] must be an object", i), raw)
				return nil, &o
			}
			em = normalizeArgs("edit", em)
			o, ok1 := argStr(em, "old_string")
			n, ok2 := argStr(em, "new_string")
			if !ok1 || !ok2 {
				v := validationErr("edit", fmt.Sprintf("edits[%d] needs old_string and new_string", i), raw)
				return nil, &v
			}
			ops = append(ops, editOp{Old: o, New: n, All: argBool(em, "replace_all")})
		}
		return ops, nil
	}
	o, ok1 := argStr(m, "old_string")
	n, ok2 := argStr(m, "new_string")
	if !ok1 {
		v := validationErr("edit", "missing required parameter 'old_string' (or 'edits')", raw)
		return nil, &v
	}
	if !ok2 {
		v := validationErr("edit", "missing required parameter 'new_string'", raw)
		return nil, &v
	}
	return []editOp{{Old: o, New: n, All: argBool(m, "replace_all")}}, nil
}

func CanonicalTool(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	if i := strings.LastIndexAny(n, ".:"); i >= 0 { // "functions.bash", "default_api:read"
		n = n[i+1:]
	}
	if a, ok := toolAliases[n]; ok {
		return a
	}
	return n
}

// ---------- paths, read-state, checkpoints ----------

func (t *Toolbox) abs(p string) string {
	p = strings.TrimSpace(p)
	if strings.HasPrefix(p, "~/") || p == "~" {
		h, _ := os.UserHomeDir()
		p = filepath.Join(h, strings.TrimPrefix(p, "~"))
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(t.Root, p)
	}
	return filepath.Clean(p)
}

func (t *Toolbox) rel(p string) string {
	if r, err := filepath.Rel(t.Root, p); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return p
}

func (t *Toolbox) stampData(p string, data []byte) {
	niTouch(p) // session context for the name resolver (nameindex.go)
	if fi, err := os.Stat(p); err == nil {
		t.mu.Lock()
		t.readState[p] = fileStamp{fi.ModTime(), fi.Size(), sha256.Sum256(data)}
		t.mu.Unlock()
	}
}

// MarkRead records a file as read (used for @file mentions and task results).
func (t *Toolbox) MarkRead(p string, data []byte) { t.stampData(t.abs(p), data) }

// stale: read before, but the file changed since (by bash or externally).
func (t *Toolbox) stale(p string, fi os.FileInfo, data []byte) bool {
	t.mu.Lock()
	st, ok := t.readState[p]
	t.mu.Unlock()
	if !ok {
		return false
	}
	if st.mod.Equal(fi.ModTime()) && st.size == fi.Size() {
		return false
	}
	return sha256.Sum256(data) != st.hash
}

func (t *Toolbox) wasRead(p string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.readState[p]
	return ok
}

// BeginTurn opens a new checkpoint for /undo and /rewind.
func (t *Toolbox) BeginTurn(msgIndex int) {
	tree := t.cpSnapshot()
	if tree != "" {
		t.obCommit(t.checkpoints(), tree, "turn-start") // hand edits between turns (checkpoint_trace.go)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.turns = append(t.turns, turnSnap{msgIndex: msgIndex, files: map[string]*snapshot{}, cpStart: tree, cpLast: tree})
	if len(t.turns) > 100 {
		t.turns = t.turns[1:]
	}
	t.written = 0
}

// TurnWritten is the number of bytes the write tool has written this turn.
func (t *Toolbox) TurnWritten() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.written
}

// addWritten records bytes written and reports whether the per-turn cap (mb MB)
// is now exceeded, so the write tool can refuse a single oversized write.
func (t *Toolbox) addWritten(n int64) {
	t.mu.Lock()
	t.written += n
	t.mu.Unlock()
}

func (t *Toolbox) snap(p string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.turns) == 0 {
		t.turns = append(t.turns, turnSnap{files: map[string]*snapshot{}})
	}
	turn := t.turns[len(t.turns)-1]
	cur := turn.files
	if _, done := cur[p]; done {
		return
	}
	s := &snapshot{afterBash: turn.bash[p]}
	if fi, err := os.Stat(p); err == nil {
		if b, err := os.ReadFile(p); err == nil {
			s.existed, s.data, s.mode = true, b, fi.Mode().Perm()
		}
	}
	cur[p] = s
}

func (t *Toolbox) restore(ts turnSnap) ([]string, error) {
	paths, cperr := t.cpRestore(ts)
	for p, s := range ts.files {
		if s.afterBash && ts.cpStart != "" {
			continue // restored from the checkpoint by cpRestore
		}
		if s.existed {
			if err := restoreFile(p, s.data, s.mode, t.Root); err != nil {
				return paths, err
			}
		} else if t.Root == "" || !pathUnder(filepath.Clean(p), filepath.Clean(t.Root)) || cpParentInside(t.Root, p) {
			os.Remove(p) // removes a symlink itself, never its target
		}
		delete(t.readState, p)
		paths = append(paths, t.rel(p))
	}
	sort.Strings(paths)
	return paths, cperr
}

// restoreFile writes an undo snapshot back without following symlinks (F8):
// if bash replaced p with a symlink during the turn, the link is removed and
// a regular file is created in its place (temp file + rename in the same
// directory), so /undo cannot write into ~/Library/LaunchAgents through a
// planted link. A parent directory that now resolves outside the project is
// refused.
func restoreFile(p string, data []byte, mode os.FileMode, root string) error {
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if root != "" && pathUnder(filepath.Clean(p), filepath.Clean(root)) && !pfInsideRoot(dir, root) {
		return fmt.Errorf("não restaurei %s: a pasta virou um link para fora do projeto", p)
	}
	if fi, err := os.Lstat(p); err == nil && !fi.Mode().IsRegular() {
		if fi.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("não restaurei %s: não é mais um arquivo comum", p)
		}
		if err := os.Remove(p); err != nil {
			return err
		}
	}
	if mode == 0 {
		mode = 0o644
	}
	tmp, err := os.CreateTemp(dir, ".jazz-undo-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, mode); err != nil {
		return err
	}
	return os.Rename(name, p)
}

// Undo restores files changed in the most recent turn that changed files.
func (t *Toolbox) Undo() ([]string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for len(t.turns) > 0 {
		last := t.turns[len(t.turns)-1]
		t.turns = t.turns[:len(t.turns)-1]
		if len(last.files) == 0 && len(last.bash) == 0 {
			continue
		}
		return t.restore(last)
	}
	return nil, nil
}

// RebaseTurns shifts the checkpoints' message indexes after a compaction
// replaced Msgs[:cut] with one summary message: a turn that began inside the
// summarized part maps to 0, a later one moves down by offset.
func (t *Toolbox) RebaseTurns(cut, offset int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := range t.turns {
		if t.turns[i].msgIndex < cut {
			t.turns[i].msgIndex = 0
		} else {
			t.turns[i].msgIndex = max(0, t.turns[i].msgIndex-offset)
		}
	}
}

// RewindTo restores every file changed by turns that began at or after msgIndex.
func (t *Toolbox) RewindTo(msgIndex int) ([]string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var all []string
	for len(t.turns) > 0 {
		last := t.turns[len(t.turns)-1]
		if last.msgIndex < msgIndex {
			break
		}
		t.turns = t.turns[:len(t.turns)-1]
		paths, err := t.restore(last)
		all = append(all, paths...)
		if err != nil {
			return all, err
		}
	}
	sort.Strings(all)
	return all, nil
}

// DropTurnsFrom forgets, without restoring, the checkpoints of turns that
// began at or after msgIndex (used by /fork: those edits stay on disk).
func (t *Toolbox) DropTurnsFrom(msgIndex int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	k := len(t.turns)
	for k > 0 && t.turns[k-1].msgIndex >= msgIndex {
		k--
	}
	t.turns = t.turns[:k]
}

// ---------- dispatch ----------

func (t *Toolbox) Exec(ctx context.Context, name, rawArgs string) ToolOutput {
	out := t.exec(ctx, name, rawArgs)
	// known keys and token shapes never reach the model, the session log or the
	// screen (bug 12): a read of auth.json or a printed .env is redacted too
	out.Text = rdToolOut(out.Text, "tool:"+name) // reversible placeholders, then the shapes left (redact.go)
	for i, l := range out.Display.Preview {
		out.Display.Preview[i] = rdToolText(l, "tool:"+name)
	}
	return out
}

func (t *Toolbox) exec(ctx context.Context, name, rawArgs string) ToolOutput {
	tool := CanonicalTool(name)
	m, repaired, err := parseArgs(rawArgs)
	if err != nil {
		return validationErr(tool, err.Error(), rawArgs)
	}
	m = normalizeArgs(tool, m)
	if repaired && (tool == "write" || tool == "edit" || tool == "bash" || tool == "script") {
		return ToolOutput{IsErr: true, Text: fmt.Sprintf("Validation failed for tool %q: the arguments JSON was cut off (output token limit) so the content is incomplete. Re-issue the call; for a big file, write a skeleton first and add the rest with several smaller edit calls.", tool),
			Display: Display{Summary: "argumentos cortados pelo limite de saída"}}
	}
	switch tool {
	case "read":
		return t.read(ctx, m, rawArgs)
	case "write":
		return t.write(m, rawArgs)
	case "edit":
		return t.edit(m, rawArgs)
	case "bash":
		return t.bash(ctx, m, rawArgs)
	case "todo":
		return t.todo(m, rawArgs)
	case "fetch":
		if !t.Web {
			break
		}
		return fetchTool(ctx, m, rawArgs)
	case "script":
		if t.Scripts {
			return t.script(ctx, m, rawArgs)
		}
	case "docs":
		if dcToolOn() {
			return t.docs(ctx, m, rawArgs)
		}
	}
	return ToolOutput{IsErr: true, Text: fmt.Sprintf("Tool %q not found. Available tools: %s.", name, strings.Join(t.toolNames(), ", ")),
		Display: Display{Summary: "ferramenta desconhecida: " + name}}
}

func (t *Toolbox) toolNames() []string {
	var n []string
	for _, s := range t.Specs() {
		n = append(n, s.Name)
	}
	return n
}

// instrNote returns nested AGENTS.md content for a path touched inside the project
// (lazy injection, Claude Code style); each directory is reported once per session.
func (t *Toolbox) instrNote(p string) string {
	if !strings.HasPrefix(p, t.Root+string(filepath.Separator)) {
		return ""
	}
	var notes []string
	for d := filepath.Dir(p); len(d) > len(t.Root); d = filepath.Dir(d) {
		t.mu.Lock()
		seen := t.seenInstr[d]
		t.seenInstr[d] = true
		t.mu.Unlock()
		if seen {
			continue
		}
		for _, n := range []string{"AGENTS.override.md", "AGENTS.md", "CLAUDE.md", "JAZZ.md"} {
			b, ok := pfSafeRead(filepath.Join(d, n), t.Root, 1<<20) // no symlinks out of the project (F6)
			if !ok || len(bytes.TrimSpace(b)) == 0 {
				continue
			}
			s := string(b)
			if len(s) > 8<<10 {
				s = s[:8<<10] + "\n[… truncated]"
			}
			notes = append(notes, fmt.Sprintf("<system-reminder>Instructions from %s (apply to files under %s):\n%s\n</system-reminder>", filepath.Join(t.rel(d), n), t.rel(d), strings.TrimSpace(s)))
			break
		}
	}
	if len(notes) == 0 {
		return ""
	}
	return "\n\n" + strings.Join(notes, "\n")
}

// TodoList returns a copy of the current todo list.
func (t *Toolbox) TodoList() []Todo {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]Todo(nil), t.Todos...)
}

// Reset clears per-conversation state (todos, read stamps, checkpoints,
// lazily-injected instructions) for /clear. Background jobs are not touched:
// callers decide with RunningJobs/KillJobs.
func (t *Toolbox) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Todos = nil
	t.readState = map[string]fileStamp{}
	t.turns = nil
	t.seenInstr = map[string]bool{}
}

// MarkInstrSeen records directories whose instructions are already in the system prompt.
func (t *Toolbox) MarkInstrSeen(dirs []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, d := range dirs {
		t.seenInstr[d] = true
	}
}

var imageExt = map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp"}

func (t *Toolbox) read(ctx context.Context, m map[string]any, raw string) ToolOutput {
	ps, ok := argStr(m, "path")
	if !ok || strings.TrimSpace(ps) == "" {
		return validationErr("read", "missing required parameter 'path'", raw)
	}
	p := t.abs(ps)
	fi, err := os.Stat(p)
	if err != nil {
		hint := ""
		if os.IsNotExist(err) {
			hint = " Use bash (fd/find/ls) to locate it."
			if sim := niSimilarPaths(t.Root, ps, 1<<20); len(sim) > 0 {
				hint = " não existe; parecido: " + strings.Join(sim, ", ") + "."
			}
		}
		return ToolOutput{IsErr: true, Text: fmt.Sprintf("Cannot read %s: %v.%s", ps, errText(err), hint), Display: Display{Summary: "não encontrado", Path: t.rel(p)}}
	}
	if fi.IsDir() {
		ents, _ := os.ReadDir(p)
		sort.Slice(ents, func(i, j int) bool {
			if ents[i].IsDir() != ents[j].IsDir() {
				return ents[i].IsDir()
			}
			return ents[i].Name() < ents[j].Name()
		})
		var b strings.Builder
		fmt.Fprintf(&b, "Directory %s (%d entries):\n", t.rel(p), len(ents))
		for i, e := range ents {
			if i >= 500 {
				fmt.Fprintf(&b, "… %d more\n", len(ents)-500)
				break
			}
			if e.IsDir() {
				b.WriteString(e.Name() + "/\n")
			} else if info, err := e.Info(); err == nil {
				fmt.Fprintf(&b, "%s  (%s)\n", e.Name(), humanBytes(info.Size()))
			}
		}
		return ToolOutput{Text: b.String(), Display: Display{Summary: fmt.Sprintf("%d itens", len(ents)), Path: t.rel(p)}}
	}
	if mime, isImg := imageExt[strings.ToLower(filepath.Ext(p))]; isImg {
		if fi.Size() > 5<<20 {
			return ToolOutput{IsErr: true, Text: fmt.Sprintf("%s is a %s image, too large to attach (limit 5 MB).", ps, humanBytes(fi.Size()))}
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return ToolOutput{IsErr: true, Text: fmt.Sprintf("Cannot read %s: %v", ps, errText(err))}
		}
		dims := ""
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
			dims = fmt.Sprintf(" %dx%d", cfg.Width, cfg.Height)
		}
		return ToolOutput{Text: fmt.Sprintf("[image attached: %s%s, %s]", ps, dims, humanBytes(fi.Size())), Image: &ImagePart{Mime: mime, Data: base64.StdEncoding.EncodeToString(data)},
			Display: Display{Summary: "imagem anexada" + dims, Path: t.rel(p)}}
	}
	if fi.Size() > 25<<20 {
		return ToolOutput{IsErr: true, Text: fmt.Sprintf("%s is %s, too large to read. Use bash with rg/head/tail/sed -n to look at parts of it.", ps, humanBytes(fi.Size()))}
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return ToolOutput{IsErr: true, Text: fmt.Sprintf("Cannot read %s: %v", ps, errText(err))}
	}
	head := data
	if len(head) > 8192 {
		head = head[:8192]
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return ToolOutput{IsErr: true, Text: fmt.Sprintf("%s is a binary file (%s). Use bash (file, strings, xxd | head) to inspect it.", ps, humanBytes(fi.Size()))}
	}
	t.ptStamp(ctx, p, data) // deferred for an early parallel read (partools.go)
	text := strings.TrimPrefix(toLF(string(data)), "\uFEFF")
	if text == "" {
		return ToolOutput{Text: fmt.Sprintf("%s is empty.", ps), Display: Display{Summary: "vazio", Path: t.rel(p)}}
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	off, _ := argInt(m, "offset")
	if off < 1 {
		off = 1
	}
	if off > len(lines) {
		return ToolOutput{IsErr: true, Text: fmt.Sprintf("offset %d is past the end of %s (%d lines).", off, ps, len(lines))}
	}
	lim, ok := argInt(m, "limit")
	if !ok || lim <= 0 {
		lim = maxReadLines
	}
	readCap := maxReadBytes
	if bgIsDoc(p) {
		readCap = maxDocReadBytes // a design doc is read whole, in one call
	}
	var b strings.Builder
	end := off - 1
	for i := off - 1; i < len(lines) && i < off-1+lim; i++ {
		l := fmt.Sprintf("%6d\t%s\n", i+1, clipLine(lines[i]))
		if b.Len()+len(l) > readCap && i > off-1 {
			break
		}
		b.WriteString(l)
		end = i + 1
	}
	if end < len(lines) {
		fmt.Fprintf(&b, "\n[Showing lines %d-%d of %d. Use offset=%d to continue.]\n", off, end, len(lines), end+1)
	}
	sum := fmt.Sprintf("%d %s", end-off+1, uiPl(end-off+1, "linha", "linhas"))
	if end-off+1 < len(lines) {
		sum = fmt.Sprintf("linhas %d-%d de %d", off, end, len(lines))
	}
	return ToolOutput{Text: b.String() + t.ptInstrNote(ctx, p), Display: Display{Summary: sum, Path: t.rel(p)}}
}

func isProseFile(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".md", ".txt", ".rst", ".adoc", ".markdown", "":
		return true
	}
	return false
}

func (t *Toolbox) write(m map[string]any, raw string) ToolOutput {
	ps, ok := argStr(m, "path")
	if !ok || strings.TrimSpace(ps) == "" {
		return validationErr("write", "missing required parameter 'path'", raw)
	}
	content, ok := argStr(m, "content")
	if !ok {
		return validationErr("write", "missing required parameter 'content'", raw)
	}
	p := t.abs(ps)
	mode := os.FileMode(0o644)
	existing := ""
	simNote := ""
	if _, serr := os.Stat(p); serr != nil && os.IsNotExist(serr) { // a new file next to a near-twin is often a typo
		if sim := niSimilarPaths(t.Root, ps, fzSlip); len(sim) > 0 {
			simNote = " Novo arquivo; já existe parecido: " + strings.Join(sim, ", ") + " (confira se não era esse)."
		}
	}
	if fi, err := os.Stat(p); err == nil {
		if fi.IsDir() {
			return ToolOutput{IsErr: true, Text: ps + " is a directory."}
		}
		if !t.wasRead(p) {
			return ToolOutput{IsErr: true, Text: fmt.Sprintf("%s already exists. Read it first before overwriting it, or use edit for partial changes.", ps), Display: Display{Summary: "bloqueado: arquivo existe e não foi lido", Path: t.rel(p)}}
		}
		data, _ := os.ReadFile(p)
		if t.stale(p, fi, data) {
			return ToolOutput{IsErr: true, Text: fmt.Sprintf("%s changed since you last read it. Read it again before overwriting.", ps), Display: Display{Summary: "bloqueado: arquivo mudou desde a leitura", Path: t.rel(p)}}
		}
		existing = string(data)
		mode = fi.Mode().Perm()
	}
	if !isProseFile(p) {
		if mk := omissionMarker(content, existing); mk != "" {
			return ToolOutput{IsErr: true, Text: fmt.Sprintf("Refused: the content contains the placeholder %q, which would replace real code with a comment. Write the complete content; for a long file, write it in parts (write a skeleton, then edit).", mk), Display: Display{Summary: "bloqueado: placeholder no lugar de código", Path: t.rel(p)}}
		}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return ToolOutput{IsErr: true, Text: "Cannot create directory: " + errText(err)}
	}
	if t.TurnWritten()+int64(len(content)) > defTurnWriteMB<<20 {
		return ToolOutput{IsErr: true, Text: fmt.Sprintf("Refused: this write would exceed the per-turn disk-write cap of %d MB (max_turn_write_mb). Write less, or split the work across turns.", defTurnWriteMB), Display: Display{Summary: "bloqueado: limite de escrita do turno", Path: t.rel(p)}}
	}
	t.snap(p)
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		return ToolOutput{IsErr: true, Text: fmt.Sprintf("Cannot write %s: %v", ps, errText(err))}
	}
	t.addWritten(int64(len(content)))
	t.stampData(p, []byte(content))
	n := strings.Count(content, "\n")
	if content != "" && !strings.HasSuffix(content, "\n") {
		n++
	}
	prev := strings.Split(content, "\n")
	if len(prev) > 4 {
		prev = prev[:4]
	}
	return ToolOutput{Text: fmt.Sprintf("Wrote %d lines (%s) to %s.", n, humanBytes(int64(len(content))), ps) + simNote + t.instrNote(p),
		Display: Display{Summary: fmt.Sprintf("%d linhas escritas", n), Preview: prev, Path: t.rel(p), Lines: n}}
}

func (t *Toolbox) edit(m map[string]any, raw string) ToolOutput {
	ps, ok := argStr(m, "path")
	if !ok || strings.TrimSpace(ps) == "" {
		return validationErr("edit", "missing required parameter 'path'", raw)
	}
	ops, bad := editOpsArgs(m, raw)
	if bad != nil {
		return *bad
	}
	p := t.abs(ps)
	fi, err := os.Stat(p)
	if err != nil {
		if os.IsNotExist(err) {
			msg := fmt.Sprintf("%s does not exist. Use write to create it.", ps)
			if sim := niSimilarPaths(t.Root, ps, 1<<20); len(sim) > 0 {
				msg = fmt.Sprintf("%s não existe; parecido: %s. Edit the right one, or use write to create it.", ps, strings.Join(sim, ", "))
			}
			return ToolOutput{IsErr: true, Text: msg, Display: Display{Summary: "arquivo não existe", Path: t.rel(p)}}
		}
		return ToolOutput{IsErr: true, Text: "Cannot edit: " + errText(err)}
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return ToolOutput{IsErr: true, Text: "Cannot read file: " + errText(err)}
	}
	if t.stale(p, fi, data) {
		return ToolOutput{IsErr: true, Text: fmt.Sprintf("%s changed since you last read it (by a command or externally). Read it again before editing.", ps), Display: Display{Summary: "arquivo mudou desde a leitura", Path: t.rel(p)}}
	}
	orig := string(data)
	bom := strings.HasPrefix(orig, "\uFEFF")
	orig = strings.TrimPrefix(orig, "\uFEFF")
	crlf := detectCRLF(orig)
	if !isProseFile(p) {
		for _, o := range ops {
			if mk := omissionMarker(o.New, orig); mk != "" {
				return ToolOutput{IsErr: true, Text: fmt.Sprintf("Refused: new_string contains the placeholder %q, which would delete real code. Write the complete code; use several targeted edits instead.", mk), Display: Display{Summary: "bloqueado: placeholder no lugar de código", Path: t.rel(p)}}
			}
		}
	}
	res, err := applyEdits(toLF(orig), ops, ps)
	if err != nil {
		return ToolOutput{IsErr: true, Text: err.Error(), Display: Display{Summary: firstSentence(err.Error()), Path: t.rel(p)}}
	}
	out := res.Content
	if crlf {
		out = strings.ReplaceAll(out, "\n", "\r\n")
	}
	if bom {
		out = "\uFEFF" + out
	}
	t.snap(p)
	if err := os.WriteFile(p, []byte(out), fi.Mode().Perm()); err != nil {
		return ToolOutput{IsErr: true, Text: "Cannot write file: " + errText(err)}
	}
	t.stampData(p, []byte(out))
	var b strings.Builder
	fmt.Fprintf(&b, "Edited %s (%d replacement", ps, res.Count)
	if res.Count != 1 {
		b.WriteString("s")
	}
	b.WriteString(").")
	if res.Fuzzy {
		b.WriteString(" Note: matched after normalizing whitespace/Unicode quotes; the touched lines were rewritten.")
	}
	if len(res.Hunks) > 0 {
		lines := strings.Split(res.Content, "\n")
		h := res.Hunks[0]
		from := h.Line - 3
		if from < 1 {
			from = 1
		}
		to := h.Line + len(h.New) + 2
		if to > len(lines) {
			to = len(lines)
		}
		if to-from > 24 {
			to = from + 24
		}
		fmt.Fprintf(&b, " Lines %d-%d now:\n", from, to)
		for i := from; i <= to; i++ {
			fmt.Fprintf(&b, "%6d\t%s\n", i, clipLine(lines[i-1]))
		}
	}
	add, del := 0, 0
	for _, h := range res.Hunks {
		add += len(h.New)
		del += len(h.Old)
	}
	if w := editDanglingRenames(res.Content, ops); w != "" {
		b.WriteString("\n" + w)
	}
	return ToolOutput{Text: b.String() + t.instrNote(p), Display: Display{Summary: fmt.Sprintf("+%d −%d", add, del), Diff: res.Hunks, Path: t.rel(p)}}
}

func (t *Toolbox) bash(ctx context.Context, m map[string]any, raw string) ToolOutput {
	if job, ok := argStr(m, "job"); ok && strings.TrimSpace(job) != "" || m["action"] == "list" {
		act, _ := argStr(m, "action")
		job, _ := argStr(m, "job")
		return t.jobAction(strings.TrimSpace(job), act)
	}
	cmd, ok := argStr(m, "command")
	if !ok || strings.TrimSpace(cmd) == "" {
		return validationErr("bash", "missing required parameter 'command'", raw)
	}
	if argBool(m, "background") {
		return t.startJob(ctx, cmd)
	}
	to, ok := argInt(m, "timeout")
	if !ok || to <= 0 {
		to = 120
	}
	if to >= 10000 && to%1000 == 0 { // model passed milliseconds
		to /= 1000
	}
	if to > 600 {
		to = 600
	}
	sh, onOut := t.Shell, t.OnShellOutput
	if own := saShellFrom(ctx); own != nil { // a parallel subagent has its own shell (subagent.go)
		sh, onOut = own, nil
	}
	window, cwd := t.Window, ""
	var r ShellResult
	if pr := ptFrom(ctx); pr != nil && pr.env != nil && IsReadOnly(cmd) {
		// early read-only command of a parallel batch: a short-lived shell,
		// never the persistent one (partools.go)
		r = sh.ptRunOnce(ctx, cmd, pr.cwd, pr.env, time.Duration(to)*time.Second)
		pr.oneShotCode = r.ExitCode
		cwd = pr.cwd
		w := pr.window
		window = func() int { return w }
		if t.Window == nil {
			window = nil
		}
	} else {
		r = sh.Run(ctx, cmd, time.Duration(to)*time.Second, onOut)
		t.cpAfterBash(cmd)
		cwd = sh.Cwd
	}
	var b strings.Builder
	if r.Output != "" {
		set := opSettingsNow() // --parse-output / --grep-trunc / --tool-budget (outshape.go)
		b.WriteString(opShapeOutput(cmd, rdToolText(r.Output, "tool:bash"), opBudget(set, window), set))
		b.WriteString("\n")
	}
	if !r.Reset {
		fmt.Fprintf(&b, "[exit code %d]", r.ExitCode)
	}
	if r.Note != "" {
		b.WriteString(r.Note)
	}
	if !r.Reset && cwd != "" && cwd != t.Root {
		fmt.Fprintf(&b, " [cwd: %s]", cwd)
	}
	lines := nonEmptyLines(r.Output)
	var prev []string
	if r.ExitCode != 0 && len(lines) > 4 {
		prev = lines[len(lines)-4:]
	} else if len(lines) > 4 {
		prev = lines[:4]
	} else {
		prev = lines
	}
	sum := fmt.Sprintf("exit %d · %d %s · %s", r.ExitCode, len(lines), uiPl(len(lines), "linha", "linhas"), r.Duration.Round(100*time.Millisecond))
	if r.Reset {
		sum = firstSentence(strings.Trim(r.Note, "[]"))
	}
	return ToolOutput{Text: b.String(), IsErr: r.ExitCode != 0, Display: Display{Summary: sum, Preview: prev, Lines: len(lines)}}
}

func (t *Toolbox) todo(m map[string]any, raw string) ToolOutput {
	v := m["todos"]
	if s, ok := v.(string); ok { // stringified list
		var x any
		if json.Unmarshal([]byte(s), &x) == nil {
			v = x
		}
	}
	arr, ok := v.([]any)
	if !ok {
		return validationErr("todo", "'todos' must be an array of {content, status}", raw)
	}
	var list []Todo
	for _, it := range arr {
		var td Todo
		switch x := it.(type) {
		case string:
			td = Todo{Content: x, Status: "pending"}
		case map[string]any:
			for _, k := range []string{"content", "step", "task", "title", "text", "description", "name"} {
				if s, ok := argStr(x, k); ok && s != "" {
					td.Content = s
					break
				}
			}
			st, _ := argStr(x, "status")
			if st == "" {
				if argBool(x, "completed") || argBool(x, "done") {
					st = "completed"
				}
			}
			td.Status = normStatus(st)
		}
		if td.Content != "" {
			list = append(list, td)
		}
	}
	t.mu.Lock()
	t.Todos = list
	t.mu.Unlock()
	var b strings.Builder
	b.WriteString("Todo list updated:\n")
	done := 0
	for _, td := range list {
		mark := "[ ]"
		switch td.Status {
		case "completed":
			mark = "[x]"
			done++
		case "in_progress":
			mark = "[~]"
		}
		fmt.Fprintf(&b, "%s %s\n", mark, td.Content)
	}
	return ToolOutput{Text: b.String(), Display: Display{Summary: fmt.Sprintf("%d/%d feitos", done, len(list)), Todos: list}}
}

func normStatus(s string) string {
	switch strings.ToLower(strings.NewReplacer("-", "_", " ", "_").Replace(strings.TrimSpace(s))) {
	case "completed", "complete", "done", "finished", "x", "ok":
		return "completed"
	case "in_progress", "inprogress", "active", "doing", "current", "started", "working", "progress":
		return "in_progress"
	}
	return "pending"
}

// ---------- small helpers ----------

func lookPath(bin string) (string, error) { return exec.LookPath(bin) }

func errText(err error) string {
	if pe, ok := err.(*os.PathError); ok {
		return pe.Err.Error()
	}
	return err.Error()
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, ". "); i > 0 && i < 140 {
		return s[:i]
	}
	if len(s) > 140 {
		return s[:140] + "…"
	}
	return s
}
