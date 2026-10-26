package main

// diffguard.go: "revisão do próprio diff", a code-review gate that runs at the
// end of every turn, like a linter before "pronto". It looks only at what jazz
// changed in the turn (write/edit snapshots plus the shadow-git checkpoint of
// bash changes, tools.go and checkpoint.go), runs a rule engine over the added
// lines and asks the user before accepting a risky change. Deny puts the
// flagged files back to how they were when the turn began; a headless run does
// the same and ends with StopDenied (exit 5) and .jazz/scan-findings.json.
//
// Rules are data (diffrules.json, embedded): id, severity, globs, regex,
// message_pt, why. Packs in ~/.config/jazz-cli/diffrules.d/*.json and
// <project>/.jazz/diffrules.d/*.json add or override rules by id; a project
// pack only loads when the user trusted its exact content (trust.go, kind
// "diffrules"). SECURITY.md documents the schema. The same engine runs on
// demand in `jazz scan` (diffguard_cmd.go) and can call semgrep when it is
// installed (diffguard_semgrep.go).
//
// "sempre" answers go to .jazz/scan-allow.json, keyed by a hash of (rule,
// file, line text) so the same line is never asked twice. Like approvals.json
// the file only counts when jazz wrote it (trust kind "scan-allow") and the
// agent's own tools cannot write it (safety.go).

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

//go:embed diffrules.json
var dgBuiltinRules []byte

const (
	dgTrustKind = "diffrules"
	dgAllowKind = "scan-allow"
	dgAllowFile = "scan-allow.json"
	dgFindFile  = "scan-findings.json"
	dgMaxFile   = 1 << 20 // files over 1 MB are not reviewed
)

// dgSev ranks severities; unknown words are rejected when a rule loads.
var dgSev = map[string]int{"INFO": 1, "LOW": 2, "MEDIUM": 3, "HIGH": 4, "CRITICAL": 5}

// dgRule is one entry of diffrules.json (or of a pack).
type dgRule struct {
	ID        string   `json:"id"`
	Severity  string   `json:"severity"`
	Globs     []string `json:"globs,omitempty"`   // empty = every file
	Exclude   []string `json:"exclude,omitempty"` // files the rule skips
	Scope     string   `json:"scope,omitempty"`   // "" | project | outside
	Regex     string   `json:"regex,omitempty"`   // RE2, matched against each added line
	Detector  string   `json:"detector,omitempty"`
	MessagePT string   `json:"message_pt"`
	Why       string   `json:"why,omitempty"`
	Disabled  bool     `json:"disabled,omitempty"` // in a pack: switches off the rule with this id

	re    *regexp.Regexp
	globs []*regexp.Regexp
	excl  []*regexp.Regexp
	pack  string
}

// dgPack describes one loaded (or refused) rule file, for `jazz scan --rules`.
type dgPack struct {
	Name   string // file path, or "embutido"
	Origin string // builtin | user | project
	Rules  int
	Note   string // why it was ignored, or load warnings
}

type dgEngine struct {
	rules []*dgRule
	packs []dgPack
}

// dgFile is one changed file: Old is the text before the turn ("" for a new
// file), New the text now. Outside marks a path outside the project.
type dgFile struct {
	Path    string
	Outside bool
	Old     string
	New     string
	Deleted bool
}

// dgFinding is one hit. Hash is stable across line moves (rule, file, text).
type dgFinding struct {
	Rule     string   `json:"rule"`
	Severity string   `json:"severity"`
	File     string   `json:"file"`
	Line     int      `json:"line"`
	Hash     string   `json:"hash"`
	Message  string   `json:"message"`
	Why      string   `json:"why,omitempty"`
	Text     string   `json:"text,omitempty"`
	Hunk     []string `json:"-"`
}

func (f dgFinding) rank() int { return dgSev[f.Severity] }

// ---------- globs ----------

// dgGlob compiles a path glob: * and ? stay inside a segment, ** crosses
// segments, a pattern without "/" matches the file name anywhere.
func dgGlob(p string) (*regexp.Regexp, error) {
	p = filepath.ToSlash(strings.TrimSpace(p))
	if p == "" {
		return nil, fmt.Errorf("glob vazio")
	}
	var b strings.Builder
	b.WriteString("^")
	if !strings.Contains(p, "/") {
		b.WriteString("(?:.*/)?")
	}
	for i := 0; i < len(p); i++ {
		switch c := p[i]; c {
		case '*':
			if i+1 < len(p) && p[i+1] == '*' {
				i++
				if i+1 < len(p) && p[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

func dgAnyMatch(res []*regexp.Regexp, path string) bool {
	for _, re := range res {
		if re.MatchString(path) {
			return true
		}
	}
	return false
}

// ---------- loading ----------

// dgCompile validates and prepares a rule; the error says why it was refused.
func dgCompile(r *dgRule) error {
	r.ID = strings.TrimSpace(r.ID)
	r.Severity = strings.ToUpper(strings.TrimSpace(r.Severity))
	if r.ID == "" {
		return fmt.Errorf("regra sem id")
	}
	if r.Disabled {
		return nil
	}
	if dgSev[r.Severity] == 0 {
		return fmt.Errorf("%s: severity deve ser CRITICAL, HIGH, MEDIUM, LOW ou INFO", r.ID)
	}
	switch r.Scope {
	case "", "project", "outside":
	default:
		return fmt.Errorf("%s: scope deve ser project ou outside", r.ID)
	}
	if r.Regex != "" {
		re, err := regexp.Compile(r.Regex)
		if err != nil {
			return fmt.Errorf("%s: regex inválida: %v", r.ID, err)
		}
		r.re = re
	}
	if r.Detector != "" && dgDetectors[r.Detector] == nil {
		return fmt.Errorf("%s: detector desconhecido %q", r.ID, r.Detector)
	}
	if r.re != nil && r.Detector != "" {
		return fmt.Errorf("%s: use regex ou detector, não os dois", r.ID)
	}
	for _, g := range r.Globs {
		re, err := dgGlob(g)
		if err != nil {
			return fmt.Errorf("%s: glob %q: %v", r.ID, g, err)
		}
		r.globs = append(r.globs, re)
	}
	for _, g := range r.Exclude {
		re, err := dgGlob(g)
		if err != nil {
			return fmt.Errorf("%s: exclude %q: %v", r.ID, g, err)
		}
		r.excl = append(r.excl, re)
	}
	if strings.TrimSpace(r.MessagePT) == "" {
		return fmt.Errorf("%s: falta message_pt", r.ID)
	}
	return nil
}

// dgParsePack reads a rule file: {"rules":[...]} or a bare array.
func dgParsePack(b []byte) ([]*dgRule, error) {
	var wrap struct {
		Rules []*dgRule `json:"rules"`
	}
	if err := json.Unmarshal(b, &wrap); err == nil && wrap.Rules != nil {
		return wrap.Rules, nil
	}
	var list []*dgRule
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("JSON inválido: %v", err)
	}
	return list, nil
}

// dgAdd merges rules into the engine: a later pack overrides an earlier rule
// with the same id; "disabled": true removes it. It returns load warnings.
func (e *dgEngine) add(rules []*dgRule, pack string) (n int, warns []string) {
	for _, r := range rules {
		if r == nil {
			continue
		}
		if err := dgCompile(r); err != nil {
			warns = append(warns, err.Error())
			continue
		}
		r.pack = pack
		kept := e.rules[:0]
		for _, old := range e.rules {
			if old.ID != r.ID {
				kept = append(kept, old)
			}
		}
		e.rules = kept
		if !r.Disabled {
			e.rules = append(e.rules, r)
		}
		n++
	}
	return n, warns
}

func dgPackFiles(dir string) []string {
	ents, _ := os.ReadDir(dir)
	var out []string
	for _, en := range ents {
		if !en.IsDir() && strings.HasSuffix(en.Name(), ".json") {
			out = append(out, filepath.Join(dir, en.Name()))
		}
	}
	sort.Strings(out)
	return out
}

func dgReadPack(path string) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > 512<<10 {
		return nil, fmt.Errorf("arquivo grande demais ou não é comum")
	}
	return os.ReadFile(path)
}

// dgProjectPackDir is where project packs live.
func dgProjectPackDir(root string) string { return filepath.Join(root, ".jazz", "diffrules.d") }

// dgProjectBlob is what the trust store hashes for project packs ("" = none).
func dgProjectBlob(root string) []byte {
	if root == "" {
		return nil
	}
	var b strings.Builder
	for _, p := range dgPackFiles(dgProjectPackDir(root)) {
		data, err := dgReadPack(p)
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "== %s\n", filepath.Base(p))
		b.Write(data)
		b.WriteString("\n")
	}
	return []byte(b.String())
}

// dgTrustSummary lists the project packs for the trust prompt.
func dgTrustSummary(root string) string {
	var b strings.Builder
	b.WriteString("  regras da revisão do diff (.jazz/diffrules.d):\n")
	for _, p := range dgPackFiles(dgProjectPackDir(root)) {
		data, err := dgReadPack(p)
		if err != nil {
			continue
		}
		rules, perr := dgParsePack(data)
		note := fmt.Sprintf("%d regra(s)", len(rules))
		if perr != nil {
			note = "inválido, será ignorado"
		}
		for _, r := range rules {
			if r != nil && r.Disabled {
				note += ", desliga " + visibleDetail(r.ID)
			}
		}
		fmt.Fprintf(&b, "    %s: %s\n", visibleDetail(filepath.Base(p)), note)
	}
	return b.String()
}

// dgLoadEngine loads the embedded rules, the user packs and (when trusted)
// the project packs, in that order of precedence.
func dgLoadEngine(root string) *dgEngine {
	e := &dgEngine{}
	rules, err := dgParsePack(dgBuiltinRules)
	if err != nil {
		panic("diffrules.json: " + err.Error()) // caught by TestDiffGuardBuiltinRules
	}
	n, warns := e.add(rules, "embutido")
	e.packs = append(e.packs, dgPack{Name: "embutido", Origin: "builtin", Rules: n, Note: strings.Join(warns, "; ")})
	load := func(dir, origin string) {
		for _, p := range dgPackFiles(dir) {
			pk := dgPack{Name: p, Origin: origin}
			data, err := dgReadPack(p)
			if err != nil {
				pk.Note = err.Error()
			} else if rs, err := dgParsePack(data); err != nil {
				pk.Note = err.Error()
			} else {
				var w []string
				pk.Rules, w = e.add(rs, p)
				pk.Note = strings.Join(w, "; ")
			}
			e.packs = append(e.packs, pk)
		}
	}
	load(filepath.Join(configDir(), "diffrules.d"), "user")
	if root != "" {
		if blob := dgProjectBlob(root); len(blob) == 0 || IsTrusted(root, dgTrustKind, blob) {
			load(dgProjectPackDir(root), "project")
		} else {
			for _, p := range dgPackFiles(dgProjectPackDir(root)) {
				e.packs = append(e.packs, dgPack{Name: p, Origin: "project", Note: "ignorado: não confiável (jazz trust)"})
			}
		}
	}
	return e
}

// ---------- scanning ----------

func dgHash(rule, file, text string) string {
	h := sha256.Sum256([]byte(rule + "\x00" + file + "\x00" + strings.TrimSpace(text)))
	return hex.EncodeToString(h[:12])
}

type dgLine struct {
	N    int
	Text string
}

// dgAdded lists the lines of f that are new (qgAddedLines, quality.go).
func dgAdded(f dgFile) []dgLine {
	if f.Deleted {
		return nil
	}
	set := qgAddedLines(f.Old, f.New)
	lines := strings.Split(f.New, "\n")
	var out []dgLine
	for i, l := range lines {
		if set[i+1] {
			out = append(out, dgLine{i + 1, strings.TrimRight(l, "\r")})
		}
	}
	return out
}

func (r *dgRule) applies(f dgFile) bool {
	switch r.Scope {
	case "outside":
		if !f.Outside {
			return false
		}
	case "project":
		if f.Outside {
			return false
		}
	}
	p := filepath.ToSlash(f.Path)
	if len(r.globs) > 0 && !dgAnyMatch(r.globs, p) {
		return false
	}
	return !dgAnyMatch(r.excl, p)
}

// dgShow is a finding's text as the user sees it: secrets redacted, escapes
// and bidi characters made visible.
func dgShow(s string) string {
	s = oneLine(visibleDetail(redactSecrets(strings.TrimSpace(s))), 200)
	return strings.Map(func(r rune) rune {
		if r >= 0x200B && r <= 0x200F || r >= 0x2060 && r <= 0x2064 || r == 0xFEFF || r >= 0xE0000 && r <= 0xE007F {
			return '·'
		}
		return r
	}, s)
}

// scan runs every rule over one file. The result is ordered by severity.
func (e *dgEngine) scan(f dgFile) []dgFinding {
	added := dgAdded(f)
	var out []dgFinding
	for _, r := range e.rules {
		if !r.applies(f) {
			continue
		}
		msg := r.MessagePT
		mk := func(line int, text string, extra string) dgFinding {
			m := msg
			if extra != "" {
				m += " (" + extra + ")"
			}
			return dgFinding{Rule: r.ID, Severity: r.Severity, File: f.Path, Line: line, Hash: dgHash(r.ID, f.Path, text), Message: m, Why: r.Why, Text: dgShow(text)}
		}
		if r.re == nil && r.Detector == "" { // a file-level rule: any change counts
			if f.Old == f.New && !f.Deleted {
				continue
			}
			fd := mk(0, "", "")
			// A file-level approval applies to this exact content, including
			// deletions; it must not authorize every later CI change.
			fd.Hash = dgHash(r.ID, f.Path, fmt.Sprintf("%t\x00%s", f.Deleted, f.New))
			if len(added) > 0 {
				fd.Line = added[0].N
			}
			for i, l := range added {
				if i >= 15 {
					fd.Hunk = append(fd.Hunk, fmt.Sprintf("+ … +%d linhas", len(added)-i))
					break
				}
				fd.Hunk = append(fd.Hunk, fmt.Sprintf("+ %d: %s", l.N, dgShow(l.Text)))
			}
			if f.Deleted {
				fd.Hunk = []string{"- arquivo removido"}
			}
			out = append(out, fd)
			continue
		}
		hits := 0
		for _, l := range added {
			text := l.Text
			hit, extra := false, ""
			if r.re != nil {
				hit = r.re.MatchString(text)
			} else {
				hit, extra = dgDetectors[r.Detector](f, text)
			}
			if !hit {
				continue
			}
			fd := mk(l.N, l.Text, extra)
			fd.Hunk = []string{fmt.Sprintf("+ %d: %s", l.N, dgShow(l.Text))}
			out = append(out, fd)
			if hits++; hits >= 5 {
				break
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].rank() > out[j].rank() })
	return out
}

// dgScanAll scans every file and drops what the allowlist already approved.
func (e *dgEngine) scanAll(files []dgFile, allow *apStore) (found []dgFinding, allowed int) {
	for _, f := range files {
		for _, fd := range e.scan(f) {
			if allow.Has("diffguard", fd.Hash) {
				allowed++
				continue
			}
			found = append(found, fd)
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].rank() > found[j].rank() })
	return found, allowed
}

// ---------- detectors (built-in checks reused by the rules) ----------

var dgDetectors = map[string]func(f dgFile, line string) (bool, string){
	"unicode":   dgDetUnicode,
	"secret":    dgDetSecret,
	"registry":  dgDetRegistry,
	"typosquat": dgDetTyposquat,
}

// dgDetUnicode: invisible, zero-width or bidi characters (sanitize.go).
func dgDetUnicode(_ dgFile, line string) (bool, string) {
	_, rep := sanitizeForModel(line, "strip")
	if rep.Invisible > 0 || rep.Bidi > 0 {
		return true, ""
	}
	return false, ""
}

// dgDetSecret: the redaction detector (safety.go redactSecrets) changes the
// line, so it holds a token shape or a literal credential.
func dgDetSecret(_ dgFile, line string) (bool, string) {
	return redactSecrets(line) != line, ""
}

var (
	dgRegistryLine = lazyRx(`(?i)(registry|index[-_]url|^\s*url\s*=|^\s*source\s)`)
	dgURLHost      = lazyRx(`(?i)\bhttps?://(?:[^/@\s"']*@)?([^/:\s"'?#]+)`)
	dgRegistryOK   = map[string]bool{"registry.npmjs.org": true, "registry.yarnpkg.com": true, "pypi.org": true, "pypi.python.org": true,
		"files.pythonhosted.org": true, "crates.io": true, "index.crates.io": true, "static.crates.io": true, "rubygems.org": true,
		"packagist.org": true, "repo.packagist.org": true, "proxy.golang.org": true, "repo.maven.apache.org": true,
		"repo1.maven.org": true, "localhost": true, "127.0.0.1": true}
)

// dgDetRegistry: a registry or index URL that is not an official public one.
func dgDetRegistry(_ dgFile, line string) (bool, string) {
	if !dgRegistryLine.MatchString(line) {
		return false, ""
	}
	for _, m := range dgURLHost.FindAllStringSubmatch(line, -1) {
		if h := strings.ToLower(m[1]); !dgRegistryOK[h] {
			return true, h
		}
	}
	return false, ""
}

var (
	dgNpmDep      = lazyRx(`^\s*"(@?[a-z0-9][a-z0-9._~/-]*)"\s*:\s*"([^"]*)"\s*,?\s*$`)
	dgNpmVer      = lazyRx(`^([\^~<>=*]|\d|latest$|[a-z]+:)`)
	dgNpmSkip     = map[string]bool{"name": true, "version": true, "description": true, "main": true, "license": true, "type": true, "author": true, "homepage": true, "module": true, "types": true, "typings": true, "node": true, "npm": true, "yarn": true, "pnpm": true, "private": true}
	dgPyReq       = lazyRx(`^\s*([A-Za-z0-9][A-Za-z0-9._-]*)\s*(?:\[[^\]]*\])?\s*(?:==|>=|<=|~=|!=|<|>|;|$)`)
	dgTomlDep     = lazyRx(`^\s*"?([A-Za-z0-9][A-Za-z0-9_.-]*)"?\s*=\s*(?:"[\^~*<>=0-9][^"]*"|\{[^}]*\b(?:version|git|path)\s*=)`)
	dgTomlSkip    = map[string]bool{"name": true, "version": true, "edition": true, "authors": true, "description": true, "license": true, "readme": true, "repository": true, "homepage": true, "rust-version": true, "requires-python": true, "python_version": true, "python": true, "url": true}
	dgGoReq       = lazyRx(`^\s*(?:require\s+)?([a-z0-9][^\s]*\.[^\s/]*/[^\s]+)\s+v[0-9]`)
	dgGemDep      = lazyRx(`^\s*gem\s+['"]([^'"]+)['"]`)
	dgComposerDep = lazyRx(`^\s*"([a-z0-9._-]+/[a-z0-9._-]+)"\s*:\s*"`)
)

// dgDepName pulls the package name out of an added manifest line.
func dgDepName(base, line string) (eco, name string) {
	pick := func(re *regexp.Regexp, e string) (string, string) {
		if m := re.FindStringSubmatch(line); m != nil {
			return e, m[1]
		}
		return "", ""
	}
	switch {
	case base == "package.json":
		if m := dgNpmDep.FindStringSubmatch(line); m != nil && !dgNpmSkip[m[1]] && dgNpmVer.MatchString(m[2]) {
			return "npm", m[1]
		}
	case strings.HasPrefix(base, "requirements") && strings.HasSuffix(base, ".txt") || strings.HasPrefix(base, "constraints"):
		if t := strings.TrimSpace(line); t != "" && t[0] != '#' && t[0] != '-' && !strings.Contains(t, "://") {
			return pick(dgPyReq.R(), "pypi")
		}
	case base == "Pipfile":
		if e, n := pick(dgTomlDep.R(), "pypi"); n != "" && !dgTomlSkip[n] {
			return e, n
		}
	case base == "Cargo.toml":
		if e, n := pick(dgTomlDep.R(), "crates"); n != "" && !dgTomlSkip[n] {
			return e, n
		}
	case base == "go.mod":
		return pick(dgGoReq.R(), "go")
	case base == "Gemfile":
		return pick(dgGemDep.R(), "gems")
	case base == "composer.json":
		return pick(dgComposerDep.R(), "composer")
	}
	return "", ""
}

// dgDetTyposquat: a new dependency whose name is one typo away from a
// popular package (sgTyposquat, guardrails.go; name only, no network).
func dgDetTyposquat(f dgFile, line string) (bool, string) {
	eco, name := dgDepName(filepath.Base(filepath.ToSlash(f.Path)), line)
	if name == "" {
		return false, ""
	}
	if near := sgTyposquat(sgPkg{Eco: eco, Name: name}); near != "" {
		return true, name + " parece com " + near
	}
	return false, ""
}

// ---------- config ----------

// dgMode is on (default) | off | strict (MEDIUM findings ask too).
func dgMode(cfg *Config) string {
	v := ""
	if cfg != nil {
		v = strings.ToLower(strings.TrimSpace(cfg.DiffGuard))
		if r := strings.ToLower(strings.TrimSpace(cfg.DiffGuardRun)); r != "" {
			v = r
		}
	}
	if e := strings.ToLower(strings.TrimSpace(os.Getenv("JAZZ_DIFF_GUARD"))); e != "" {
		v = e
	}
	switch v {
	case "off", "0", "false", "no":
		return "off"
	case "strict":
		return "strict"
	}
	return "on"
}

// ---------- the turn diff ----------

// Show returns the content of rel in tree (the checkpoint at turn start).
func (c *cpStore) Show(tree, rel string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.ready || tree == "" {
		return "", false
	}
	out, err := c.cmd(nil, nil, "cat-file", "blob", tree+":"+rel)
	if err != nil {
		return "", false
	}
	return string(out), true
}

// dgSkipPath: places jazz and git own; never reviewed.
func dgSkipPath(rel string) bool {
	rel = filepath.ToSlash(rel)
	for _, p := range []string{".git/", ".jazz/", "node_modules/"} {
		if strings.HasPrefix(rel, p) || strings.Contains(rel, "/"+p) {
			return true
		}
	}
	return false
}

// dgTurnFiles lists the files changed in the current turn with their text
// before (write/edit snapshot, or the turn-start checkpoint for bash
// changes) and now.
func (t *Toolbox) dgTurnFiles() []dgFile {
	t.mu.Lock()
	if len(t.turns) == 0 {
		t.mu.Unlock()
		return nil
	}
	ts := t.turns[len(t.turns)-1]
	paths := map[string]*snapshot{}
	for p, s := range ts.files {
		paths[p] = s
	}
	for p := range ts.bash {
		if _, ok := paths[p]; !ok {
			paths[p] = nil
		}
	}
	cp := t.cp
	t.mu.Unlock()
	var names []string
	for p := range paths {
		names = append(names, p)
	}
	sort.Strings(names)
	var out []dgFile
	for _, p := range names {
		s := paths[p]
		rel, err := filepath.Rel(t.Root, p)
		outside := err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
		display := filepath.ToSlash(rel)
		if outside {
			display = filepath.ToSlash(p)
		} else if dgSkipPath(rel) {
			continue
		}
		f := dgFile{Path: display, Outside: outside}
		fi, err := os.Lstat(p)
		switch {
		case err != nil:
			f.Deleted = true
		case !fi.Mode().IsRegular() || fi.Size() > dgMaxFile:
			continue
		default:
			b, err := os.ReadFile(p)
			if err != nil || strings.IndexByte(string(b[:min(len(b), 8000)]), 0) >= 0 {
				continue
			}
			f.New = string(b)
		}
		switch {
		case s != nil && !s.afterBash:
			if s.existed {
				f.Old = string(s.data)
			}
		case cp != nil && ts.cpStart != "" && !outside:
			f.Old, _ = cp.Show(ts.cpStart, filepath.ToSlash(rel))
		}
		if f.Old == f.New {
			continue
		}
		out = append(out, f)
	}
	return out
}

// dgRestore puts the given display paths back to how the turn found them.
// It returns what was restored and what could not be.
func (t *Toolbox) dgRestore(paths []string) (done, failed []string) {
	t.mu.Lock()
	if len(t.turns) == 0 {
		t.mu.Unlock()
		return nil, paths
	}
	ts := t.turns[len(t.turns)-1]
	cp := t.cp
	t.mu.Unlock()
	for _, d := range paths {
		abs := d
		if !filepath.IsAbs(filepath.FromSlash(d)) {
			abs = filepath.Join(t.Root, filepath.FromSlash(d))
		}
		abs = filepath.FromSlash(abs)
		var err error
		if s := ts.files[abs]; s != nil && !s.afterBash {
			if s.existed {
				err = restoreFile(abs, s.data, s.mode, t.Root)
			} else if pathUnder(filepath.Clean(abs), filepath.Clean(t.Root)) && !cpParentInside(t.Root, abs) {
				err = fmt.Errorf("pasta fora do projeto")
			} else {
				err = os.Remove(abs)
			}
		} else if rel, rerr := filepath.Rel(t.Root, abs); cp != nil && ts.cpStart != "" && rerr == nil && !strings.HasPrefix(rel, "..") {
			err = cp.Restore(ts.cpStart, []string{filepath.ToSlash(rel)})
		} else {
			err = fmt.Errorf("sem cópia do início do turno")
		}
		if err != nil {
			failed = append(failed, d)
			continue
		}
		t.mu.Lock()
		delete(t.readState, abs)
		t.mu.Unlock()
		done = append(done, d)
	}
	return done, failed
}

// ---------- the review ----------

func (a *Agent) dgAllow() *apStore {
	a.apMu.Lock()
	defer a.apMu.Unlock()
	if a.dgAl == nil {
		a.dgAl = newAPStoreFile(a.tools.Root, dgAllowFile, dgAllowKind)
	}
	return a.dgAl
}

// dgReview is the turn-end gate (called from the defer in Run). It returns
// StopDenied when the changes were refused, "" otherwise.
func (a *Agent) dgReview(ctx context.Context) StopReason {
	if a.sub || a.tools == nil || a.cfg == nil {
		return ""
	}
	mode := dgMode(a.cfg)
	if mode == "off" {
		return ""
	}
	files := a.tools.dgTurnFiles()
	if len(files) == 0 {
		return ""
	}
	eng := dgLoadEngine(a.tools.Root)
	found, _ := eng.scanAll(files, a.dgAllow())
	found = append(found, dgDropAllowed(a.dgSemgrepFindings(ctx, files), a.dgAllow())...)
	sort.SliceStable(found, func(i, j int) bool { return found[i].rank() > found[j].rank() })
	if len(found) == 0 {
		return ""
	}
	minRank := dgSev["HIGH"]
	if mode == "strict" {
		minRank = dgSev["MEDIUM"]
	}
	var gate, soft []dgFinding
	for _, f := range found {
		auditRec("diffguard.finding", map[string]any{"rule": f.Rule, "file": f.File, "line": f.Line, "hash": f.Hash}) // audit.go
		if f.rank() >= minRank {
			gate = append(gate, f)
		} else {
			soft = append(soft, f)
		}
	}
	if len(soft) > 0 {
		a.ui.Notice("revisão do diff: " + dgBrief(soft, 4))
	}
	if len(gate) == 0 {
		return ""
	}
	flagged := dgFlaggedFiles(gate)
	reason := fmt.Sprintf("revisão do diff: %d achado(s) de risco neste turno", len(gate))
	detail := dgBrief(gate, 3)
	if ctx.Err() != nil && !a.Headless {
		a.ui.Warn(reason + " (turno cancelado, nada foi restaurado): " + detail)
		return ""
	}
	if a.Headless {
		restored, failed := a.tools.dgRestore(flagged)
		dgWriteFindings(a.tools.Root, gate, restored, failed)
		a.obApproval("diffguard", detail, reason, Deny)
		a.ui.Warn(reason + "; sem quem aprove, restaurei " + dgList(restored) + " (achados em .jazz/" + dgFindFile + ")")
		return StopDenied
	}
	if h, ok := a.ui.(approveExtra); ok {
		h.ApproveExtra(dgHunkText(gate))
	}
	if h, ok := a.ui.(approveAlwaysOffer); ok {
		h.ApproveAlways(dgList(flagged))
	}
	ap, _ := a.ui.Approve(ctx, "diffguard", detail, reason)
	a.obApproval("diffguard", detail, reason, ap)
	switch ap {
	case Allow:
		return ""
	case AllowAlways:
		store := a.dgAllow()
		for _, f := range gate {
			if err := store.Add("diffguard", f.Hash, f.Rule+" "+f.File); err != nil {
				a.ui.Warn("não consegui salvar a permissão: " + err.Error())
				break
			}
		}
		a.ui.Notice("não pergunto de novo por estas linhas neste projeto (.jazz/" + dgAllowFile + ")")
		return ""
	}
	restored, failed := a.tools.dgRestore(flagged)
	msg := "mudanças recusadas na revisão do diff; restaurei " + dgList(restored)
	if len(failed) > 0 {
		msg += " (não consegui restaurar " + dgList(failed) + ")"
	}
	a.ui.Warn(msg)
	a.pendingNote = append(a.pendingNote, "<system-reminder>The user rejected the diff review at the end of the last turn and the files "+dgList(restored)+" were restored to their previous content. Do not redo those changes without asking.</system-reminder>")
	return StopDenied
}

func dgDropAllowed(in []dgFinding, allow *apStore) []dgFinding {
	var out []dgFinding
	for _, f := range in {
		if !allow.Has("diffguard", f.Hash) {
			out = append(out, f)
		}
	}
	return out
}

func dgFlaggedFiles(fs []dgFinding) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range fs {
		if !seen[f.File] {
			seen[f.File] = true
			out = append(out, f.File)
		}
	}
	return out
}

func dgList(paths []string) string {
	if len(paths) == 0 {
		return "nenhum arquivo"
	}
	if len(paths) > 4 {
		return strings.Join(paths[:4], ", ") + fmt.Sprintf(" +%d", len(paths)-4)
	}
	return strings.Join(paths, ", ")
}

// dgBrief is a one-line summary: "jz-tls-off em app.py:3, ...".
func dgBrief(fs []dgFinding, max int) string {
	var parts []string
	for i, f := range fs {
		if i >= max {
			parts = append(parts, fmt.Sprintf("+%d", len(fs)-max))
			break
		}
		loc := f.File
		if f.Line > 0 {
			loc += ":" + itoa(f.Line)
		}
		parts = append(parts, f.Severity+" "+f.Rule+" em "+loc)
	}
	return strings.Join(parts, "; ")
}

// dgHunkText is what "v" shows: each finding with why and the added lines.
func dgHunkText(fs []dgFinding) string {
	var b strings.Builder
	for i, f := range fs {
		if i > 0 {
			b.WriteString("\n")
		}
		loc := f.File
		if f.Line > 0 {
			loc += ":" + itoa(f.Line)
		}
		fmt.Fprintf(&b, "%s  %s  %s\n  %s\n", f.Severity, f.Rule, loc, f.Message)
		if f.Why != "" {
			fmt.Fprintf(&b, "  por quê: %s\n", f.Why)
		}
		for _, l := range f.Hunk {
			b.WriteString("  " + l + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

type dgFindingsFile struct {
	Version  int         `json:"version"`
	At       time.Time   `json:"at"`
	Stop     string      `json:"stop"`
	Restored []string    `json:"restored,omitempty"`
	Failed   []string    `json:"not_restored,omitempty"`
	Findings []dgFinding `json:"findings"`
}

// dgWriteFindings writes .jazz/scan-findings.json for a run nobody approved.
func dgWriteFindings(root string, gate []dgFinding, restored, failed []string) {
	if root == "" {
		return
	}
	b, err := json.MarshalIndent(dgFindingsFile{Version: 1, At: time.Now().UTC().Truncate(time.Second), Stop: string(StopDenied), Restored: restored, Failed: failed, Findings: gate}, "", "  ")
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Join(root, ".jazz"), 0o755) == nil {
		_ = writeFileAtomic(filepath.Join(root, ".jazz", dgFindFile), append(b, '\n'), 0o644)
	}
}
