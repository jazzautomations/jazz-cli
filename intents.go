package main

// Deterministic fast paths (docs/research/tocomas-plan-reuse.md A1): a clear
// request such as "rode os testes", "git status" or "o que mudou" maps to one
// command derived from project detection and runs through the shell and the
// normal permission check, with no LLM call. Only an unambiguous full match
// fires; a failing command is handed to the model as the first turn.
// Interactive: `"fastpaths": "on"` (default) asks [Enter] before running;
// `"off"` disables. Headless: only with --fast-path (or in offline mode).

import (
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

//go:embed intents.json
var fpIntentsJSON []byte

// fpProject is what fast paths and run_tests know about the project. It must
// agree with jazz-scripts/detect_project.py (TestFastPathDetectMatchesScript).
type fpProject struct {
	Langs                  []string
	Test, Build, Lint, Fmt string
	Git                    bool
	PM                     string // npm | pnpm | yarn | bun
}

var fpMakeTarget = regexp.MustCompile(`(?m)^([A-Za-z][\w-]*)\s*:([^=]|$)`)

// fpDetect inspects marker files at root (a handful of stats, no walk).
func fpDetect(root string) fpProject {
	var p fpProject
	has := func(n string) bool { _, err := os.Stat(filepath.Join(root, n)); return err == nil }
	read := func(n string) string {
		f, err := os.Open(filepath.Join(root, n))
		if err != nil {
			return ""
		}
		defer f.Close()
		b := make([]byte, 256<<10)
		k, _ := f.Read(b)
		return string(b[:k])
	}
	targets := map[string]bool{}
	if has("Makefile") {
		for _, m := range fpMakeTarget.FindAllStringSubmatch(read("Makefile"), -1) {
			targets[m[1]] = true
		}
	}
	if has("go.mod") {
		p.Langs = append(p.Langs, "go")
	}
	if has("Cargo.toml") {
		p.Langs = append(p.Langs, "rust")
	}
	if has("package.json") {
		p.Langs = append(p.Langs, "js")
	}
	for _, m := range []string{"pyproject.toml", "setup.py", "setup.cfg", "requirements.txt", "pytest.ini", "tox.ini"} {
		if has(m) {
			p.Langs = append(p.Langs, "python")
			break
		}
	}
	put := func(dst *string, v string) {
		if *dst == "" {
			*dst = v
		}
	}
	makeCmds := func() {
		for k, dst := range map[string]*string{"test": &p.Test, "build": &p.Build, "lint": &p.Lint, "fmt": &p.Fmt} {
			if targets[k] {
				put(dst, "make "+k)
			}
		}
	}
	useMake := has("Makefile") && len(p.Langs) != 1
	if useMake {
		makeCmds()
	}
	for _, l := range p.Langs {
		switch l {
		case "go":
			put(&p.Test, "go test ./...")
			put(&p.Build, "go build ./...")
			put(&p.Lint, "go vet ./...")
			put(&p.Fmt, "gofmt -l .")
		case "rust":
			put(&p.Test, "cargo test")
			put(&p.Build, "cargo build")
			put(&p.Lint, "cargo clippy")
			put(&p.Fmt, "cargo fmt --check")
		case "js":
			p.PM = "npm"
			for _, lk := range [][2]string{{"pnpm-lock.yaml", "pnpm"}, {"yarn.lock", "yarn"}, {"bun.lockb", "bun"}, {"bun.lock", "bun"}} {
				if has(lk[0]) {
					p.PM = lk[1]
					break
				}
			}
			var pkg struct {
				Scripts map[string]any `json:"scripts"`
			}
			_ = json.Unmarshal([]byte(read("package.json")), &pkg)
			sc := func(k string) (string, bool) {
				v, ok := pkg.Scripts[k]
				s, _ := v.(string)
				return s, ok
			}
			if t, ok := sc("test"); ok && t != "" && !strings.Contains(t, "no test specified") {
				put(&p.Test, p.PM+" test")
			}
			if _, ok := sc("build"); ok {
				put(&p.Build, p.PM+" run build")
			}
			if _, ok := sc("lint"); ok {
				put(&p.Lint, p.PM+" run lint")
			}
			for _, k := range []string{"format", "fmt"} {
				if _, ok := sc(k); ok {
					put(&p.Fmt, p.PM+" run "+k)
					break
				}
			}
		case "python":
			pyproject := read("pyproject.toml")
			pytest := has("pytest.ini") || has("conftest.py") || strings.Contains(pyproject, "[tool.pytest") ||
				strings.Contains(read("tox.ini"), "[pytest]") || strings.Contains(read("setup.cfg"), "[tool:pytest]") ||
				strings.Contains(read("requirements.txt"), "pytest") || strings.Contains(read("requirements-dev.txt"), "pytest")
			if pytest {
				put(&p.Test, "python3 -m pytest")
			} else {
				put(&p.Test, "python3 -m unittest discover")
			}
			switch {
			case strings.Contains(pyproject, "[tool.ruff") || has("ruff.toml") || has(".ruff.toml"):
				put(&p.Lint, "ruff check .")
				put(&p.Fmt, "ruff format --check .")
			case has(".flake8") || strings.Contains(read("setup.cfg"), "[flake8]") || strings.Contains(read("tox.ini"), "[flake8]"):
				put(&p.Lint, "python3 -m flake8")
			}
		}
	}
	if !useMake {
		makeCmds()
	}
	for d := root; ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			p.Git = true
			break
		}
		up := filepath.Dir(d)
		if up == d {
			break
		}
		d = up
	}
	return p
}

var fpSafeArg = regexp.MustCompile(`^[\w./:@=+-]+$`)

// fpShQuote quotes s for POSIX sh (dash too) when it is not plainly safe.
func fpShQuote(s string) string {
	if fpSafeArg.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// fpTestCommand is the project's test command narrowed to target (a package
// path, file or test name); "" when no test command is known.
func fpTestCommand(p fpProject, target string) string {
	c := p.Test
	target = strings.TrimSpace(target)
	if c == "" || target == "" {
		return c
	}
	q := fpShQuote(target)
	switch {
	case c == "go test ./...":
		if strings.ContainsAny(target, "/.") {
			if !strings.HasPrefix(target, ".") && !strings.HasPrefix(target, "/") {
				q = fpShQuote("./" + target)
			}
			return "go test " + q
		}
		return "go test -run " + q + " ./..."
	case strings.HasSuffix(c, " test") && (strings.HasPrefix(c, "npm") || strings.HasPrefix(c, "pnpm") || strings.HasPrefix(c, "yarn") || strings.HasPrefix(c, "bun")):
		return c + " -- " + q
	case strings.HasPrefix(c, "make "):
		return c // a make target takes no free-form target
	}
	return c + " " + q
}

// ---------- intents ----------

type fpIntentDef struct {
	Name     string            `json:"name"`
	Cmd      string            `json:"cmd"`
	Patterns []string          `json:"patterns"`
	Slots    map[string]string `json:"slots,omitempty"`
	Reject   []string          `json:"reject,omitempty"`
	Disabled bool              `json:"disabled,omitempty"`
}

type fpIntent struct {
	fpIntentDef
	res    []*regexp.Regexp
	slots  map[string]*regexp.Regexp
	reject map[string]bool
}

// fpSet is the compiled intent list for one project root.
type fpSet struct {
	intents []*fpIntent
	warns   []string
}

var (
	fpCacheMu sync.Mutex
	fpCache   = map[string]*fpSet{}
)

// fpIntents returns the intents for root: built-in < user < trusted project.
func fpIntents(root string) *fpSet {
	fpCacheMu.Lock()
	defer fpCacheMu.Unlock()
	if s, ok := fpCache[root]; ok {
		return s
	}
	s := fpBuild(root)
	fpCache[root] = s
	return s
}

func fpBuild(root string) *fpSet {
	set := &fpSet{}
	byName := map[string]fpIntentDef{}
	var order []string
	merge := func(raw []byte, src string) {
		var f struct {
			Intents []fpIntentDef `json:"intents"`
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			set.warns = append(set.warns, src+": "+err.Error())
			return
		}
		for _, d := range f.Intents {
			if d.Name == "" {
				continue
			}
			if _, ok := byName[d.Name]; !ok {
				order = append(order, d.Name)
			}
			byName[d.Name] = d
		}
	}
	merge(fpIntentsJSON, "intents.json")
	if b, err := scReadSmall(filepath.Join(configDir(), "intents.json")); err == nil {
		merge(b, "~/.config/jazz-cli/intents.json")
	}
	if root != "" && IsTrusted(root, "scripts", pfScriptsBlob(root)) {
		if b, err := scReadSmall(filepath.Join(root, ".jazz", "intents.json")); err == nil {
			merge(b, ".jazz/intents.json")
		}
	}
	for _, n := range order {
		d := byName[n]
		if d.Disabled || strings.TrimSpace(d.Cmd) == "" {
			continue
		}
		it := &fpIntent{fpIntentDef: d, slots: map[string]*regexp.Regexp{}, reject: map[string]bool{}}
		ok := true
		for _, p := range d.Patterns {
			re, err := regexp.Compile(`(?i)^(?:` + p + `)$`)
			if err != nil {
				set.warns = append(set.warns, d.Name+": "+err.Error())
				ok = false
				break
			}
			it.res = append(it.res, re)
		}
		for k, p := range d.Slots {
			re, err := regexp.Compile(p)
			if err != nil {
				set.warns = append(set.warns, d.Name+"."+k+": "+err.Error())
				ok = false
				break
			}
			it.slots[k] = re
		}
		for _, r := range d.Reject {
			it.reject[strings.ToLower(r)] = true
		}
		if ok && len(it.res) > 0 {
			set.intents = append(set.intents, it)
		}
	}
	if len(set.warns) > 0 {
		dbg("intents", map[string]any{"warnings": set.warns})
	}
	return set
}

// fpMatch is a resolved fast path.
type fpMatch struct {
	Intent string
	Cmd    string
}

var (
	fpLead  = regexp.MustCompile(`(?i)^(?:(?:por favor|pfv|pf|please|pls|jazz|ei|hey|ok|okay|so|just|pode|voce pode|vc pode|can you|could you|agora|now)[,:]?\s+)+`)
	fpTrail = regexp.MustCompile(`(?i)(?:[,\s]+(?:por favor|pfv|pf|please|pls|pra mim|for me|agora|now))+$`)
	fpPunct = regexp.MustCompile(`[\s?!.…;:]+$`)
	fpVar   = regexp.MustCompile(`\{\{(\w+)\}\}`)
	fpSlotV = regexp.MustCompile(`^[\w$.:/-]{1,80}$`)
)

// fpNormalize strips accents, trailing punctuation and polite filler, and
// collapses spaces; "" when the input is not a one-line short request.
func fpNormalize(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 120 || strings.ContainsAny(s, "\n\r`@") || strings.HasPrefix(s, "/") || strings.HasPrefix(s, "!") {
		return ""
	}
	var b strings.Builder
	for _, r := range norm.NFD.String(s) {
		if unicode.Is(unicode.Mn, r) {
			continue // combining accent
		}
		b.WriteRune(r)
	}
	s = strings.Join(strings.Fields(b.String()), " ")
	for i := 0; i < 3; i++ {
		before := s
		s = fpPunct.ReplaceAllString(s, "")
		s = fpLead.ReplaceAllString(s, "")
		s = fpTrail.ReplaceAllString(s, "")
		s = strings.TrimSpace(s)
		if s == before {
			break
		}
	}
	return s
}

// Match resolves input to exactly one intent whose command can be built for
// this project; two different intents matching = ambiguous = no fast path.
func (set *fpSet) Match(input string, p fpProject, hasRg bool) (fpMatch, bool) {
	n := fpNormalize(input)
	if n == "" || set == nil {
		return fpMatch{}, false
	}
	vars := map[string]string{"test": p.Test, "build": p.Build, "lint": p.Lint, "fmt": p.Fmt}
	if p.Git {
		vars["git"] = "git"
	}
	if hasRg {
		vars["search"] = "rg -n --no-heading -S"
	} else {
		vars["search"] = "grep -rnI --exclude-dir=.git --exclude-dir=node_modules --exclude-dir=vendor"
	}
	var found []fpMatch
	for _, it := range set.intents {
		for _, re := range it.res {
			sm := re.FindStringSubmatch(n)
			if sm == nil {
				continue
			}
			slots := map[string]string{}
			good := true
			for i, name := range re.SubexpNames() {
				if name == "" || i >= len(sm) {
					continue
				}
				v := sm[i]
				chk := fpSlotV
				if c, ok := it.slots[name]; ok {
					chk = c
				}
				if !chk.MatchString(v) || !fpSlotV.MatchString(v) || it.reject[strings.ToLower(v)] {
					good = false
					break
				}
				slots[name] = fpShQuote(v)
			}
			if !good {
				continue
			}
			cmd, ok := fpExpand(it.Cmd, vars, slots)
			if !ok {
				continue
			}
			found = append(found, fpMatch{Intent: it.Name, Cmd: cmd})
			break
		}
	}
	if len(found) != 1 {
		if len(found) > 1 {
			var names []string
			for _, f := range found {
				names = append(names, f.Intent)
			}
			sort.Strings(names)
			dbg("intents", map[string]any{"ambiguous": names, "input": n})
		}
		return fpMatch{}, false
	}
	return found[0], true
}

// fpExpand fills {{var}}; false when a variable is unknown or empty.
func fpExpand(tmpl string, vars, slots map[string]string) (string, bool) {
	ok := true
	out := fpVar.ReplaceAllStringFunc(tmpl, func(m string) string {
		k := m[2 : len(m)-2]
		if v, has := slots[k]; has && v != "" {
			return v
		}
		if v := vars[k]; v != "" {
			return v
		}
		ok = false
		return ""
	})
	return strings.TrimSpace(out), ok && strings.TrimSpace(out) != ""
}

// fpEnabled reads the fastpaths config ("on" default, "off").
func fpEnabled(cfg *Config) bool {
	return !olOffWord(cfg.FastPaths)
}
