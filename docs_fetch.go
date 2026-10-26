package main

// docs_fetch.go: the sources of the docs tool (docs.go).
//
//	local    installed/vendored package: README, docs/*.md, docstrings,
//	         .d.ts declarations, Go and Rust doc comments (version-exact, offline)
//	context7 Context7's public MCP endpoint (opt-in, docs.context7: on)
//	native   pkg.go.dev, docs.rs + crates.io readme, PyPI JSON (+ the docs
//	         page it links), npm registry + unpkg (README and .d.ts of the
//	         exact version), MDN search — all through web.go's guarded
//	         client (fetchBlocked + fetchDialControl) and its hidden-HTML pass.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ---------- local ----------

// dcLocal reads the documentation of the installed/vendored package.
func dcLocal(ctx context.Context, root string, tg dcTarget) (dcResult, bool) {
	dir, ver := dcLocalDir(root, tg)
	if dir == "" {
		return dcResult{}, false
	}
	var text string
	switch tg.Eco {
	case "npm":
		text = dcLocalNode(dir)
	case "go":
		text = dcLocalGo(dir)
	case "cargo":
		text = dcLocalRust(dir)
	default:
		text = dcLocalPy(dir)
	}
	if strings.TrimSpace(text) == "" {
		return dcResult{}, false
	}
	shown := dir
	if r, err := filepath.Rel(root, dir); err == nil && !strings.HasPrefix(r, "..") {
		shown = filepath.ToSlash(r) + "/"
	}
	r := dcResult{Text: text, Source: "local", URL: shown, Version: dcOrStr(ver, tg.Version)}
	if ver == "" && tg.Version != "" {
		r.Note = "installed version not readable; assuming the pinned one"
	}
	return r, true
}

// dcVendorDirs are the folders a project vendors packages into.
var dcVendorDirs = []string{"", "vendor", "third_party", "third-party", "libs", "lib", "src", "_vendor", "external"}

// dcLocalDir finds the package directory and its installed version.
func dcLocalDir(root string, tg dcTarget) (dir, ver string) {
	lib := tg.Lib
	switch tg.Eco {
	case "npm":
		d := filepath.Join(root, "node_modules", filepath.FromSlash(lib))
		if b, err := os.ReadFile(filepath.Join(d, "package.json")); err == nil {
			var p struct{ Version string }
			_ = json.Unmarshal(b, &p)
			return d, p.Version
		}
	case "go":
		if d := filepath.Join(root, "vendor", filepath.FromSlash(lib)); dcIsDir(d) {
			return d, tg.Version
		}
		if tg.Version == "" {
			return "", ""
		}
		for _, mc := range dcGoModCaches() {
			if d := filepath.Join(mc, dcGoEscape(lib)+"@"+tg.Version); dcIsDir(d) {
				return d, tg.Version
			}
		}
	case "cargo":
		home, _ := os.UserHomeDir()
		ch := os.Getenv("CARGO_HOME")
		if ch == "" {
			ch = filepath.Join(home, ".cargo")
		}
		pat := filepath.Join(ch, "registry", "src", "*", lib+"-"+dcOrStr(tg.Version, "*"))
		if m, _ := filepath.Glob(pat); len(m) > 0 {
			sort.Strings(m)
			d := m[len(m)-1]
			return d, strings.TrimPrefix(filepath.Base(d), lib+"-")
		}
	case "py", "":
		return dcPyDir(root, lib)
	}
	return "", ""
}

func dcIsDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// dcGoEscape is the module cache's case encoding (A -> !a).
func dcGoEscape(m string) string {
	var b strings.Builder
	for _, r := range m {
		if r >= 'A' && r <= 'Z' {
			b.WriteByte('!')
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return filepath.FromSlash(b.String())
}

func dcGoModCaches() []string {
	var out []string
	if v := os.Getenv("GOMODCACHE"); v != "" {
		out = append(out, v)
	}
	for _, gp := range filepath.SplitList(os.Getenv("GOPATH")) {
		if gp != "" {
			out = append(out, filepath.Join(gp, "pkg", "mod"))
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out, filepath.Join(home, "go", "pkg", "mod"))
	}
	return out
}

var (
	dcPyVerRE   = regexp.MustCompile(`(?m)^__version__\s*(?::\s*str\s*)?=\s*['"]([^'"]+)['"]`)
	dcPyImpName = regexp.MustCompile(`^[A-Za-z_]\w*$`)
)

// dcPyDir: a vendored package in the project, a project virtualenv, then the
// interpreter's own path (located with find_spec, which does not run the
// package's code).
func dcPyDir(root, lib string) (string, string) {
	names := []string{lib}
	if n := strings.ReplaceAll(lib, "-", "_"); n != lib {
		names = append(names, n)
	}
	if n := strings.ToLower(names[len(names)-1]); n != names[len(names)-1] {
		names = append(names, n)
	}
	var cands []string
	for _, n := range names {
		for _, v := range dcVendorDirs {
			cands = append(cands, filepath.Join(root, v, n))
		}
	}
	for _, venv := range []string{".venv", "venv", "env", ".env"} {
		sp, _ := filepath.Glob(filepath.Join(root, venv, "lib", "python*", "site-packages"))
		sp = append(sp, filepath.Join(root, venv, "Lib", "site-packages"))
		for _, s := range sp {
			for _, n := range names {
				cands = append(cands, filepath.Join(s, n))
			}
		}
	}
	for _, c := range cands {
		if fi, err := os.Stat(filepath.Join(c, "__init__.py")); err == nil && fi.Mode().IsRegular() {
			return c, dcPyVersion(c, lib)
		}
		if fi, err := os.Stat(c + ".py"); err == nil && fi.Mode().IsRegular() && filepath.Dir(c) != root {
			return c + ".py", dcPyVersion(c+".py", lib)
		}
	}
	if !dcPyImpName.MatchString(names[len(names)-1]) {
		return "", ""
	}
	py, err := exec.LookPath("python3")
	if err != nil {
		return "", ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, py, "-I", "-c", "import importlib.util,sys\ns=importlib.util.find_spec(sys.argv[1])\nprint(s.origin if s and s.origin else '')", names[len(names)-1])
	cmd.Dir = os.TempDir() // never the project: -I already drops cwd from sys.path
	cmd.Env = scrubEnv(os.Environ())
	out, err := cmd.Output()
	o := strings.TrimSpace(string(out))
	if err != nil || o == "" || o == "built-in" || o == "frozen" || !filepath.IsAbs(o) {
		return "", ""
	}
	if dcPyStdlib(o) {
		return "", ""
	}
	p := o
	if filepath.Base(o) == "__init__.py" {
		p = filepath.Dir(o)
	}
	return p, dcPyVersion(p, lib)
}

// dcPyStdlib: a module of the standard library (not site-packages).
func dcPyStdlib(origin string) bool {
	return !strings.Contains(origin, "site-packages") && !strings.Contains(origin, "dist-packages")
}

// dcPyVersion: dist-info next to the package, __version__, or a VERSION file.
func dcPyVersion(p, lib string) string {
	parent := filepath.Dir(p)
	if strings.HasSuffix(p, ".py") {
		parent = filepath.Dir(p)
	}
	key := strings.ToLower(dpPySep.ReplaceAllString(lib, "_"))
	if m, _ := filepath.Glob(filepath.Join(parent, "*.dist-info")); len(m) > 0 {
		for _, d := range m {
			b := strings.TrimSuffix(filepath.Base(d), ".dist-info")
			i := strings.LastIndex(b, "-")
			if i > 0 && strings.ToLower(dpPySep.ReplaceAllString(b[:i], "_")) == key {
				return b[i+1:]
			}
		}
	}
	init := p
	if !strings.HasSuffix(p, ".py") {
		init = filepath.Join(p, "__init__.py")
		if b, err := os.ReadFile(filepath.Join(p, "VERSION")); err == nil && len(b) < 64 {
			if v := strings.TrimSpace(string(b)); v != "" {
				return v
			}
		}
	}
	if b, err := dcReadCap(init, 256<<10); err == nil {
		if m := dcPyVerRE.FindStringSubmatch(b); m != nil {
			return m[1]
		}
	}
	return ""
}

func dcReadCap(p string, n int64) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, n))
	return string(b), err
}

// dcDocFiles: README/CHANGELOG/docs/*.md under dir (bounded).
func dcDocFiles(dir string) []string {
	var out []string
	add := func(pat string) {
		m, _ := filepath.Glob(filepath.Join(dir, pat))
		sort.Strings(m)
		for _, f := range m {
			if fi, err := os.Stat(f); err == nil && fi.Mode().IsRegular() && fi.Size() < 1<<20 && len(out) < 30 {
				out = append(out, f)
			}
		}
	}
	for _, p := range []string{"README*", "readme*", "docs/*.md", "docs/*.rst", "docs/*.txt", "docs/*/*.md", "doc/*.md", "CHANGELOG*", "CHANGES*", "MIGRATION*", "UPGRAD*"} {
		add(p)
	}
	return out
}

// dcDocText concatenates doc files with a heading per file.
func dcDocText(dir string, files []string, budget int) string {
	var b strings.Builder
	for _, f := range files {
		if b.Len() > budget {
			break
		}
		s, err := dcReadCap(f, 256<<10)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(dir, f)
		fmt.Fprintf(&b, "\n# %s\n\n", filepath.ToSlash(rel))
		b.WriteString(dcRstHeadings(s))
		b.WriteString("\n")
	}
	return b.String()
}

var dcRstUnder = regexp.MustCompile(`^(={3,}|-{3,}|~{3,}|\^{3,}|\*{3,})\s*$`)

// dcRstHeadings turns reStructuredText underlined titles into "##" headings
// so dcSplit can cut them.
func dcRstHeadings(s string) string {
	lines := strings.Split(s, "\n")
	for i := 1; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i-1])
		if dcRstUnder.MatchString(lines[i]) && t != "" && !strings.HasPrefix(t, "#") && len(t) < 120 && len(strings.TrimSpace(lines[i])) >= len(t)-2 {
			lines[i-1] = "## " + t
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}

var (
	dcPyDefRE = regexp.MustCompile(`(?m)^([ \t]*)(?:async[ \t]+)?(def|class)[ \t]+([A-Za-z]\w*)[ \t]*(\((?:[^()]|\([^()]*\))*\))?[ \t]*(->[^:\n]+)?:[ \t]*\n(?:[ \t]*\n)*[ \t]*[rRbBuU]{0,2}("""|''')`)
	dcPyModRE = regexp.MustCompile(`^(?:\s*#[^\n]*\n)*\s*[rRuU]?("""|''')`)
)

// dcLocalPy: docs files plus the public functions/classes with signature and
// docstring, module by module (regex, no import).
func dcLocalPy(p string) string {
	var b strings.Builder
	dir := p
	if strings.HasSuffix(p, ".py") {
		dir = filepath.Dir(p)
	} else {
		b.WriteString(dcDocText(p, dcDocFiles(p), 200<<10))
	}
	var files []string
	if strings.HasSuffix(p, ".py") {
		files = []string{p}
	} else {
		_ = filepath.WalkDir(p, func(f string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			n := d.Name()
			if d.IsDir() {
				if f != p && (strings.HasPrefix(n, ".") || strings.HasPrefix(n, "_") && n != "__init__" || n == "tests" || n == "test" || n == "docs") {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(n, ".py") && !strings.HasPrefix(n, "test_") && (!strings.HasPrefix(n, "_") || n == "__init__.py") && len(files) < 80 {
				files = append(files, f)
			}
			return nil
		})
		sort.SliceStable(files, func(i, j int) bool { // __init__ first: the public API
			return strings.HasSuffix(files[i], "__init__.py") && !strings.HasSuffix(files[j], "__init__.py")
		})
	}
	base := filepath.Dir(dir)
	for _, f := range files {
		if b.Len() > dcMaxDoc {
			break
		}
		src, err := dcReadCap(f, 512<<10)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(base, f)
		mod := strings.TrimSuffix(strings.TrimSuffix(filepath.ToSlash(rel), ".py"), "/__init__")
		mod = strings.ReplaceAll(mod, "/", ".")
		if m := dcPyModRE.FindStringSubmatchIndex(src); m != nil {
			if doc := dcPyDocAt(src, m[1], src[m[2]:m[3]]); doc != "" {
				fmt.Fprintf(&b, "\n## module %s\n%s\n", mod, doc)
			}
		}
		classAt := map[int]string{} // indent -> enclosing class
		for _, m := range dcPyDefRE.FindAllStringSubmatchIndex(src, -1) {
			indent := len(src[m[2]:m[3]])
			kind, name := src[m[4]:m[5]], src[m[6]:m[7]]
			sig := ""
			if m[8] >= 0 {
				sig = strings.Join(strings.Fields(src[m[8]:m[9]]), " ")
			}
			if m[10] >= 0 {
				sig += " " + strings.TrimSpace(src[m[10]:m[11]])
			}
			for k := range classAt {
				if k >= indent {
					delete(classAt, k)
				}
			}
			owner := ""
			for k, c := range classAt {
				if k < indent {
					owner = c + "."
				}
			}
			if kind == "class" {
				classAt[indent] = name
			}
			doc := dcPyDocAt(src, m[1], src[m[12]:m[13]])
			title := fmt.Sprintf("%s %s.%s%s%s", kind, mod, owner, name, sig)
			fmt.Fprintf(&b, "\n## %s\n%s\n", strings.TrimSpace(title), doc)
		}
	}
	return b.String()
}

// dcPyDocAt reads a docstring that starts at i (just after its opening quotes).
func dcPyDocAt(src string, i int, q string) string {
	j := strings.Index(src[i:], q)
	if j < 0 {
		return ""
	}
	return dcDedent(src[i : i+j])
}

func dcDedent(s string) string {
	lines := strings.Split(strings.Trim(s, "\n"), "\n")
	ind := -1
	for k, l := range lines {
		if k == 0 || strings.TrimSpace(l) == "" {
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, " \t"))
		if ind < 0 || n < ind {
			ind = n
		}
	}
	for k, l := range lines {
		if k > 0 && ind > 0 && len(l) >= ind {
			lines[k] = l[ind:]
		}
		lines[k] = strings.TrimRight(lines[k], " \t")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

var dcTSDeclRE = regexp.MustCompile(`(?m)(/\*\*(?:[^*]|\*[^/])*\*/\s*)?^[ \t]*(?:export[ \t]+)(?:declare[ \t]+)?(?:default[ \t]+)?(function|class|interface|type|const|let|enum|namespace|abstract class)[ \t]+([A-Za-z_$][\w$]*)([^\n{;]*)`)

// dcLocalNode: README plus the declarations of the package's .d.ts files.
func dcLocalNode(dir string) string {
	var b strings.Builder
	b.WriteString(dcDocText(dir, dcDocFiles(dir), 200<<10))
	var p struct {
		Types, Typings string
	}
	if pb, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		_ = json.Unmarshal(pb, &p)
	}
	var dts []string
	for _, t := range []string{p.Types, p.Typings, "index.d.ts", "dist/index.d.ts", "lib/index.d.ts", "types/index.d.ts"} {
		if t != "" {
			if f := filepath.Join(dir, filepath.FromSlash(t)); dcIsFile(f) {
				dts = append(dts, f)
			}
		}
	}
	more, _ := filepath.Glob(filepath.Join(dir, "*.d.ts"))
	dts = append(dts, more...)
	seen := map[string]bool{}
	for _, f := range dts {
		if seen[f] || len(seen) >= 8 {
			continue
		}
		seen[f] = true
		src, err := dcReadCap(f, 512<<10)
		if err != nil {
			continue
		}
		for _, m := range dcTSDeclRE.FindAllStringSubmatch(src, 400) {
			doc := strings.TrimSpace(regexp.MustCompile(`(?m)^\s*/?\*+/?\s?`).ReplaceAllString(m[1], ""))
			fmt.Fprintf(&b, "\n## export %s %s%s\n%s\n", m[2], m[3], strings.TrimSpace(m[4]), doc)
		}
	}
	return b.String()
}

func dcIsFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

var (
	dcGoDeclRE = regexp.MustCompile(`(?m)((?:^//[^\n]*\n)+)?^(func(?:\s*\([^)]*\))?\s+([A-Z]\w*)[^\n{]*|type\s+([A-Z]\w*)[^\n{]*)`)
	dcRsDeclRE = regexp.MustCompile(`(?m)((?:^[ \t]*///[^\n]*\n)+)(?:[ \t]*#\[[^\n]*\n)*[ \t]*(pub(?:\([^)]*\))?\s+(?:async\s+)?(?:unsafe\s+)?(fn|struct|enum|trait|type|const|mod)\s+(\w+)[^\n{;]*)`)
)

// dcLocalGo: README plus exported declarations with their doc comments.
func dcLocalGo(dir string) string {
	var b strings.Builder
	b.WriteString(dcDocText(dir, dcDocFiles(dir), 120<<10))
	files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
	sort.Strings(files)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || b.Len() > dcMaxDoc {
			continue
		}
		src, err := dcReadCap(f, 512<<10)
		if err != nil {
			continue
		}
		for _, m := range dcGoDeclRE.FindAllStringSubmatch(src, 600) {
			doc := strings.TrimSpace(regexp.MustCompile(`(?m)^//\s?`).ReplaceAllString(m[1], ""))
			fmt.Fprintf(&b, "\n## %s\n%s\n", strings.TrimSpace(m[2]), doc)
		}
	}
	return b.String()
}

// dcLocalRust: README plus pub items with /// docs under src/.
func dcLocalRust(dir string) string {
	var b strings.Builder
	b.WriteString(dcDocText(dir, dcDocFiles(dir), 120<<10))
	n := 0
	_ = filepath.WalkDir(filepath.Join(dir, "src"), func(f string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(f, ".rs") || n > 60 || b.Len() > dcMaxDoc {
			return nil
		}
		n++
		src, err := dcReadCap(f, 512<<10)
		if err != nil {
			return nil
		}
		for _, m := range dcRsDeclRE.FindAllStringSubmatch(src, 400) {
			doc := strings.TrimSpace(regexp.MustCompile(`(?m)^[ \t]*///\s?`).ReplaceAllString(m[1], ""))
			fmt.Fprintf(&b, "\n## %s\n%s\n", strings.TrimSpace(m[2]), doc)
		}
		return nil
	})
	return b.String()
}

// ---------- network (web.go's guarded client) ----------

var errDcNotFound = errors.New("not found")

// dcGet fetches u through the fetch tool's client: the egress gate refuses
// private/loopback/tailnet addresses (also after DNS and on redirects).
// HTML is converted to text with hidden elements removed (sanitize.go).
var dcGet = dcGetNet

// dcBlocked is the egress gate (a variable so tests can reach httptest servers).
var dcBlocked = fetchBlocked

// Context7 uses the same DNS/redirect egress policy as native docs.
var dcMCPClient = fetchClient

func dcGetNet(ctx context.Context, u string) (body []byte, ctype string, suspect string, err error) {
	if r := dcBlocked(u); r != "" {
		return nil, "", "", fmt.Errorf("blocked: %s", r)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, "", "", err
	}
	req.Header.Set("User-Agent", "jazz-cli/"+version+" (docs tool)")
	req.Header.Set("Accept", "application/json, text/html;q=0.9, text/plain;q=0.8, */*;q=0.5")
	resp, err := fetchClient.Do(req)
	if err != nil {
		return nil, "", "", err
	}
	defer resp.Body.Close()
	body, _ = io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode == 404 {
		return nil, "", "", errDcNotFound
	}
	if resp.StatusCode >= 400 {
		return nil, "", "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return body, resp.Header.Get("Content-Type"), "", nil
}

// dcGetText: a page as readable text (HTML -> text, hidden HTML removed).
func dcGetText(ctx context.Context, u string) (string, string, error) {
	body, ct, _, err := dcGet(ctx, u)
	if err != nil {
		return "", "", err
	}
	if strings.Contains(ct, "html") || looksHTML(body) {
		page, hidden := snHiddenHTML(string(body), true)
		return htmlToText(dcMainHTML(page)), (snReport{Hidden: hidden}).suspect(), nil
	}
	if isBinary(body) {
		return "", "", errors.New("binary content")
	}
	return string(body), "", nil
}

// dcMarkdown cleans a README from a registry: hidden HTML out, the rest as is.
func dcMarkdown(s string) (string, string) {
	if !snLooksHTML(s) {
		return s, ""
	}
	page, hidden := snHiddenHTML(s, true)
	return page, (snReport{Hidden: hidden}).suspect()
}

var dcMainRE = regexp.MustCompile(`(?is)<main\b[^>]*>(.*)</main>`)

// dcMainHTML keeps <main> when the page has one (docs sites put menus around it).
func dcMainHTML(page string) string {
	if m := dcMainRE.FindStringSubmatch(page); m != nil && len(m[1]) > 500 {
		return m[1]
	}
	return page
}

var dcExactVer = regexp.MustCompile(`^v?\d+(\.\d+)*([-+.][\w.]+)?$`)

// dcNative fetches the official docs of the ecosystem for tg.
func dcNative(ctx context.Context, tg dcTarget, query string) dcResult {
	ver := strings.TrimSpace(tg.Version)
	exact := dcExactVer.MatchString(ver)
	note := ""
	if ver != "" && !exact {
		note = "the project gives a range (" + ver + "): docs of the latest release"
	}
	switch tg.Eco {
	case "go":
		u := "https://pkg.go.dev/" + tg.Lib
		if exact {
			u += "@" + ver
		}
		text, sus, err := dcGetText(ctx, u)
		if err != nil {
			return dcResult{Err: "pkg.go.dev: " + err.Error()}
		}
		return dcResult{Text: text, Source: "pkg.go.dev", URL: u, Version: ver, Note: note, Suspect: sus}
	case "cargo":
		v := "latest"
		if exact {
			v = strings.TrimPrefix(ver, "v")
		}
		u := "https://docs.rs/" + url.PathEscape(tg.Lib) + "/" + v + "/" + strings.ReplaceAll(tg.Lib, "-", "_") + "/"
		text, sus, err := dcGetText(ctx, u)
		if err != nil {
			return dcResult{Err: "docs.rs: " + err.Error()}
		}
		if v != "latest" {
			if rd, _, err := dcGetText(ctx, "https://crates.io/api/v1/crates/"+url.PathEscape(tg.Lib)+"/"+v+"/readme"); err == nil {
				text = "# README\n\n" + rd + "\n\n# API (docs.rs)\n\n" + text
			}
		}
		return dcResult{Text: text, Source: "docs.rs", URL: u, Version: dcOrStr(ver, ""), Note: note, Suspect: sus}
	case "py":
		return dcPyPI(ctx, tg, exact, note)
	case "npm":
		return dcNpm(ctx, tg, exact, note)
	case "mdn":
		return dcMDN(ctx, query)
	case "gem":
		u := "https://rubygems.org/api/v1/gems/" + url.PathEscape(tg.Lib) + ".json"
		body, _, _, err := dcGet(ctx, u)
		if err != nil {
			return dcResult{Err: "rubygems: " + err.Error()}
		}
		var g struct {
			DocumentationURI string `json:"documentation_uri"`
			HomepageURI      string `json:"homepage_uri"`
			Info             string `json:"info"`
			Version          string `json:"version"`
		}
		_ = json.Unmarshal(body, &g)
		text := "# " + tg.Lib + "\n\n" + g.Info
		src := u
		if d := dcOrStr(g.DocumentationURI, g.HomepageURI); d != "" {
			if t, _, err := dcGetText(ctx, d); err == nil {
				text += "\n\n" + t
				src = d
			}
		}
		return dcResult{Text: text, Source: "rubygems", URL: src, Version: g.Version, Note: "docs of the latest release"}
	}
	return dcResult{Err: "no native docs source for " + dcOrStr(tg.Eco, "this ecosystem")}
}

func dcPyPI(ctx context.Context, tg dcTarget, exact bool, note string) dcResult {
	name := url.PathEscape(tg.Lib)
	u := "https://pypi.org/pypi/" + name + "/json"
	if exact {
		u = "https://pypi.org/pypi/" + name + "/" + url.PathEscape(strings.TrimPrefix(tg.Version, "v")) + "/json"
	}
	body, _, _, err := dcGet(ctx, u)
	if err != nil {
		return dcResult{Err: "pypi: " + err.Error()}
	}
	var v struct {
		Info struct {
			Description string            `json:"description"`
			Summary     string            `json:"summary"`
			Version     string            `json:"version"`
			ProjectURLs map[string]string `json:"project_urls"`
			DocsURL     string            `json:"docs_url"`
		} `json:"info"`
	}
	if json.Unmarshal(body, &v) != nil {
		return dcResult{Err: "pypi: unexpected answer"}
	}
	readme, sus := dcMarkdown(v.Info.Description)
	text := "# " + tg.Lib + " " + v.Info.Version + "\n\n" + v.Info.Summary + "\n\n# README\n\n" + dcRstHeadings(readme)
	src := "https://pypi.org/project/" + name + "/" + v.Info.Version + "/"
	if d := dcPyDocsURL(v.Info.ProjectURLs, v.Info.DocsURL); d != "" {
		if t, s2, err := dcGetText(ctx, d); err == nil && len(t) > 200 {
			text += "\n\n# Documentation (" + d + ")\n\n" + t
			src += " + " + d
			if sus == "" {
				sus = s2
			}
		}
	}
	return dcResult{Text: text, Source: "pypi", URL: src, Version: v.Info.Version, Note: note, Suspect: sus}
}

// dcPyDocsURL picks the documentation link of a PyPI project.
func dcPyDocsURL(urls map[string]string, docs string) string {
	if docs != "" {
		return docs
	}
	keys := make([]string, 0, len(urls))
	for k := range urls {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, want := range []string{"documentation", "docs", "doc", "api"} {
		for _, k := range keys {
			if strings.Contains(strings.ToLower(k), want) {
				return urls[k]
			}
		}
	}
	for _, k := range keys {
		if strings.Contains(urls[k], "readthedocs") {
			return urls[k]
		}
	}
	return ""
}

func dcNpm(ctx context.Context, tg dcTarget, exact bool, note string) dcResult {
	ver := "latest"
	if exact {
		ver = strings.TrimPrefix(tg.Version, "v")
	} else if m := regexp.MustCompile(`\d+(\.\d+){0,2}`).FindString(tg.Version); m != "" && strings.Count(m, ".") == 2 && strings.HasPrefix(strings.TrimSpace(tg.Version), "^") {
		ver, note = "latest", "the project gives a range ("+tg.Version+"): docs of the latest release"
	}
	name := strings.ReplaceAll(url.PathEscape(tg.Lib), "%40", "@")
	name = strings.ReplaceAll(name, "%2F", "/")
	u := "https://registry.npmjs.org/" + strings.ReplaceAll(name, "/", "%2F") + "/" + ver
	body, _, _, err := dcGet(ctx, u)
	if err != nil {
		return dcResult{Err: "npm: " + err.Error()}
	}
	var p struct {
		Version, Types, Typings, Readme, Description string
	}
	_ = json.Unmarshal(body, &p)
	if p.Version == "" {
		return dcResult{Err: "npm: unexpected answer"}
	}
	base := "https://unpkg.com/" + name + "@" + p.Version + "/"
	readme := p.Readme
	if readme == "" {
		if t, _, err := dcGetText(ctx, base+"README.md"); err == nil {
			readme = t
		}
	}
	readme, sus := dcMarkdown(readme)
	text := "# " + tg.Lib + " " + p.Version + "\n\n" + p.Description + "\n\n" + readme
	if ty := dcOrStr(p.Types, p.Typings); ty != "" {
		if t, _, err := dcGetText(ctx, base+strings.TrimPrefix(ty, "./")); err == nil {
			for _, m := range dcTSDeclRE.FindAllStringSubmatch(t, 400) {
				doc := strings.TrimSpace(regexp.MustCompile(`(?m)^\s*/?\*+/?\s?`).ReplaceAllString(m[1], ""))
				text += fmt.Sprintf("\n## export %s %s%s\n%s\n", m[2], m[3], strings.TrimSpace(m[4]), doc)
			}
		}
	}
	return dcResult{Text: text, Source: "npm", URL: base, Version: p.Version, Note: note, Suspect: sus}
}

func dcMDN(ctx context.Context, query string) dcResult {
	u := "https://developer.mozilla.org/api/v1/search?locale=en-US&size=3&q=" + url.QueryEscape(query)
	body, _, _, err := dcGet(ctx, u)
	if err != nil {
		return dcResult{Err: "mdn: " + err.Error()}
	}
	var v struct {
		Documents []struct {
			Title   string `json:"title"`
			MDNURL  string `json:"mdn_url"`
			Summary string `json:"summary"`
		} `json:"documents"`
	}
	if json.Unmarshal(body, &v) != nil || len(v.Documents) == 0 {
		return dcResult{Err: "mdn: no result for " + query}
	}
	var b strings.Builder
	var urls []string
	for i, d := range v.Documents {
		page := "https://developer.mozilla.org" + d.MDNURL
		fmt.Fprintf(&b, "\n# %s\n\n%s\n", d.Title, d.Summary)
		if i < 2 {
			if t, _, err := dcGetText(ctx, page); err == nil {
				b.WriteString("\n" + t + "\n")
			}
		}
		urls = append(urls, page)
	}
	return dcResult{Text: b.String(), Source: "mdn", URL: strings.Join(urls[:min(len(urls), 2)], " "), Version: "current"}
}

// ---------- Context7 (HTTP MCP, opt-in) ----------

// dcContext7Preset: the user enabled the context7 MCP preset (jazz mcp enable context7).
func dcContext7Preset() bool {
	c, ok := dcGlobalMCP("context7")
	return ok && !c.Disabled
}

// dcGlobalMCP reads one server of the user's global mcp.json (never a
// project's .mcp.json: a repo must not point the docs tool elsewhere).
func dcGlobalMCP(name string) (MCPServerConf, bool) {
	f, ok := pfReadMCPFile(filepath.Join(configDir(), "mcp.json"))
	if !ok {
		return MCPServerConf{}, false
	}
	c, ok := f.MCPServers[name]
	return c, ok
}

// dcContext7Conf is the server config: the user's mcp.json entry when there
// is one, else the public endpoint with the optional key.
func dcContext7Conf() MCPServerConf {
	if c, ok := dcGlobalMCP("context7"); ok && !c.Disabled && c.URL != "" {
		return c
	}
	s := dcSettings()
	c := MCPServerConf{Type: "http", URL: dcOrStr(s.Context7URL, dcContext7)}
	if k := strings.TrimSpace(s.Context7Key); k != "" {
		c.Headers = map[string]string{"CONTEXT7_API_KEY": k, "Authorization": "Bearer " + k}
	}
	return c
}

var (
	dcC7IDRE  = regexp.MustCompile(`(?m)(?:Context7-compatible library ID|Library ID)[^/\n]*(/[\w.-]+/[\w.-]+(?:/[\w.-]+)?)`)
	dcC7AnyID = regexp.MustCompile(`(/[\w.-]+/[\w.-]+)`)
)

// dcFromContext7 resolves the library id and asks for the docs of query.
func dcFromContext7(ctx context.Context, tg dcTarget, query string) dcResult {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	s := &MCPServer{Name: "context7", Conf: dcContext7Conf(), client: dcMCPClient, pending: map[int64]chan rpcMsg{}}
	if s.Conf.Type == "" {
		s.Conf.Type = "http"
	}
	if !s.isHTTP() {
		return dcResult{Err: "the context7 server must use type http"}
	}
	if r := dcBlocked(expandEnv(s.Conf.URL)); r != "" {
		return dcResult{Err: "blocked: " + r}
	}
	if err := s.connect(ctx, expandEnv); err != nil {
		return dcResult{Err: err.Error()}
	}
	defer s.close()
	var resolve, docs *mcpTool
	for _, t := range s.tools() {
		t := t
		switch {
		case strings.Contains(t.Name, "resolve"):
			resolve = &t
		case t.Name == "query-docs" || t.Name == "get-library-docs" || strings.Contains(t.Name, "docs"):
			docs = &t
		}
	}
	if resolve == nil || docs == nil {
		return dcResult{Err: "unexpected tools (resolve-library-id / query-docs not found)"}
	}
	name := tg.Lib
	if i := strings.LastIndex(name, "/"); i >= 0 && tg.Eco == "go" {
		name = name[i+1:]
	}
	args := dcC7Args(resolve, map[string]string{"libraryName": name, "query": query})
	txt, err := dcC7Call(ctx, s, resolve.Name, args)
	if err != nil {
		return dcResult{Err: "resolve-library-id: " + err.Error()}
	}
	id := ""
	if m := dcC7IDRE.FindStringSubmatch(txt); m != nil {
		id = m[1]
	} else if m := dcC7AnyID.FindStringSubmatch(txt); m != nil {
		id = m[1]
	}
	if id == "" {
		return dcResult{Err: "library not found on Context7"}
	}
	note := ""
	if v := strings.TrimSpace(tg.Version); v != "" && dcExactVer.MatchString(v) {
		// the chosen library's block may list "Versions: v1.2.3, …" (ids /org/project/<version>)
		if vv := dcC7Version(txt, id, v); vv != "" {
			id += "/" + vv
		} else {
			note = "Context7 lists no docs for " + v + ": latest docs"
		}
	}
	dargs := dcC7Args(docs, map[string]string{"libraryId": id, "context7CompatibleLibraryID": id, "query": query, "topic": query})
	if p, _ := docs.InputSchema["properties"].(map[string]any); p != nil {
		if _, ok := p["tokens"]; ok {
			dargs["tokens"] = 4000
		}
	}
	out, err := dcC7Call(ctx, s, docs.Name, dargs)
	if err != nil {
		return dcResult{Err: docs.Name + ": " + err.Error()}
	}
	out, sus := dcMarkdown(out)
	return dcResult{Text: out, Source: "context7", URL: "https://context7.com" + id, Version: dcOrStr(tg.Version, ""), Note: note, Suspect: sus}
}

// dcC7Version finds v among the versions Context7 lists for id ("" = none).
func dcC7Version(txt, id, v string) string {
	want := dcNormVer(v)
	for _, blk := range strings.Split(txt, "----------") {
		if !strings.Contains(blk, id+"\n") && !strings.HasSuffix(strings.TrimSpace(blk), id) && !strings.Contains(blk, id+"/") {
			continue
		}
		for _, l := range strings.Split(blk, "\n") {
			i := strings.Index(l, "Versions:")
			if i < 0 {
				continue
			}
			for _, tok := range strings.Split(l[i+len("Versions:"):], ",") {
				tok = strings.TrimSpace(tok)
				tok = tok[strings.LastIndex(tok, "/")+1:]
				if tok != "" && dcNormVer(tok) == want {
					return tok
				}
			}
		}
		if m := regexp.MustCompile(regexp.QuoteMeta(id) + `/(v?` + regexp.QuoteMeta(want) + `)\b`).FindStringSubmatch(blk); m != nil {
			return m[1]
		}
	}
	return ""
}

// dcC7Args fills the arguments the tool's schema declares.
func dcC7Args(t *mcpTool, vals map[string]string) map[string]any {
	out := map[string]any{}
	props, _ := t.InputSchema["properties"].(map[string]any)
	for k := range props {
		if v, ok := vals[k]; ok {
			out[k] = v
		}
	}
	return out
}

// dcC7Call runs one tools/call and returns its text content.
func dcC7Call(ctx context.Context, s *MCPServer, name string, args map[string]any) (string, error) {
	raw, err := s.request(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return "", err
	}
	var r struct {
		Content []struct {
			Type, Text string
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return "", errors.New("unexpected answer")
	}
	var b strings.Builder
	for _, c := range r.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
			b.WriteString("\n")
		}
	}
	if r.IsError {
		return "", errors.New(oneLine(b.String(), 200))
	}
	return b.String(), nil
}
