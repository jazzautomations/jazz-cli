package main

// docs_deps.go: which libraries (and which versions) this project uses, read
// from its lockfiles and manifests with plain regexes and encoding/json (no
// TOML/YAML parser). The docs tool (docs.go) uses the version to fetch the
// documentation of the version the project really runs, and the API-error
// hints name it. Parsed once per project and re-parsed when a file's mtime or
// size changes.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// dpDep is one dependency.
type dpDep struct {
	Name    string `json:"name"`
	Version string `json:"version"` // exact when a lockfile pins it; else the manifest spec (">=1.2", "^3")
	Eco     string `json:"eco"`     // go | py | npm | cargo | gem | composer
	From    string `json:"from"`    // file it came from (relative)
}

// dpFiles are the files read, in priority order: a lockfile (exact versions)
// wins over a manifest (ranges) for the same name.
var dpFiles = []struct {
	name  string
	eco   string
	parse func(b []byte) []dpDep
	exact bool
}{
	{"go.mod", "go", dpGoMod, true},
	{"poetry.lock", "py", dpTomlPackages, true},
	{"Pipfile.lock", "py", dpPipfileLock, true},
	{"uv.lock", "py", dpTomlPackages, true},
	{"package-lock.json", "npm", dpPackageLock, true},
	{"pnpm-lock.yaml", "npm", dpPnpmLock, true},
	{"yarn.lock", "npm", dpYarnLock, true},
	{"Cargo.lock", "cargo", dpTomlPackages, true},
	{"Gemfile.lock", "gem", dpGemfileLock, true},
	{"composer.lock", "composer", dpComposerLock, true},
	{"go.sum", "go", dpGoSum, false},
	{"pyproject.toml", "py", dpPyproject, false},
	{"package.json", "npm", dpPackageJSON, false},
}

type dpCacheEntry struct {
	stamp string
	deps  map[string]dpDep // key: dpKey(eco, name)
}

var (
	dpMu    sync.Mutex
	dpCache = map[string]*dpCacheEntry{}
)

// dpReqGlob: requirements.txt, requirements-dev.txt, requirements/base.txt …
var dpReqGlobs = []string{"requirements*.txt", "requirements/*.txt"}

// dpProjectFiles lists the dependency files present in root.
func dpProjectFiles(root string) []string {
	var out []string
	for _, f := range dpFiles {
		p := filepath.Join(root, f.name)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			out = append(out, p)
		}
	}
	for _, g := range dpReqGlobs {
		m, _ := filepath.Glob(filepath.Join(root, g))
		sort.Strings(m)
		out = append(out, m...)
	}
	return out
}

// dpStamp fingerprints the dependency files (path, size, mtime).
func dpStamp(files []string) string {
	var b strings.Builder
	for _, p := range files {
		if fi, err := os.Stat(p); err == nil {
			fmt.Fprintf(&b, "%s|%d|%d\n", p, fi.ModTime().UnixNano(), fi.Size())
		}
	}
	return b.String()
}

// dpKey normalizes a name per ecosystem: Python names compare
// case-insensitively with -, _ and . equivalent (PEP 503).
func dpKey(eco, name string) string {
	n := strings.TrimSpace(name)
	switch eco {
	case "py":
		n = strings.ToLower(dpPySep.ReplaceAllString(n, "-"))
	case "npm", "gem", "cargo", "composer":
		n = strings.ToLower(n)
	}
	return eco + ":" + n
}

// dpLoad returns the project's dependencies (cached; re-parsed when a
// dependency file changes).
func dpLoad(root string) map[string]dpDep {
	files := dpProjectFiles(root)
	st := dpStamp(files)
	dpMu.Lock()
	if e := dpCache[root]; e != nil && e.stamp == st {
		dpMu.Unlock()
		return e.deps
	}
	dpMu.Unlock()
	deps := map[string]dpDep{}
	exact := map[string]bool{}
	add := func(d dpDep, isExact bool, from string) {
		if d.Name == "" {
			return
		}
		d.From = from
		k := dpKey(d.Eco, d.Name)
		if old, ok := deps[k]; ok && (exact[k] || !isExact) && old.Version != "" {
			return
		}
		deps[k] = d
		exact[k] = isExact
	}
	for _, p := range files {
		b, err := dpReadSmall(p)
		if err != nil {
			continue
		}
		base := filepath.Base(p)
		rel := base
		if r, err := filepath.Rel(root, p); err == nil {
			rel = filepath.ToSlash(r)
		}
		if strings.HasPrefix(base, "requirements") || strings.HasPrefix(rel, "requirements/") {
			for _, d := range dpRequirements(b) {
				add(d, strings.HasPrefix(d.Version, "=="), rel)
			}
			continue
		}
		for _, f := range dpFiles {
			if f.name != base {
				continue
			}
			for _, d := range f.parse(b) {
				if d.Eco == "" {
					d.Eco = f.eco
				}
				add(d, f.exact, rel)
			}
		}
	}
	for k, d := range deps { // "==1.2.3" from requirements → "1.2.3"
		d.Version = strings.TrimPrefix(d.Version, "==")
		deps[k] = d
	}
	dpMu.Lock()
	dpCache[root] = &dpCacheEntry{stamp: st, deps: deps}
	dpMu.Unlock()
	return deps
}

// dpReadSmall reads a regular file of at most 16 MB (lockfiles can be big).
func dpReadSmall(p string) ([]byte, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > 16<<20 {
		return nil, os.ErrInvalid
	}
	return os.ReadFile(p)
}

// dpFind looks a library up by name: exact key first, then any ecosystem,
// then a Go module whose last path element (or a major-version suffix
// /vN before it) is the name.
func dpFind(deps map[string]dpDep, lib string) (dpDep, bool) {
	lib = strings.TrimSpace(lib)
	if lib == "" {
		return dpDep{}, false
	}
	for _, eco := range []string{"go", "py", "npm", "cargo", "gem", "composer"} {
		if d, ok := deps[dpKey(eco, lib)]; ok {
			return d, true
		}
	}
	// python import name vs distribution name: yaml/PyYAML, bs4 … are not
	// guessed; but "_" vs "-" is (dpKey); Go: last element
	var best dpDep
	found := false
	l := strings.ToLower(lib)
	for _, d := range deps {
		if d.Eco != "go" {
			continue
		}
		parts := strings.Split(strings.ToLower(d.Name), "/")
		last := parts[len(parts)-1]
		if dpMajorRE.MatchString(last) && len(parts) > 1 {
			last = parts[len(parts)-2]
		}
		if last == l || strings.TrimPrefix(last, "go-") == l {
			if !found || len(d.Name) < len(best.Name) {
				best, found = d, true
			}
		}
	}
	return best, found
}

// ---------- parsers ----------

var (
	dpPySep      = lazyRx(`[-_.]+`)
	dpMajorRE    = lazyRx(`^v\d+$`)
	dpNumRE      = lazyRx(`\d+`)
	dpGoReqLine  = lazyRx(`^\s*([^\s()]+)\s+(v[^\s]+)`)
	dpGoReqOne   = lazyRx(`^\s*require\s+([^\s()]+)\s+(v[^\s]+)`)
	dpTomlName   = lazyRx(`^\s*name\s*=\s*"([^"]+)"`)
	dpTomlVer    = lazyRx(`^\s*version\s*=\s*"([^"]+)"`)
	dpReqLine    = lazyRx(`^\s*([A-Za-z0-9][A-Za-z0-9._-]*)\s*(\[[^\]]*\])?\s*((?:===?|~=|>=|<=|!=|>|<)\s*[^\s;#,]+)?`)
	dpYarnHead   = lazyRx(`^"?(@?[^@\s"]+)@`)
	dpYarnVer    = lazyRx(`^\s+version:?\s+"?([^"\s]+)"?`)
	dpGemSpec    = lazyRx(`^    ([A-Za-z0-9_.-]+) \(([^)]+)\)\s*$`)
	dpPnpmImp    = lazyRx(`^\s{6}'?(@?[^:'\s]+)'?:\s*$`)
	dpPnpmImpVer = lazyRx(`^\s{8}version:\s*'?([^\s('"]+)`)
	dpPnpmOld    = lazyRx(`^\s{2}'?(@?[^:'\s]+)'?:\s*'?([0-9][^\s('"]*)`)
	dpPnpmPkg    = lazyRx(`^\s{2}'?/?(@?[^@\s']+)@([0-9][^(:'\s]*)`)
	dpPyDepsArr  = lazyRx(`(?s)\n\s*(?:dependencies|requires)\s*=\s*\[(.*?)\]`)
	dpPyQuoted   = lazyRx(`"([^"]+)"|'([^']+)'`)
	dpPoetrySect = lazyRx(`(?m)^\[tool\.poetry\.(?:dev-)?dependencies\]\s*$`)
	dpPoetryLine = lazyRx(`^\s*([A-Za-z0-9][A-Za-z0-9._-]*)\s*=\s*(?:"([^"]*)"|\{[^}]*version\s*=\s*"([^"]*)")`)
)

func dpGoMod(b []byte) []dpDep {
	var out []dpDep
	in := false
	for _, l := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(t, "require ("), t == "require(":
			in = true
			continue
		case in && t == ")":
			in = false
			continue
		}
		var m []string
		if in {
			m = dpGoReqLine.FindStringSubmatch(t)
		} else {
			m = dpGoReqOne.FindStringSubmatch(t)
		}
		if m != nil && !strings.HasPrefix(m[1], "//") {
			out = append(out, dpDep{Name: m[1], Version: m[2], Eco: "go"})
		}
	}
	return out
}

// dpGoSum: without go.mod, the highest version listed per module.
func dpGoSum(b []byte) []dpDep {
	best := map[string]string{}
	var order []string
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) < 3 || strings.HasSuffix(f[1], "/go.mod") {
			continue
		}
		if _, ok := best[f[0]]; !ok {
			order = append(order, f[0])
		}
		if dpVerLess(best[f[0]], f[1]) {
			best[f[0]] = f[1]
		}
	}
	var out []dpDep
	for _, n := range order {
		out = append(out, dpDep{Name: n, Version: best[n], Eco: "go"})
	}
	return out
}

// dpVerLess compares dotted numeric versions loosely (a < b).
func dpVerLess(a, b string) bool {
	if a == "" {
		return true
	}
	na, nb := dpNumRE.FindAllString(a, 4), dpNumRE.FindAllString(b, 4)
	for i := 0; i < len(na) && i < len(nb); i++ {
		x, y := len(na[i]), len(nb[i])
		if x != y {
			return x < y
		}
		if na[i] != nb[i] {
			return na[i] < nb[i]
		}
	}
	return len(na) < len(nb)
}

// dpTomlPackages reads [[package]] name/version pairs (poetry.lock, uv.lock, Cargo.lock).
func dpTomlPackages(b []byte) []dpDep {
	var out []dpDep
	name, ver, inPkg := "", "", false
	flush := func() {
		if inPkg && name != "" {
			out = append(out, dpDep{Name: name, Version: ver})
		}
		name, ver = "", ""
	}
	for _, l := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") {
			if t == "[[package]]" {
				flush()
				inPkg = true
				continue
			}
			if inPkg && name != "" && ver != "" {
				continue // [package.dependencies] and friends
			}
		}
		if !inPkg {
			continue
		}
		if m := dpTomlName.FindStringSubmatch(l); m != nil && name == "" {
			name = m[1]
		} else if m := dpTomlVer.FindStringSubmatch(l); m != nil && ver == "" {
			ver = m[1]
		}
	}
	flush()
	return out
}

func dpPipfileLock(b []byte) []dpDep {
	var v map[string]map[string]struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	var out []dpDep
	for _, sect := range []string{"default", "develop"} {
		names := make([]string, 0, len(v[sect]))
		for n := range v[sect] {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			out = append(out, dpDep{Name: n, Version: strings.TrimPrefix(v[sect][n].Version, "==")})
		}
	}
	return out
}

func dpRequirements(b []byte) []dpDep {
	var out []dpDep
	for _, l := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "-") || strings.Contains(t, "://") {
			continue
		}
		if m := dpReqLine.FindStringSubmatch(t); m != nil {
			out = append(out, dpDep{Name: m[1], Version: strings.ReplaceAll(m[3], " ", ""), Eco: "py"})
		}
	}
	return out
}

func dpPyproject(b []byte) []dpDep {
	s := string(b)
	var out []dpDep
	for _, m := range dpPyDepsArr.FindAllStringSubmatch(s, -1) {
		for _, q := range dpPyQuoted.FindAllStringSubmatch(m[1], -1) {
			spec := q[1] + q[2]
			if r := dpReqLine.FindStringSubmatch(spec); r != nil {
				out = append(out, dpDep{Name: r[1], Version: strings.ReplaceAll(r[3], " ", ""), Eco: "py"})
			}
		}
	}
	for _, loc := range dpPoetrySect.FindAllStringIndex(s, -1) {
		for _, l := range strings.Split(s[loc[1]:], "\n") {
			t := strings.TrimSpace(l)
			if strings.HasPrefix(t, "[") {
				break
			}
			if m := dpPoetryLine.FindStringSubmatch(l); m != nil && !strings.EqualFold(m[1], "python") {
				out = append(out, dpDep{Name: m[1], Version: m[2] + m[3], Eco: "py"})
			}
		}
	}
	return out
}

func dpPackageLock(b []byte) []dpDep {
	var v struct {
		Packages map[string]struct {
			Version string `json:"version"`
		} `json:"packages"`
		Dependencies map[string]struct {
			Version string `json:"version"`
		} `json:"dependencies"`
	}
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	var out []dpDep
	if len(v.Packages) > 0 {
		for k, p := range v.Packages {
			n := strings.TrimPrefix(k, "node_modules/")
			if k == "" || n == k || strings.Contains(n, "/node_modules/") {
				continue // the root and nested copies
			}
			out = append(out, dpDep{Name: n, Version: p.Version})
		}
	} else {
		for n, p := range v.Dependencies {
			out = append(out, dpDep{Name: n, Version: p.Version})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func dpPnpmLock(b []byte) []dpDep {
	var out []dpDep
	seen := map[string]bool{}
	lines := strings.Split(string(b), "\n")
	sect, pending := "", ""
	for _, l := range lines {
		if len(l) > 0 && l[0] != ' ' {
			sect = strings.TrimSuffix(strings.TrimSpace(l), ":")
			pending = ""
			continue
		}
		switch sect {
		case "importers": // lockfile v6+/v9: importers: .: dependencies: name: {specifier, version}
			if m := dpPnpmImp.FindStringSubmatch(l); m != nil {
				pending = m[1]
			} else if m := dpPnpmImpVer.FindStringSubmatch(l); m != nil && pending != "" && !seen[pending] {
				seen[pending] = true
				out = append(out, dpDep{Name: pending, Version: m[1]})
				pending = ""
			}
		case "dependencies", "devDependencies": // lockfile v5: name: 1.2.3
			if m := dpPnpmOld.FindStringSubmatch(l); m != nil && !seen[m[1]] {
				seen[m[1]] = true
				out = append(out, dpDep{Name: m[1], Version: m[2]})
			}
		}
	}
	for _, l := range lines { // packages: /name@1.2.3: (only names not seen above)
		if m := dpPnpmPkg.FindStringSubmatch(l); m != nil && !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, dpDep{Name: m[1], Version: m[2]})
		}
	}
	return out
}

func dpYarnLock(b []byte) []dpDep {
	var out []dpDep
	seen := map[string]bool{}
	cur := ""
	for _, l := range strings.Split(string(b), "\n") {
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		if l[0] != ' ' {
			cur = ""
			if m := dpYarnHead.FindStringSubmatch(l); m != nil {
				cur = m[1]
			}
			continue
		}
		if m := dpYarnVer.FindStringSubmatch(l); m != nil && cur != "" && !seen[cur] {
			seen[cur] = true
			out = append(out, dpDep{Name: cur, Version: m[1]})
			cur = ""
		}
	}
	return out
}

func dpGemfileLock(b []byte) []dpDep {
	var out []dpDep
	in := false
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) == "specs:" {
			in = true
			continue
		}
		if in && len(l) > 0 && l[0] != ' ' {
			in = false
		}
		if m := dpGemSpec.FindStringSubmatch(l); in && m != nil {
			out = append(out, dpDep{Name: m[1], Version: m[2]})
		}
	}
	return out
}

func dpComposerLock(b []byte) []dpDep {
	var v struct {
		Packages    []struct{ Name, Version string } `json:"packages"`
		PackagesDev []struct{ Name, Version string } `json:"packages-dev"`
	}
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	var out []dpDep
	for _, p := range append(v.Packages, v.PackagesDev...) {
		out = append(out, dpDep{Name: p.Name, Version: strings.TrimPrefix(p.Version, "v")})
	}
	return out
}

func dpPackageJSON(b []byte) []dpDep {
	var v map[string]json.RawMessage
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	var out []dpDep
	for _, sect := range []string{"dependencies", "devDependencies", "peerDependencies"} {
		var m map[string]string
		if json.Unmarshal(v[sect], &m) != nil {
			continue
		}
		names := make([]string, 0, len(m))
		for n := range m {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			out = append(out, dpDep{Name: n, Version: m[n]})
		}
	}
	return out
}
