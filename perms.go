package main

// perms.go: persistent, pattern-based permission rules (G11). Owner: platform.
// Contract surface (CONTRACT.md): PermDecision, PermSet, LoadPerms, Check,
// Remember, Rules, Remove. All methods are nil-safe (nil = no rules).
//
// Rule syntax follows Claude Code settings.json:
//   Bash                  every bash command
//   Bash(npm test:*)      commands starting with "npm test" (word boundary)
//   Bash(git status)      exactly that command; * works as a wildcard
//   Edit(src/**)          edit and write under src/ (relative to the project root)
//   Write(*.md) Read(.env) a pattern without "/" matches the file name at any depth
//   Edit(/etc/**)         absolute; ~/ expands to $HOME
//   WebFetch(domain:go.dev)
//   mcp__github__*        MCP tools (glob); mcp__github = the whole server
// Compound bash commands (a && b; c | d) are split: deny/ask match when any
// segment matches, allow only when every segment is allowed.

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
)

// PermDecision is what the rules say about one tool call.
type PermDecision int

const (
	PermDefault PermDecision = iota // no rule matched: fall back to the mode
	PermAllow                       // skip the prompt (DangerReason still asks)
	PermDeny                        // refuse without asking
	PermAsk                         // prompt even in auto mode
)

// PermRule is one rule and where it came from.
type PermRule struct {
	Kind   string // allow | deny | ask
	Rule   string // e.g. "Bash(npm test:*)"
	Source string // file path, or "flag" / "config"
}

// PermSet holds the merged rules from config.json, .jazz/settings.json,
// .claude/settings.json (+ ~/.claude/settings.json) and --allow-tools.
type PermSet struct {
	mu    sync.Mutex
	rules []PermRule
	cfg   *Config
	root  string
}

// pfToolAliases maps flag/rule tool names to the Claude Code rule names.
var pfRuleTool = map[string]string{
	"bash": "Bash", "edit": "Edit", "write": "Write", "read": "Read", "fetch": "WebFetch", "webfetch": "WebFetch",
	"todo": "TodoWrite", "todowrite": "TodoWrite", "task": "Task", "multiedit": "Edit", "websearch": "WebFetch",
	"script": "Script",
}

// pfCCName is the Claude Code name of a canonical jazz tool (mcp__ names unchanged).
func pfCCName(tool string) string {
	if strings.HasPrefix(tool, "mcp__") {
		return tool
	}
	if n, ok := pfRuleTool[strings.ToLower(tool)]; ok {
		return n
	}
	return tool
}

// pfNormRule canonicalises the tool part of a rule: "bash(x)" -> "Bash(x)", "edit" -> "Edit".
func pfNormRule(r string) string {
	r = strings.TrimSpace(r)
	name, spec, hasSpec := strings.Cut(r, "(")
	if strings.HasPrefix(name, "mcp__") {
		return r
	}
	n := pfCCName(name)
	if hasSpec {
		return n + "(" + spec
	}
	return n
}

// LoadPerms builds the rule set for a project. extraAllow comes from --allow-tools.
// Project allow rules count only when the project policy is trusted (trust.go);
// project deny/ask rules always count.
func LoadPerms(cfg *Config, root string, extraAllow []string) *PermSet {
	p := &PermSet{cfg: cfg, root: root}
	add := func(kind string, rules []string, src string) {
		for _, r := range rules {
			if strings.TrimSpace(r) == "" {
				continue
			}
			p.rules = append(p.rules, PermRule{Kind: kind, Rule: pfNormRule(r), Source: src})
		}
	}
	if cfg != nil {
		add("allow", cfg.Permissions.Allow, "config")
		add("deny", cfg.Permissions.Deny, "config")
		add("ask", cfg.Permissions.Ask, "config")
	}
	for _, f := range pfGlobalSettingsFiles() {
		if s, ok := pfReadSettings(f); ok {
			add("allow", s.Permissions.Allow, f)
			add("deny", s.Permissions.Deny, f)
			add("ask", s.Permissions.Ask, f)
		}
	}
	trusted := root != "" && pfProjectPolicyTrusted(root)
	if root != "" {
		for _, f := range pfProjectSettingsFiles(root) {
			if s, ok := pfReadSettings(f); ok {
				if trusted {
					add("allow", s.Permissions.Allow, f)
				}
				add("deny", s.Permissions.Deny, f)
				add("ask", s.Permissions.Ask, f)
			}
		}
	}
	for _, r := range extraAllow {
		for _, x := range pfSplitFlag(r) {
			add("allow", []string{x}, "flag")
		}
	}
	return p
}

// pfSplitFlag splits "--allow-tools bash,edit,Bash(npm test:*)" on commas outside parentheses.
func pfSplitFlag(s string) []string {
	var out []string
	depth, start := 0, 0
	for i, c := range s {
		switch c {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	out = append(out, strings.TrimSpace(s[start:]))
	var res []string
	for _, x := range out {
		if x != "" {
			res = append(res, x)
		}
	}
	return res
}

// Check evaluates the rules for a canonical tool name and its normalized args.
// Deny wins over ask, ask over allow. rule is the matching rule text ("" = none).
func (p *PermSet) Check(tool string, args map[string]any) (d PermDecision, rule string) {
	if p == nil {
		return PermDefault, ""
	}
	p.mu.Lock()
	rules := append([]PermRule(nil), p.rules...)
	p.mu.Unlock()
	if len(rules) == 0 {
		return PermDefault, ""
	}
	tool = CanonicalTool(tool)
	var segs []string
	if tool == "bash" {
		cmd, _ := argStr(args, "command")
		if job, _ := argStr(args, "job"); strings.TrimSpace(cmd) == "" && job != "" {
			segs = []string{"job " + job}
		} else {
			segs = shellSegments(cmd)
			if len(segs) == 0 {
				segs = []string{strings.TrimSpace(cmd)}
			}
		}
	}
	// deny/ask see through /bin/rm, sudo rm, git -C x push, git --no-pager push
	// and eval (shellSegments descends into eval); allow only through the
	// harmless git globals (never -c, which can set a pager or an alias)
	match := func(r PermRule) bool {
		if tool != "bash" {
			return p.matches(r, tool, args, "")
		}
		for _, s := range segs {
			if p.matches(r, tool, args, s) || p.matches(r, tool, args, normSegment(s, true)) {
				return true
			}
		}
		return false
	}
	for _, r := range rules {
		if r.Kind == "deny" && match(r) {
			return PermDeny, r.Rule
		}
	}
	for _, r := range rules {
		if r.Kind == "ask" && match(r) {
			return PermAsk, r.Rule
		}
	}
	if tool == "bash" {
		// an allow rule never covers writing to a file through a redirect
		// (Bash(npm test:*) must not allow `npm test > ~/.profile`)
		if cmd, _ := argStr(args, "command"); pfFileRedirect(cmd) {
			return PermDefault, ""
		}
		// every segment needs an allow rule
		first := ""
		for _, s := range segs {
			ok := false
			for _, r := range rules {
				if r.Kind == "allow" && (p.matches(r, tool, args, s) || p.matches(r, tool, args, normSegment(s, false))) {
					ok = true
					if first == "" {
						first = r.Rule
					}
					break
				}
			}
			if !ok {
				return PermDefault, ""
			}
		}
		if first != "" {
			return PermAllow, first
		}
		return PermDefault, ""
	}
	for _, r := range rules {
		if r.Kind == "allow" && match(r) {
			return PermAllow, r.Rule
		}
	}
	return PermDefault, ""
}

// matches tests one rule against a call. seg is the bash segment being checked.
func (p *PermSet) matches(r PermRule, tool string, args map[string]any, seg string) bool {
	name, spec, hasSpec := strings.Cut(r.Rule, "(")
	spec = strings.TrimSuffix(spec, ")")
	if strings.HasPrefix(name, "mcp__") || strings.HasPrefix(tool, "mcp__") {
		if !strings.HasPrefix(tool, "mcp__") || !strings.HasPrefix(name, "mcp__") {
			return false
		}
		if ok, _ := path.Match(name, tool); ok {
			return true
		}
		return strings.Count(name, "__") == 1 && strings.HasPrefix(tool, name+"__") // whole server
	}
	cc := pfCCName(tool)
	switch {
	case name == cc:
	case name == "Edit" && cc == "Write": // Edit rules cover every file-changing tool
	default:
		return false
	}
	if !hasSpec || spec == "*" || spec == "" {
		return true
	}
	switch cc {
	case "Bash":
		return pfMatchBash(spec, seg)
	case "Script": // Script(name), globs allowed; never covers a script's cmd (scripts.go)
		return scRuleMatch(spec, args)
	case "Edit", "Write", "Read":
		pth, _ := argStr(args, "path")
		if pth == "" {
			return false
		}
		// allow rules never reach outside the project through a bare pattern;
		// deny/ask patterns match anywhere (safer)
		return pfMatchPath(spec, pth, p.root, r.Kind != "allow")
	case "WebFetch":
		u, _ := argStr(args, "url")
		if d, ok := strings.CutPrefix(spec, "domain:"); ok {
			pu, err := url.Parse(u)
			if err != nil {
				return false
			}
			h := strings.ToLower(pu.Hostname())
			d = strings.ToLower(d)
			return h == d || strings.HasSuffix(h, "."+d) || (strings.HasPrefix(d, "*.") && strings.HasSuffix(h, d[1:]))
		}
		ok, _ := path.Match(spec, u)
		return ok
	}
	return false
}

// pfMatchBash matches one command segment against a Bash(...) spec.
func pfMatchBash(spec, seg string) bool {
	seg = strings.Join(strings.Fields(seg), " ")
	spec = strings.TrimSpace(spec)
	if pre, ok := strings.CutSuffix(spec, ":*"); ok {
		pre = strings.Join(strings.Fields(pre), " ")
		return seg == pre || strings.HasPrefix(seg, pre+" ")
	}
	if strings.Contains(spec, "*") {
		re := "^" + strings.ReplaceAll(regexp.QuoteMeta(spec), `\*`, ".*") + "$"
		ok, _ := regexp.MatchString(re, seg)
		return ok
	}
	return seg == strings.Join(strings.Fields(spec), " ")
}

// pfMatchPath matches a file path against a gitignore-like glob with ** support.
// A pattern without "/" matches the file name at any depth (inside the project
// unless anywhere is set); relative patterns are relative to the project root.
func pfMatchPath(spec, p, root string, anywhere bool) bool {
	home, _ := os.UserHomeDir()
	if anywhere && runtime.GOOS == "darwin" {
		// deny/ask rules: APFS is case-insensitive, ~/.SSH is ~/.ssh
		low := strings.ToLower
		return pfMatchPathIn(low(spec), low(p), low(root), low(home), anywhere)
	}
	return pfMatchPathIn(spec, p, root, home, anywhere)
}

func pfMatchPathIn(spec, p, root, home string, anywhere bool) bool {
	abs := p
	if strings.HasPrefix(abs, "~/") && home != "" {
		abs = filepath.Join(home, abs[2:])
	}
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, abs)
	}
	abs = filepath.Clean(abs)
	inRoot := root != "" && (abs == root || strings.HasPrefix(abs, root+string(filepath.Separator)))
	pat := strings.TrimPrefix(spec, "./")
	switch {
	case pat == "**" || pat == "*":
		if pat == "**" {
			return inRoot
		}
		return filepath.Dir(abs) == root
	case strings.HasPrefix(pat, "//"):
		pat = pat[1:]
	case strings.HasPrefix(pat, "~/") && home != "":
		pat = filepath.Join(home, pat[2:])
	case strings.HasPrefix(pat, "/"):
	case !strings.Contains(pat, "/"):
		if ok, _ := path.Match(pat, filepath.Base(abs)); ok && (inRoot || anywhere) {
			return true
		}
		return false
	default:
		pat = filepath.Join(root, pat)
	}
	if strings.HasSuffix(pat, "/**") {
		base := strings.TrimSuffix(pat, "/**")
		return abs == base || strings.HasPrefix(abs, base+"/") || pfGlob(pat, abs)
	}
	return pfGlob(pat, abs)
}

// pfGlob matches with path.Match semantics plus "**" for any number of directories.
func pfGlob(pat, s string) bool {
	if !strings.Contains(pat, "**") {
		ok, _ := path.Match(pat, s)
		return ok
	}
	var re strings.Builder
	re.WriteString("^")
	for i := 0; i < len(pat); i++ {
		c := pat[i]
		switch {
		case c == '*' && i+1 < len(pat) && pat[i+1] == '*':
			i++
			if i+1 < len(pat) && pat[i+1] == '/' {
				i++
				re.WriteString("(?:.*/)?")
			} else {
				re.WriteString(".*")
			}
		case c == '*':
			re.WriteString("[^/]*")
		case c == '?':
			re.WriteString("[^/]")
		default:
			re.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	re.WriteString("$")
	ok, _ := regexp.MatchString(re.String(), s)
	return ok
}

var pfSubcmd = lazyRx(`^[a-z][a-z0-9_:-]*$`)

// normSegment canonicalises one bash segment for rule matching: the command
// word loses its directory (/bin/rm -> rm, deny/ask only) and git loses its
// global options (-C dir, --no-pager, --git-dir=…). forDeny also removes a
// leading sudo/doas/pkexec and git's -c k=v, which only make a deny/ask rule
// match more; allow matching keeps them so they cannot widen an allow rule.
func normSegment(seg string, forDeny bool) string {
	w := strings.Fields(seg)
	for forDeny && len(w) > 0 {
		switch filepath.Base(w[0]) {
		case "sudo", "doas", "pkexec":
			w = w[1:]
			for len(w) > 0 && strings.HasPrefix(w[0], "-") {
				opt := w[0]
				w = w[1:]
				switch opt {
				case "-u", "-g", "-C", "-D", "-h", "-p", "-r", "-t", "-U", "-T", "--user", "--group":
					if len(w) > 0 {
						w = w[1:]
					}
				}
			}
			w = stripWrappers(w)
			continue
		}
		break
	}
	if len(w) == 0 {
		return ""
	}
	if forDeny {
		w[0] = filepath.Base(w[0])
	}
	if filepath.Base(w[0]) == "git" {
		rest := w[1:]
	loop:
		for len(rest) > 0 && strings.HasPrefix(rest[0], "-") {
			switch o := rest[0]; {
			case o == "-C" || o == "--git-dir" || o == "--work-tree" || o == "--namespace":
				if len(rest) < 2 {
					break loop
				}
				rest = rest[2:]
			case o == "-c" || o == "--config-env" || strings.HasPrefix(o, "--config-env=") || strings.HasPrefix(o, "--exec-path"):
				if !forDeny {
					break loop
				}
				if o == "-c" || o == "--config-env" {
					if len(rest) < 2 {
						break loop
					}
					rest = rest[2:]
				} else {
					rest = rest[1:]
				}
			case o == "--no-pager" || o == "-P" || o == "-p" || o == "--paginate" || o == "--bare" || o == "--no-replace-objects" ||
				o == "--literal-pathspecs" || o == "--glob-pathspecs" || o == "--noglob-pathspecs" || o == "--icase-pathspecs" || o == "--no-optional-locks" ||
				strings.HasPrefix(o, "--git-dir=") || strings.HasPrefix(o, "--work-tree=") || strings.HasPrefix(o, "--namespace="):
				rest = rest[1:]
			default:
				break loop
			}
		}
		w = append([]string{w[0]}, rest...)
	}
	return strings.Join(w, " ")
}

// pfFileRedirect reports whether cmd redirects output (>, >>, &>, >|) into a
// file other than /dev/null or another descriptor, outside quotes.
func pfFileRedirect(cmd string) bool {
	var q byte
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case q != 0:
			if c == q {
				q = 0
			} else if c == '\\' && q == '"' {
				i++
			}
			continue
		case c == '\\':
			i++
			continue
		case c == '\'' || c == '"':
			q = c
			continue
		case c != '>':
			continue
		}
		j := i + 1
		if j < len(cmd) && (cmd[j] == '>' || cmd[j] == '|') {
			j++
		}
		if j < len(cmd) && cmd[j] == '(' {
			return true // >(cmd) process substitution
		}
		if j < len(cmd) && cmd[j] == '&' {
			i = j
			continue // >&2, 2>&1
		}
		for j < len(cmd) && (cmd[j] == ' ' || cmd[j] == '\t') {
			j++
		}
		k := j
		for k < len(cmd) && !strings.ContainsRune(" \t;&|<>()", rune(cmd[k])) {
			k++
		}
		if t := strings.Trim(cmd[j:k], `"'`); t != "/dev/null" && t != "/dev/stdout" && t != "/dev/stderr" {
			return true
		}
		i = k - 1
	}
	return false
}

// pfExactCmd lists commands whose arguments are code (interpreters, shells,
// awk/sed scripts, find -exec): [t] saves the exact command, never a prefix.
var pfExactCmd = lazyRx(`^(python[0-9.]*|pypy[0-9.]*|node|nodejs|ruby|perl|php|lua[0-9.]*|deno|bun|tsx|ts-node|Rscript|osascript|sh|bash|zsh|dash|ksh|fish|awk|gawk|mawk|nawk|sed|find|xargs|env|eval|exec|source|\.)$`)

// pfRuleLiteral makes text safe inside Bash(...): a literal * would act as a wildcard.
func pfRuleLiteral(s string) bool { return !strings.ContainsAny(s, "*()") }

// pfDeriveRules derives "always" rules for one call.
func (p *PermSet) pfDeriveRules(tool string, args map[string]any) []string {
	tool = CanonicalTool(tool)
	switch {
	case strings.HasPrefix(tool, "mcp__"):
		return []string{tool}
	case tool == "bash":
		cmd, _ := argStr(args, "command")
		var out []string
		seen := map[string]bool{}
		for _, s := range shellSegments(cmd) {
			w := strings.Fields(normSegment(s, false))
			for len(w) > 0 && strings.Contains(w[0], "=") && !strings.HasPrefix(w[0], "=") {
				w = w[1:] // FOO=bar cmd
			}
			if len(w) == 0 {
				continue
			}
			var r string
			exactOnly := pfExactCmd.MatchString(filepath.Base(w[0])) && !(len(w) == 2 && (w[1] == "--version" || w[1] == "-V" || w[1] == "-v"))
			// git with options normSegment kept (-c k=v can set a pager or alias)
			exactOnly = exactOnly || filepath.Base(w[0]) == "git" && len(w) > 1 && strings.HasPrefix(w[1], "-")
			if exactOnly {
				// interpreter code or a script: only this exact command
				exact := strings.Join(w, " ")
				if !pfRuleLiteral(exact) {
					continue // cannot be written as an exact rule: no rule at all
				}
				r = "Bash(" + exact + ")"
			} else {
				pre := w[0]
				if len(w) > 1 && pfSubcmd.MatchString(w[1]) {
					pre += " " + w[1]
				}
				r = "Bash(" + pre + ":*)"
			}
			if !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
		}
		return out
	case tool == "edit" || tool == "write" || tool == "read":
		pth, _ := argStr(args, "path")
		if pth == "" {
			return nil
		}
		name := "Edit"
		if tool == "read" {
			name = "Read"
		}
		abs := expandTilde(pth)
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(p.root, abs)
		}
		abs = filepath.Clean(abs)
		dir := filepath.Dir(abs)
		if rel, err := filepath.Rel(p.root, dir); p.root != "" && err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			if rel == "." {
				return []string{name + "(**)"}
			}
			return []string{name + "(" + filepath.ToSlash(rel) + "/**)"}
		}
		// outside the project: never a whole home (or an ancestor of the home or
		// the project); a file there gets a rule for itself only
		home, _ := os.UserHomeDir()
		if pathUnder(home, dir) || pathUnder(p.root, dir) || dir == filepath.Dir(dir) {
			if strings.ContainsAny(abs, "*?[") {
				return nil
			}
			return []string{name + "(" + abs + ")"}
		}
		return []string{name + "(" + dir + "/**)"}
	case tool == "fetch":
		u, _ := argStr(args, "url")
		if pu, err := url.Parse(u); err == nil && pu.Hostname() != "" {
			return []string{"WebFetch(domain:" + pu.Hostname() + ")"}
		}
		return []string{"WebFetch"}
	case tool == "script":
		return scDeriveRules(p, args)
	}
	return []string{pfCCName(tool)}
}

// Remember derives a rule from an "always" approval (Bash(npm test:*), Edit(dir/**)),
// saves it to the project's .jazz/settings.json and returns it.
func (p *PermSet) Remember(tool string, args map[string]any) (rule string, err error) {
	if p == nil {
		return "", nil
	}
	rules := p.pfDeriveRules(tool, args)
	if len(rules) == 0 {
		return "", errors.New("não consegui derivar uma regra")
	}
	file := filepath.Join(p.root, ".jazz", "settings.json")
	p.mu.Lock()
	for _, r := range rules {
		dup := false
		for _, x := range p.rules {
			if x.Kind == "allow" && x.Rule == r {
				dup = true
				break
			}
		}
		if !dup {
			p.rules = append(p.rules, PermRule{Kind: "allow", Rule: r, Source: file})
		}
	}
	p.mu.Unlock()
	if p.root == "" {
		return strings.Join(rules, ", "), nil
	}
	err = pfEditSettings(p.root, file, func(perm map[string]any) {
		perm["allow"] = pfAppendUnique(perm["allow"], rules...)
	})
	return strings.Join(rules, ", "), err
}

// RememberSession is Remember for this session only: the rule lives in
// memory (source "sessão") and nothing is written ("Sim, e não perguntar de
// novo nesta sessão", as Claude Code's "allow all edits during this session").
func (p *PermSet) RememberSession(tool string, args map[string]any) string {
	if p == nil {
		return ""
	}
	rules := p.pfDeriveRules(tool, args)
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, r := range rules {
		dup := false
		for _, x := range p.rules {
			if x.Kind == "allow" && x.Rule == r {
				dup = true
				break
			}
		}
		if !dup {
			p.rules = append(p.rules, PermRule{Kind: "allow", Rule: r, Source: pfSessionSource})
		}
	}
	return strings.Join(rules, ", ")
}

// pfSessionSource marks rules that live only in this session.
const pfSessionSource = "sessão"

// Rules lists every rule, for /permissions.
func (p *PermSet) Rules() []PermRule {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]PermRule(nil), p.rules...)
}

// Remove deletes a rule from the file it came from (and from this session).
func (p *PermSet) Remove(rule string) error {
	if p == nil {
		return nil
	}
	rule = pfNormRule(rule)
	p.mu.Lock()
	var removed []PermRule
	kept := p.rules[:0:0]
	for _, r := range p.rules {
		if r.Rule == rule {
			removed = append(removed, r)
			continue
		}
		kept = append(kept, r)
	}
	if len(removed) == 0 {
		p.mu.Unlock()
		return errors.New("regra não encontrada: " + rule)
	}
	p.rules = kept
	p.mu.Unlock()
	var firstErr error
	for _, r := range removed {
		var err error
		switch r.Source {
		case "flag", pfSessionSource:
		case "config":
			if p.cfg != nil {
				p.cfg.Permissions.Allow = pfRemoveStr(p.cfg.Permissions.Allow, rule)
				p.cfg.Permissions.Deny = pfRemoveStr(p.cfg.Permissions.Deny, rule)
				p.cfg.Permissions.Ask = pfRemoveStr(p.cfg.Permissions.Ask, rule)
				err = p.cfg.Save()
			}
		default:
			kind := r.Kind
			err = pfEditSettings(p.root, r.Source, func(perm map[string]any) {
				var keep []any
				if l, ok := perm[kind].([]any); ok {
					for _, x := range l {
						if s, ok := x.(string); ok && pfNormRule(s) == rule {
							continue
						}
						keep = append(keep, x)
					}
				}
				perm[kind] = keep
			})
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// pfEditSettings rewrites the permissions object of a settings file, keeping
// every other key. When the project policy was trusted before the edit (or the
// file is user-level), the new content is trusted too: the user made this change.
func pfEditSettings(root, file string, edit func(perm map[string]any)) error {
	project := root != "" && strings.HasPrefix(file, root+string(filepath.Separator))
	trustedBefore := project && pfProjectPolicyTrusted(root)
	doc := map[string]any{}
	if b, err := os.ReadFile(file); err == nil {
		if err := json.Unmarshal(b, &doc); err != nil {
			return errors.New(file + ": JSON inválido, não alterei: " + err.Error())
		}
	}
	perm, _ := doc["permissions"].(map[string]any)
	if perm == nil {
		perm = map[string]any{}
	}
	edit(perm)
	for k, v := range perm {
		if l, ok := v.([]any); ok && len(l) == 0 {
			delete(perm, k)
		}
	}
	doc["permissions"] = perm
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(file, append(b, '\n'), 0o644); err != nil {
		return err
	}
	if trustedBefore {
		return Trust(root, "hooks", pfProjectPolicy(root))
	}
	return nil
}

func pfAppendUnique(v any, add ...string) []any {
	l, _ := v.([]any)
	for _, a := range add {
		dup := false
		for _, x := range l {
			if s, ok := x.(string); ok && s == a {
				dup = true
				break
			}
		}
		if !dup {
			l = append(l, a)
		}
	}
	return l
}

func pfRemoveStr(l []string, s string) []string {
	var out []string
	for _, x := range l {
		if pfNormRule(x) != s {
			out = append(out, x)
		}
	}
	return out
}

// pfAdd adds a rule of kind (allow|deny|ask) to .jazz/settings.json and this session.
func (p *PermSet) pfAdd(kind, rule string) error {
	if p == nil {
		return errors.New("sem conjunto de regras")
	}
	rule = pfNormRule(rule)
	if rule == "" {
		return errors.New("regra vazia")
	}
	file := filepath.Join(p.root, ".jazz", "settings.json")
	p.mu.Lock()
	p.rules = append(p.rules, PermRule{Kind: kind, Rule: rule, Source: file})
	p.mu.Unlock()
	return pfEditSettings(p.root, file, func(perm map[string]any) {
		perm[kind] = pfAppendUnique(perm[kind], rule)
	})
}
