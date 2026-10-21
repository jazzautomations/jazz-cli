package main

// steering.go: conditional steering rules (Kiro steering, Cursor rules).
//
// Sources, global first, project last (a project rule replaces a global one
// with the same name):
//   - configDir()/steering/*.md                         (global, always trusted)
//   - .jazz/steering/*.md and .kiro/steering/*.md       (front matter: inclusion,
//     fileMatchPattern, name, description)
//   - .cursor/rules/**/*.mdc                            (front matter: alwaysApply,
//     globs, description)
//
// Inclusion modes:
//   - always:    the body goes into the system prompt, frozen for the session.
//   - auto:      listed in the system prompt with its description, like skills;
//     the model reads it when the task matches.
//   - manual:    listed by name only; the model reads it when the user asks
//     (for example "#name").
//   - fileMatch: appended once per session as a <system-reminder> to the first
//     successful read/edit/write result whose path matches. The history is
//     append-only, so the prompt cache stays valid.
//
// Project rules are instructions that reach the model with system authority and
// can be injected in the middle of a turn, so they sit behind the project trust
// gate (kind "steering", sha256 of every project rule file) like .mcp.json and
// hooks. Symlinks and oversized files are skipped, so a hostile repo cannot
// point a rule at a secret elsewhere on the disk.

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	acSteerFileMax   = 64 << 10 // larger files are not steering rules
	acSteerBodyMax   = 8 << 10  // per rule, in the prompt or a reminder
	acSteerAlwaysMax = 16 << 10 // all "always" rules together
	acSteerMaxFiles  = 100
	acSteerListMax   = 40
)

// SteeringRule is one parsed steering file.
type SteeringRule struct {
	Name        string
	Path        string // absolute path
	Display     string // path shown to the model (relative for project files)
	Inclusion   string // always | auto | manual | fileMatch
	Description string
	Patterns    []string // fileMatch globs, relative to the project root
	Body        string   // without front matter, capped
	Project     bool
}

type acSteerFile struct {
	path, display string
	data          []byte
	cursor        bool
	project       bool
}

// acSteerScan lists the rule files of one directory (sorted, regular files only).
func acSteerScan(dir, ext string, recursive bool) []string {
	var out []string
	walk := func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != dir && !recursive {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() && strings.EqualFold(filepath.Ext(p), ext) && len(out) < acSteerMaxFiles {
			out = append(out, p)
		}
		return nil
	}
	_ = filepath.WalkDir(dir, walk)
	sort.Strings(out)
	return out
}

func acSteerRead(p string) ([]byte, bool) {
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > acSteerFileMax {
		return nil, false
	}
	b, err := os.ReadFile(p)
	return b, err == nil
}

// acProjectSteerFiles reads the project's rule files.
func acProjectSteerFiles(root string) []acSteerFile {
	var out []acSteerFile
	add := func(paths []string, cursor bool) {
		for _, p := range paths {
			if b, ok := acSteerRead(p); ok && pfInsideRoot(p, root) { // no symlinked rule dirs (F6)
				rel, err := filepath.Rel(root, p)
				if err != nil {
					rel = p
				}
				out = append(out, acSteerFile{path: p, display: filepath.ToSlash(rel), data: b, cursor: cursor, project: true})
			}
		}
	}
	add(acSteerScan(filepath.Join(root, ".jazz", "steering"), ".md", false), false)
	add(acSteerScan(filepath.Join(root, ".kiro", "steering"), ".md", false), false)
	add(acSteerScan(filepath.Join(root, ".cursor", "rules"), ".mdc", true), true)
	return out
}

// acProjectSteeringBlob is what the trust gate hashes (kind "steering"):
// every project rule file, path and content. nil = no project rules.
func acProjectSteeringBlob(root string) []byte {
	return acSteerBlob(acProjectSteerFiles(root))
}

func acSteerBlob(files []acSteerFile) []byte {
	if len(files) == 0 {
		return nil
	}
	var b []byte
	for _, f := range files {
		b = append(b, f.display...)
		b = append(b, 0)
		b = append(b, f.data...)
		b = append(b, 0)
	}
	return b
}

// acProjectSteeringTrusted reports whether project rules may be used.
func acProjectSteeringTrusted(root string) bool {
	return IsTrusted(root, "steering", acProjectSteeringBlob(root))
}

// acLoadSteering returns the global rules plus, when the project is trusted,
// the project rules. A project rule replaces a global rule with the same name.
func acLoadSteering(root string) []SteeringRule {
	var files []acSteerFile
	for _, p := range acSteerScan(filepath.Join(configDir(), "steering"), ".md", false) {
		if b, ok := acSteerRead(p); ok {
			files = append(files, acSteerFile{path: p, display: p, data: b})
		}
	}
	if proj := acProjectSteerFiles(root); len(proj) > 0 && IsTrusted(root, "steering", acSteerBlob(proj)) {
		files = append(files, proj...)
	}
	var out []SteeringRule
	idx := map[string]int{}
	for _, f := range files {
		r := acParseSteering(f.path, string(f.data), f.cursor)
		r.Display, r.Project = f.display, f.project
		if i, ok := idx[r.Name]; ok {
			out[i] = r
			continue
		}
		idx[r.Name] = len(out)
		out = append(out, r)
	}
	return out
}

// acParseSteering parses one rule file. cursor selects Cursor .mdc semantics.
func acParseSteering(path, s string, cursor bool) SteeringRule {
	scal, lists, body := acSteerFrontMatter(s)
	r := SteeringRule{Path: path, Description: scal["description"]}
	r.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if n := scal["name"]; n != "" && !cursor {
		r.Name = n
	}
	pats := func(key string) []string {
		if l, ok := lists[key]; ok {
			return l
		}
		return acSteerSplit(scal[key])
	}
	if cursor {
		r.Patterns = pats("globs")
		switch {
		case strings.EqualFold(scal["alwaysapply"], "true"):
			r.Inclusion = "always"
		case len(r.Patterns) > 0:
			r.Inclusion = "fileMatch"
		case r.Description != "":
			r.Inclusion = "auto"
		default:
			r.Inclusion = "manual"
		}
	} else {
		r.Patterns = pats("filematchpattern")
		switch strings.ToLower(scal["inclusion"]) {
		case "", "always":
			r.Inclusion = "always"
		case "filematch":
			r.Inclusion = "fileMatch"
		case "auto":
			r.Inclusion = "auto"
		default: // manual, or a typo: never load it on its own
			r.Inclusion = "manual"
		}
		if r.Inclusion == "fileMatch" && len(r.Patterns) == 0 {
			r.Inclusion = "manual"
		}
	}
	if r.Inclusion != "fileMatch" {
		r.Patterns = nil
	}
	body = strings.TrimSpace(body)
	if len(body) > acSteerBodyMax {
		body = strings.ToValidUTF8(body[:acSteerBodyMax], "") + "\n[… truncated]"
	}
	r.Body = body
	return r
}

// acSteerFrontMatter splits YAML-ish front matter into scalars and lists
// (keys lowercased). It handles "key: value", inline "[a, b]" lists and
// block lists ("key:" followed by "- item" lines). Without front matter the
// whole text is the body.
func acSteerFrontMatter(s string) (map[string]string, map[string][]string, string) {
	scal, lists := map[string]string{}, map[string][]string{}
	s = strings.TrimPrefix(strings.ReplaceAll(s, "\r\n", "\n"), string(rune(0xFEFF)))
	if !strings.HasPrefix(s, "---\n") {
		return scal, lists, s
	}
	rest := s[4:]
	end := strings.Index(rest, "\n---")
	var head string
	if strings.HasPrefix(rest, "---") {
		head, rest = "", rest[3:]
	} else if end < 0 {
		return scal, lists, s
	} else {
		head, rest = rest[:end], rest[end+4:]
	}
	if i := strings.IndexByte(rest, '\n'); i >= 0 {
		rest = rest[i+1:] // rest of the closing "---" line
	} else {
		rest = ""
	}
	key := ""
	for _, l := range strings.Split(head, "\n") {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if strings.HasPrefix(t, "- ") || t == "-" {
			if key != "" {
				if v := acSteerUnquote(strings.TrimSpace(strings.TrimPrefix(t, "-"))); v != "" {
					lists[key] = append(lists[key], v)
				}
			}
			continue
		}
		k, v, ok := strings.Cut(t, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		if strings.HasPrefix(v, "[") && strings.HasSuffix(v, "]") {
			lists[key] = acSteerSplit(v[1 : len(v)-1])
			continue
		}
		scal[key] = acSteerUnquote(v)
	}
	return scal, lists, rest
}

func acSteerUnquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		if v[0] == '"' {
			if u, err := strconv.Unquote(v); err == nil {
				return u
			}
		}
		return v[1 : len(v)-1]
	}
	return v
}

// acSteerSplit splits a comma-separated pattern list, keeping commas inside
// braces ("**/*.{ts,tsx}") and dropping quotes around each item.
func acSteerSplit(s string) []string {
	var out []string
	depth, start := 0, 0
	flush := func(end int) {
		if v := acSteerUnquote(strings.TrimSpace(s[start:end])); v != "" {
			out = append(out, v)
		}
	}
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				flush(i)
				start = i + 1
			}
		}
	}
	flush(len(s))
	return out
}

// acSteerBraces expands the first {a,b} group recursively ("*.{ts,tsx}" →
// "*.ts", "*.tsx"). Capped so a hostile pattern cannot blow up.
func acSteerBraces(p string) []string {
	out := []string{p}
	for round := 0; round < 4; round++ {
		var next []string
		changed := false
		for _, s := range out {
			i := strings.IndexByte(s, '{')
			j := -1
			if i >= 0 {
				j = strings.IndexByte(s[i:], '}')
			}
			if i < 0 || j < 0 {
				next = append(next, s)
				continue
			}
			changed = true
			for _, alt := range strings.Split(s[i+1:i+j], ",") {
				next = append(next, s[:i]+alt+s[i+j+1:])
			}
		}
		out = next
		if !changed || len(out) > 64 {
			break
		}
	}
	return out
}

// acSteeringMatch reports whether abs (absolute, cleaned) matches any of the
// rule's patterns. Patterns are relative to root; one without "/" matches the
// file name at any depth (gitignore style), and only files inside root match.
func acSteeringMatch(r SteeringRule, abs, root string) bool {
	for _, p := range r.Patterns {
		p = strings.TrimLeft(filepath.ToSlash(strings.TrimSpace(p)), "/")
		if p == "" {
			continue
		}
		for _, c := range acSteerBraces(p) {
			if pfMatchPath(c, abs, root, false) {
				return true
			}
		}
	}
	return false
}

// acSteeringBlock renders the system-prompt part: "always" bodies, then the
// list of auto/manual rules. "" when there is nothing to show.
func acSteeringBlock(rules []SteeringRule) string {
	var b strings.Builder
	total := 0
	var listed []SteeringRule
	fileMatch := false
	for _, r := range rules {
		switch r.Inclusion {
		case "always":
			if r.Body == "" || total+len(r.Body) > acSteerAlwaysMax {
				if r.Body != "" {
					listed = append(listed, SteeringRule{Name: r.Name, Display: r.Display, Inclusion: "manual", Description: "(too long to include; read it when relevant)"})
				}
				continue
			}
			total += len(r.Body)
			b.WriteString("<steering_rule path=" + strconv.Quote(r.Display) + ">\n" + r.Body + "\n</steering_rule>\n")
		case "auto", "manual":
			listed = append(listed, r)
		case "fileMatch":
			fileMatch = true
		}
	}
	if len(listed) > 0 || fileMatch {
		b.WriteString("<steering>\n")
		if len(listed) > 0 {
			b.WriteString("Steering files hold extra project guidance. Read one with the read tool before you start when the task matches its description (auto), or when the user names it, for example #name (manual).\n")
		}
		if fileMatch {
			b.WriteString("Rules for specific files are added to a tool result the first time you read or change a matching file; follow them.\n")
		}
		if len(listed) > acSteerListMax {
			listed = listed[:acSteerListMax]
		}
		for _, r := range listed {
			d := strings.Join(strings.Fields(r.Description), " ")
			if len(d) > 200 {
				d = strings.ToValidUTF8(d[:200], "") + "…"
			}
			if d != "" {
				d = ": " + d
			}
			b.WriteString("- " + r.Name + " (" + r.Inclusion + ")" + d + " (" + r.Display + ")\n")
		}
		b.WriteString("</steering>\n")
	}
	return b.String()
}

// acSteeringPrompt is the system-prompt part for root ("" = none).
// The lessons block (lessons.go) leads it: right after AGENTS.md, frozen
// for the session like every other "always" part.
func acSteeringPrompt(root string) string {
	return lsPromptBlock(root) + acSteeringBlock(acLoadSteering(root))
}

// acSteerMarker starts the reminder of rule r; it is also how an injection
// already in the history is recognised (resume, rewind and compaction safe).
func acSteerMarker(r SteeringRule) string {
	return "<system-reminder>Steering rule " + strconv.Quote(r.Display)
}

// acSteeringNote returns the fileMatch reminders to append to the result of
// tool call tc ("" = none). Each rule is injected once per conversation: the
// history itself is the record, so a rule whose reminder was compacted or
// rewound away is injected again the next time it matches.
func (a *Agent) acSteeringNote(tc ToolCall, out ToolOutput) string {
	if out.IsErr || a.tools == nil {
		return ""
	}
	switch CanonicalTool(tc.Name) {
	case "read", "edit", "write":
	default:
		return ""
	}
	if strings.HasPrefix(tc.Name, "mcp__") {
		return ""
	}
	p := strings.TrimSpace(argPath(tc.Args))
	if p == "" {
		return ""
	}
	if !a.steeringLoaded {
		for _, r := range acLoadSteering(a.tools.Root) {
			if r.Inclusion == "fileMatch" && r.Body != "" {
				a.fileRules = append(a.fileRules, r)
			}
		}
		a.steeringLoaded = true
	}
	if len(a.fileRules) == 0 {
		return ""
	}
	abs := a.tools.abs(p)
	var notes []string
	for _, r := range a.fileRules {
		if !acSteeringMatch(r, abs, a.tools.Root) {
			continue
		}
		mk := acSteerMarker(r)
		if a.acSteerInjected(mk) {
			continue
		}
		notes = append(notes, mk+" (applies to files matching "+strings.Join(r.Patterns, ", ")+"):\n"+r.Body+"\n</system-reminder>")
		a.ui.Notice("regra de steering aplicada: " + r.Name)
	}
	if len(notes) == 0 {
		return ""
	}
	return "\n\n" + strings.Join(notes, "\n")
}

// acSteerInjected reports whether a tool result in the history carries mk.
func (a *Agent) acSteerInjected(mk string) bool {
	for i := len(a.Msgs) - 1; i >= 0; i-- {
		if m := a.Msgs[i]; m.Role == "tool" && strings.Contains(m.Content, mk) {
			return true
		}
	}
	return false
}

// acSteeringTrustSummary describes the project rules for the trust prompt.
func acSteeringTrustSummary(root string) string {
	var b strings.Builder
	for _, f := range acProjectSteerFiles(root) {
		r := acParseSteering(f.path, string(f.data), f.cursor)
		what := r.Inclusion
		if len(r.Patterns) > 0 {
			what += " " + strings.Join(r.Patterns, ", ")
		}
		b.WriteString("  regra de steering (" + visibleDetail(what) + "): " + visibleDetail(f.display) + "\n")
	}
	return b.String()
}
