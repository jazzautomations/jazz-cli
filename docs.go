package main

// docs.go: the docs tool. The model asks {library, query, version?} and gets
// at most ~1.5k tokens of the documentation sections that best match the
// query, for the version the project pins (docs_deps.go), with the source
// and URL. Retrieval is lexical ("grep is all you need",
// docs/research/grep-is-all-you-need.md): the docs are split into sections
// and scored by the query's identifiers (exact symbol > word overlap).
//
// Sources (docs_fetch.go), first hit wins:
//  1. local: the installed or vendored package (node_modules, site-packages,
//     a vendored package dir, GOMODCACHE, the cargo registry): exact version,
//     offline;
//  2. Context7 (docs.context7: on, default off: the query leaves the machine)
//     through the HTTP MCP client (mcp.go);
//  3. native fetchers through web.go's guarded client: pkg.go.dev, docs.rs,
//     PyPI, the npm registry + unpkg, MDN.
//
// Everything goes into tool results or the user message, never the system
// prompt (prompt cache). Remote docs are cached in dataDir()/docs for 7 days;
// --offline / JAZZ_OFFLINE=1 serve the cache and local sources only.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DocsConf is the "docs" object of config.json (Config.Docs).
type DocsConf struct {
	Tool        string `json:"tool,omitempty"`         // on (default) | off: the docs tool
	Context7    string `json:"context7,omitempty"`     // off (default) | on: ask Context7 first (queries leave the machine)
	Context7Key string `json:"context7_key,omitempty"` // optional API key (${ENV} expanded)
	Context7URL string `json:"context7_url,omitempty"` // default https://mcp.context7.com/mcp
	Hints       string `json:"hints,omitempty"`        // on (default) | off: "docs disponíveis" after an API error
	APIMap      string `json:"api_map,omitempty"`      // on (default) | off: "APIs do projeto relacionadas" note
	Prefetch    string `json:"prefetch,omitempty"`     // on (default) | off: plan mode, L tasks: cached docs of the libs named
	TTLDays     int    `json:"ttl_days,omitempty"`     // remote docs cache (default 7)
}

const (
	dcMaxChars   = 6000 // ~1.5k tokens returned to the model
	dcMaxSection = 2400 // one section at most
	dcMaxDoc     = 600 << 10
	dcContext7   = "https://mcp.context7.com/mcp"
)

var (
	dcOfflineRun atomic.Bool // --offline for this run
	dcSetOnce    sync.Once
	dcSetVal     DocsConf
)

// dcSettings: config.json "docs" (read once) with JAZZ_DOCS* env on top.
func dcSettings() DocsConf {
	dcSetOnce.Do(func() {
		var f struct {
			Docs DocsConf `json:"docs"`
		}
		if b, err := os.ReadFile(filepath.Join(configDir(), "config.json")); err == nil {
			_ = json.Unmarshal(b, &f)
		}
		dcSetVal = f.Docs
	})
	c := dcSetVal
	for env, p := range map[string]*string{"JAZZ_DOCS": &c.Tool, "JAZZ_DOCS_CONTEXT7": &c.Context7, "JAZZ_DOCS_HINTS": &c.Hints, "JAZZ_API_MAP": &c.APIMap, "JAZZ_DOCS_PREFETCH": &c.Prefetch} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			*p = v
		}
	}
	if k := strings.TrimSpace(os.Getenv("CONTEXT7_API_KEY")); k != "" && c.Context7Key == "" {
		c.Context7Key = k
	}
	if c.TTLDays <= 0 {
		c.TTLDays = 7
	}
	return c
}

func dcOn(v string) bool { return !olOffWord(v) }
func dcToolOn() bool     { return dcOn(dcSettings().Tool) }
func dcOffline() bool    { return dcOfflineRun.Load() || olForcedEnv() }
func dcOptIn(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "1", "true", "yes", "sim":
		return true
	}
	return false
}

// dcConfigure applies the run flags (pfSetupAgent).
func dcConfigure(offline bool) { dcOfflineRun.Store(offline) }

var dcSpec = ToolSpec{Name: "docs", Description: "Look up the documentation of a library for the version this project uses (detected from its lockfile/manifest) and get only the sections relevant to query, with source URL and version. Use it before calling an API you are not sure about, and after an AttributeError/TypeError/unknown-symbol error from a library. Library can be a package/module name (requests, lodash, github.com/spf13/cobra, serde) or \"mdn\" for web APIs.",
	Params: obj(map[string]any{
		"library": str("Package or module name as in the lockfile/imports, or \"mdn\""),
		"query":   str("Identifiers and/or words to look for, e.g. \"Client.get timeout\""),
		"version": str("Version to use instead of the detected one (optional)"),
	}, "library", "query")}

// ---------- tool ----------

// dcTarget is a resolved library.
type dcTarget struct {
	Lib     string // as given (or the dep's full name)
	Eco     string // go | py | npm | cargo | gem | composer | mdn | ""
	Version string
	VerFrom string // file the version came from, "arg", "installed" or ""
}

var dcEcoPrefix = regexp.MustCompile(`^(go|py|python|pypi|npm|node|js|cargo|rust|crate|gem|ruby|composer|php|mdn|web):(.+)$`)

// dcResolve maps the model's library name to an ecosystem and version.
func dcResolve(root, lib, ver string) dcTarget {
	lib = strings.TrimSpace(lib)
	t := dcTarget{Lib: lib}
	if m := dcEcoPrefix.FindStringSubmatch(strings.ToLower(lib)); m != nil {
		t.Eco = map[string]string{"go": "go", "py": "py", "python": "py", "pypi": "py", "npm": "npm", "node": "npm", "js": "npm",
			"cargo": "cargo", "rust": "cargo", "crate": "cargo", "gem": "gem", "ruby": "gem", "composer": "composer", "php": "composer", "mdn": "mdn", "web": "mdn"}[m[1]]
		t.Lib = lib[len(m[1])+1:]
	}
	switch strings.ToLower(t.Lib) {
	case "mdn", "web", "dom", "web api", "web apis", "browser", "javascript", "css", "html":
		if t.Eco == "" {
			t.Eco = "mdn"
		}
	}
	if t.Eco == "mdn" {
		return t
	}
	deps := dpLoad(root)
	if d, ok := dpFind(deps, t.Lib); ok && (t.Eco == "" || t.Eco == d.Eco) {
		t.Lib, t.Eco, t.Version, t.VerFrom = d.Name, d.Eco, d.Version, d.From
	} else if t.Eco != "" {
		if d, ok := deps[dpKey(t.Eco, t.Lib)]; ok {
			t.Lib, t.Version, t.VerFrom = d.Name, d.Version, d.From
		}
	}
	// a Go package inside a required module: github.com/x/y/sub
	if t.Version == "" && strings.Contains(t.Lib, "/") {
		for _, d := range deps {
			if d.Eco == "go" && strings.HasPrefix(t.Lib, d.Name+"/") {
				t.Eco, t.Version, t.VerFrom = "go", d.Version, d.From
			}
		}
	}
	if t.Eco == "" {
		t.Eco = dcGuessEco(root, t.Lib)
	}
	if v := strings.TrimSpace(ver); v != "" {
		t.Version, t.VerFrom = v, "arg"
	}
	return t
}

var dcGoModRE = regexp.MustCompile(`^[a-z0-9.-]+\.[a-z]{2,}/`)

// dcGuessEco: the ecosystem of a library the lockfiles do not list.
func dcGuessEco(root, lib string) string {
	switch {
	case dcGoModRE.MatchString(lib):
		return "go"
	case strings.HasPrefix(lib, "@"):
		return "npm"
	}
	if _, err := os.Stat(filepath.Join(root, "node_modules", lib, "package.json")); err == nil {
		return "npm"
	}
	for _, f := range []struct{ file, eco string }{{"go.mod", "go"}, {"Cargo.toml", "cargo"}, {"package.json", "npm"}, {"pyproject.toml", "py"}, {"setup.py", "py"}, {"requirements.txt", "py"}, {"Gemfile", "gem"}, {"composer.json", "composer"}} {
		if _, err := os.Stat(filepath.Join(root, f.file)); err == nil {
			if f.eco == "go" && !strings.Contains(lib, "/") {
				continue // a bare name in a Go repo is rarely a Go module
			}
			return f.eco
		}
	}
	return "py" // vendored Python packages are the common case without a manifest
}

func (t *Toolbox) docs(ctx context.Context, m map[string]any, raw string) ToolOutput {
	lib, _ := argStr(m, "library")
	q, _ := argStr(m, "query")
	ver, _ := argStr(m, "version")
	lib, q = strings.TrimSpace(lib), strings.TrimSpace(q)
	if lib == "" {
		return validationErr("docs", "missing required parameter 'library'", raw)
	}
	if q == "" {
		q = lib
	}
	t0 := time.Now()
	tg := dcResolve(t.Root, lib, ver)
	if !dcValidLibrary(tg.Lib) || len(ver) > 128 || len(q) > 1200 {
		return validationErr("docs", "invalid library name or oversized version/query", raw)
	}
	res := dcLookup(ctx, t.Root, tg, q, dcLookupOpts{Net: t.Web && !dcOffline(), Context7: dcOptIn(dcSettings().Context7) || dcContext7Preset()})
	if res.Err != "" {
		return ToolOutput{IsErr: true, Text: res.Err, Display: Display{Summary: oneLine(res.Err, 120)}}
	}
	text, n, total := dcRender(tg, res, q)
	return ToolOutput{Text: text, Suspect: res.Suspect, Display: Display{
		Summary: fmt.Sprintf("%s %s · %s · %d/%d seções · %s", tg.Lib, dcOrStr(res.Version, "?"), res.Source, n, total, time.Since(t0).Round(100*time.Millisecond)),
		Preview: dcPreview(text)}}
}

// dcValidLibrary accepts registry/module names, never filesystem paths or globs.
func dcValidLibrary(lib string) bool {
	if lib == "" || len(lib) > 240 || strings.HasPrefix(lib, "/") || strings.ContainsAny(lib, "\\:*?[]\r\n\t ") {
		return false
	}
	for _, part := range strings.Split(lib, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func dcOrStr(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

func dcPreview(text string) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, "## ") || strings.HasPrefix(l, "# ") {
			out = append(out, strings.TrimLeft(l, "# "))
			if len(out) == 3 {
				break
			}
		}
	}
	return out
}

// ---------- lookup ----------

type dcLookupOpts struct {
	Net       bool // network sources allowed (fetch on, not offline)
	Context7  bool
	LocalOnly bool // prefetch: local + cache, never the network
}

// dcResult is the documentation found for a target.
type dcResult struct {
	Text    string
	Source  string // local | context7 | pkg.go.dev | docs.rs | pypi | npm | mdn | cache:<source>
	URL     string // URL or local path
	Version string
	Note    string // e.g. "versão aproximada"
	Suspect string
	Err     string
}

// dcLookup tries the sources in order: local, cache, Context7, native.
func dcLookup(ctx context.Context, root string, tg dcTarget, query string, o dcLookupOpts) dcResult {
	if tg.Eco != "mdn" {
		if r, ok := dcLocal(ctx, root, tg); ok {
			return r
		}
	}
	queryKey := "q-" + pfHash([]byte(strings.ToLower(query)))[:12]
	if r, ok := dcCacheGet(tg, queryKey, o.Net && !o.LocalOnly); ok {
		return r
	}
	key := ""
	if tg.Eco == "mdn" {
		key = queryKey
	}
	if r, ok := dcCacheGet(tg, key, o.Net && !o.LocalOnly); ok {
		return r
	}
	if !o.Net || o.LocalOnly {
		why := "offline mode (--offline / JAZZ_OFFLINE)"
		if !o.Net && !dcOffline() {
			why = "web access is off (\"web\": false)"
		}
		if o.LocalOnly {
			why = "prefetch uses local and cached docs only"
		}
		return dcResult{Err: fmt.Sprintf("No local or cached docs for %s %s and the network is not used: %s. Read the package sources directly (rg in node_modules/site-packages/vendor) instead.", tg.Lib, tg.Version, why)}
	}
	var errs []string
	if o.Context7 && tg.Eco != "mdn" {
		r := dcFromContext7(ctx, tg, query)
		if r.Err == "" && strings.TrimSpace(r.Text) != "" {
			dcCachePut(tg, queryKey, r) // query-specific: Context7 answers per topic
			return r
		}
		errs = append(errs, "context7: "+r.Err)
	}
	r := dcNative(ctx, tg, query)
	if r.Err == "" && strings.TrimSpace(r.Text) != "" {
		dcCachePut(tg, key, r)
		return r
	}
	errs = append(errs, r.Err)
	return dcResult{Err: fmt.Sprintf("No docs found for %s (%s %s): %s. Try another library name (as in the lockfile), or fetch the docs URL directly.", tg.Lib, tg.Eco, dcOrStr(tg.Version, "latest"), strings.Join(errs, "; "))}
}

// ---------- cache ----------

type dcMeta struct {
	Source  string    `json:"source"`
	URL     string    `json:"url"`
	Version string    `json:"version"`
	Note    string    `json:"note,omitempty"`
	Suspect string    `json:"suspect,omitempty"`
	Fetched time.Time `json:"fetched"`
}

var dcSafeRE = regexp.MustCompile(`[^A-Za-z0-9._@+-]+`)

func dcCacheDir(tg dcTarget) string {
	v := dcOrStr(tg.Version, "latest")
	lib := strings.Trim(dcSafeRE.ReplaceAllString(strings.ToLower(tg.Lib), "_"), "._") + "-" + pfHash([]byte(tg.Lib))[:12]
	return filepath.Join(dataDir(), "docs", dcOrStr(tg.Eco, "any"), dcOrStr(lib, "_"), strings.Trim(dcSafeRE.ReplaceAllString(v, "_"), "._"))
}

// dcCacheGet returns a cached doc; a stale one is used only when the network
// is not (offline serves whatever the cache holds).
func dcCacheGet(tg dcTarget, key string, net bool) (dcResult, bool) {
	dir := dcCacheDir(tg)
	name := dcOrStr(key, "doc")
	b, err := os.ReadFile(filepath.Join(dir, name+".md"))
	if err != nil {
		return dcResult{}, false
	}
	var m dcMeta
	mb, _ := os.ReadFile(filepath.Join(dir, name+".json"))
	_ = json.Unmarshal(mb, &m)
	ttl := time.Duration(dcSettings().TTLDays) * 24 * time.Hour
	if net && time.Since(m.Fetched) > ttl {
		return dcResult{}, false
	}
	return dcResult{Text: string(b), Source: "cache:" + m.Source, URL: m.URL, Version: dcOrStr(m.Version, tg.Version), Note: m.Note, Suspect: m.Suspect}, true
}

func dcCachePut(tg dcTarget, key string, r dcResult) {
	dir := dcCacheDir(tg)
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	name := dcOrStr(key, "doc")
	text := r.Text
	if len(text) > dcMaxDoc {
		text = text[:dcMaxDoc]
	}
	_ = os.WriteFile(filepath.Join(dir, name+".md"), []byte(text), 0o600)
	mb, _ := json.Marshal(dcMeta{Source: r.Source, URL: r.URL, Version: r.Version, Note: r.Note, Suspect: r.Suspect, Fetched: time.Now()})
	_ = os.WriteFile(filepath.Join(dir, name+".json"), mb, 0o600)
}

// ---------- selection ----------

// dcSection is one heading-delimited part of a doc.
type dcSection struct {
	Title string
	Body  string
	idx   int
	score float64
}

var dcHeadRE = regexp.MustCompile(`^(#{1,6})\s+\S`)

// dcSplit cuts a doc at markdown headings (HTML pages arrive as text with
// "#" headings, htmlToText); long sections are cut again at blank lines.
func dcSplit(text string) []dcSection {
	var out []dcSection
	cur := dcSection{}
	var body []string
	flush := func() {
		b := strings.TrimSpace(strings.Join(body, "\n"))
		if b == "" && cur.Title == "" {
			return
		}
		for _, part := range dcChunk(b, dcMaxSection) {
			out = append(out, dcSection{Title: cur.Title, Body: part, idx: len(out)})
		}
		if b == "" {
			out = append(out, dcSection{Title: cur.Title, idx: len(out)})
		}
	}
	inFence := false
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			inFence = !inFence
		}
		if !inFence && dcHeadRE.MatchString(l) {
			flush()
			cur, body = dcSection{Title: strings.TrimSpace(strings.TrimLeft(l, "#"))}, nil
			continue
		}
		body = append(body, l)
	}
	flush()
	return out
}

// dcChunk splits s into pieces of at most n bytes at paragraph breaks.
func dcChunk(s string, n int) []string {
	if len(s) <= n {
		return []string{s}
	}
	var out []string
	var cur strings.Builder
	for _, p := range strings.Split(s, "\n\n") {
		if cur.Len() > 0 && cur.Len()+len(p)+2 > n {
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
		}
		for len(p) > n { // one huge paragraph
			out = append(out, p[:n])
			p = p[n:]
		}
		cur.WriteString(p)
		cur.WriteString("\n\n")
	}
	if strings.TrimSpace(cur.String()) != "" {
		out = append(out, strings.TrimSpace(cur.String()))
	}
	return out
}

var (
	dcDefRE   = regexp.MustCompile(`(?i)\b(def|func|function|class|type|struct|fn|method|interface|const|let|var|trait|enum|module)\b`)
	dcIdentRE = regexp.MustCompile(`[A-Za-z_$][\w$]*(?:[.:]{1,2}[A-Za-z_$][\w$]*)*`)
	dcStop    = map[string]bool{}
)

func init() {
	for _, w := range strings.Fields("the a an and or of to in on for with how what use using usage example examples is are be do does by from at as it its this that " +
		"o a os as um uma de do da dos das em no na nos nas para por com como usar uso exemplo exemplos que qual quais é e ou se ao à função funcao metodo método") {
		dcStop[w] = true
	}
}

// dcTerms extracts the query's identifiers (symbols) and plain words.
func dcTerms(q string) (syms, words []string) {
	seen := map[string]bool{}
	for _, id := range dcIdentRE.FindAllString(q, -1) {
		parts := []string{id}
		if strings.ContainsAny(id, ".:") {
			f := strings.FieldsFunc(id, func(r rune) bool { return r == '.' || r == ':' })
			parts = append(parts, f[len(f)-1])
			if len(f) > 1 {
				parts = append(parts, f[len(f)-2])
			}
		}
		for _, p := range parts {
			lp := strings.ToLower(p)
			if seen[p] || dcStop[lp] || len(p) < 2 {
				continue
			}
			seen[p] = true
			syms = append(syms, p)
			if !seen["w:"+lp] && len(lp) >= 3 {
				seen["w:"+lp] = true
				words = append(words, lp)
			}
		}
	}
	return syms, words
}

// dcScore: an exact symbol in the title (a definition heading) counts most,
// then the symbol in the body, then shared words.
func dcScore(s *dcSection, syms []string, res []*regexp.Regexp, words []string) float64 {
	sc := 0.0
	title, body := s.Title, s.Body
	lt, lb := strings.ToLower(title), strings.ToLower(body)
	for i, sym := range syms {
		re := res[i]
		if re.MatchString(title) {
			sc += 10
			if dcDefRE.MatchString(title) {
				sc += 4
			}
		}
		if n := len(re.FindAllStringIndex(body, 8)); n > 0 {
			sc += 3 + 0.5*float64(min(n-1, 4))
		} else if strings.Contains(lb, strings.ToLower(sym)) {
			sc += 1
		}
	}
	for _, w := range words {
		if strings.Contains(lt, w) {
			sc += 1.5
		} else if strings.Contains(lb, w) {
			sc += 1
		}
	}
	if sc > 0 && len(body) < 40 {
		sc -= 1 // a bare heading
	}
	return sc
}

// dcSelect picks the best sections within max bytes; with no match it
// returns the opening sections (overview). total = sections in the doc.
func dcSelect(text, query string, limit int) (picked []dcSection, total int, others []string) {
	secs := dcSplit(text)
	total = len(secs)
	syms, words := dcTerms(query)
	res := make([]*regexp.Regexp, len(syms))
	for i, sym := range syms {
		res[i] = regexp.MustCompile(`(^|[^\w$])` + regexp.QuoteMeta(sym) + `($|[^\w$])`)
	}
	for i := range secs {
		secs[i].score = dcScore(&secs[i], syms, res, words)
	}
	ranked := append([]dcSection(nil), secs...)
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	matched := len(ranked) > 0 && ranked[0].score > 0
	if !matched {
		ranked = secs
	}
	used := 0
	chosen := map[int]bool{}
	for _, s := range ranked {
		if matched && s.score <= 0 {
			break
		}
		size := len(s.Title) + len(s.Body) + 8
		if used+size > limit {
			if used == 0 { // the best section alone is too big: cut it
				s.Body = s.Body[:min(len(s.Body), max(0, limit-len(s.Title)-40))] + "\n[…]"
				size = limit
			} else {
				continue
			}
		}
		picked = append(picked, s)
		chosen[s.idx] = true
		used += size
		if used >= limit-200 || len(picked) >= 8 {
			break
		}
	}
	if matched { // reading order reads better than score order
		sort.SliceStable(picked, func(i, j int) bool { return picked[i].idx < picked[j].idx })
	}
	seen := map[string]bool{}
	for _, s := range secs {
		if !chosen[s.idx] && s.Title != "" && !seen[s.Title] && len(others) < 25 {
			seen[s.Title] = true
			others = append(others, oneLine(s.Title, 60))
		}
	}
	return picked, total, others
}

// dcRender is the tool result: a header (library, version, source) and the
// selected sections, within dcMaxChars.
func dcRender(tg dcTarget, r dcResult, query string) (string, int, int) {
	ver := dcOrStr(r.Version, tg.Version)
	var h strings.Builder
	fmt.Fprintf(&h, "docs %s %s", tg.Lib, dcOrStr(ver, "(version unknown)"))
	if tg.VerFrom != "" && tg.VerFrom != "arg" {
		fmt.Fprintf(&h, " (pinned in %s)", tg.VerFrom)
	}
	fmt.Fprintf(&h, " · source: %s", r.Source)
	if r.URL != "" {
		fmt.Fprintf(&h, " · %s", r.URL)
	}
	if r.Note != "" {
		fmt.Fprintf(&h, " · %s", r.Note)
	}
	if tg.Version != "" && r.Version != "" && dcNormVer(tg.Version) != dcNormVer(r.Version) && tg.VerFrom != "" {
		fmt.Fprintf(&h, "\nWARNING: these docs are for %s but the project pins %s; check the API against the installed code.", r.Version, tg.Version)
	}
	head := h.String()
	picked, total, others := dcSelect(r.Text, query, dcMaxChars-len(head)-400)
	var b strings.Builder
	b.WriteString(head)
	if len(picked) == 0 {
		b.WriteString("\n\n(the doc is empty)")
		return b.String(), 0, total
	}
	if picked[0].score > 0 {
		fmt.Fprintf(&b, "\n%d of %d sections match %q:\n", len(picked), total, query)
	} else {
		fmt.Fprintf(&b, "\nNothing matched %q; the opening sections follow. Ask again with a symbol from the section list.\n", query)
	}
	for _, s := range picked {
		b.WriteString("\n")
		if s.Title != "" {
			b.WriteString("## " + s.Title + "\n")
		}
		b.WriteString(s.Body)
		b.WriteString("\n")
	}
	if len(others) > 0 {
		rest := "\nOther sections: " + strings.Join(others, " · ")
		if b.Len()+len(rest) > dcMaxChars {
			rest = rest[:max(0, dcMaxChars-b.Len()-3)] + "…"
		}
		b.WriteString(rest)
	}
	text := b.String()
	if len(text) > dcMaxChars {
		text = text[:dcMaxChars]
	}
	return text, len(picked), total
}

func dcNormVer(v string) string {
	return strings.TrimLeft(strings.TrimSpace(v), "v=^~ ")
}

// ---------- automatic hints (cache-safe: appended to a tool result) ----------

var (
	dcErrRE = regexp.MustCompile(`(?m)(AttributeError: |TypeError: |ImportError: cannot import name|has no attribute|unexpected keyword argument|positional argument|is not a function|is not a constructor|has no exported member|does not exist on type|does not provide an export named|undefined: \w+\.\w+|has no field or method|(too many|not enough) arguments in call|error\[E0(425|599|432|061|433)\]|NoMethodError|undefined method)`)
	// module names the error itself names
	dcErrModRE = []*regexp.Regexp{
		regexp.MustCompile(`module '([\w.]+)' has no attribute`),
		regexp.MustCompile(`cannot import name '\w+' from '([\w.]+)'`),
		regexp.MustCompile(`undefined: (\w+)\.\w+`),
		regexp.MustCompile(`([A-Za-z_$][\w$]*)\.[\w$.]+ is not a (?:function|constructor)`),
		regexp.MustCompile(`Module '"([^"]+)"' has no exported member`),
		regexp.MustCompile(`site-packages/([\w.-]+)/`),
		regexp.MustCompile(`node_modules/((?:@[\w.-]+/)?[\w.-]+)/`),
		regexp.MustCompile(`pkg/mod/([^@\s]+)@`),
		regexp.MustCompile(`File "(?:[^"]*/)?(\w+)/__init__\.py"`),
	}
	dcImportRE = regexp.MustCompile(`(?m)^\s*(?:from\s+([\w.]+)\s+import|import\s+([\w.]+)(?:\s+as\s+\w+)?\s*$)|require\(\s*['"]([^'"]+)['"]\s*\)|\bfrom\s+['"]([^'"./][^'"]*)['"]|^\s*"([\w.-]+\.[a-z]{2,}/[^"]+)"\s*$|^\s*use\s+(\w+)::`)
)

const dcHintMark = "docs disponíveis: use docs {library: "

// dcHintNote: after a result with an API error that involves a library the
// project pins (or has installed), point the model to the docs tool once per
// library and conversation. "" = nothing to add.
func (a *Agent) dcHintNote(tc ToolCall, out ToolOutput) string {
	if a.tools == nil || !dcToolOn() || !dcOn(dcSettings().Hints) || strings.HasPrefix(tc.Name, "mcp__") {
		return ""
	}
	switch CanonicalTool(tc.Name) {
	case "bash", "script":
	default:
		return ""
	}
	text := out.Text
	if len(text) > 24<<10 {
		text = text[len(text)-24<<10:]
	}
	if !dcErrRE.MatchString(text) {
		return ""
	}
	cands := dcErrLibs(text)
	cands = append(cands, a.dcTurnImports()...)
	root := a.tools.Root
	for _, c := range cands {
		tg, ok := dcHintTarget(root, c)
		if !ok {
			continue
		}
		mk := dcHintMark + fmt.Sprintf("%q", tg.Lib)
		if a.dcHinted(mk) {
			continue
		}
		v := "versão não fixada"
		if tg.Version != "" {
			v = "versão " + tg.Version + " detectada"
			if tg.VerFrom != "" && tg.VerFrom != "installed" {
				v += " em " + tg.VerFrom
			}
		}
		q := dcErrSymbol(text)
		if q == "" {
			q = "…"
		}
		a.ui.Notice("dica: docs de " + tg.Lib + " disponíveis")
		return fmt.Sprintf("\n\n<system-reminder>%s%q, query: %q} (%s)</system-reminder>", dcHintMark, tg.Lib, q, v)
	}
	return ""
}

// dcErrLibs: library names an error output mentions.
func dcErrLibs(text string) []string {
	var out []string
	for _, re := range dcErrModRE {
		for _, m := range re.FindAllStringSubmatch(text, 6) {
			n := strings.SplitN(m[1], ".", 2)[0]
			if strings.Contains(m[1], "/") {
				n = m[1]
			}
			out = append(out, n)
		}
	}
	return out
}

var dcSymRE = []*regexp.Regexp{
	regexp.MustCompile(`has no attribute '(\w+)'`),
	regexp.MustCompile(`cannot import name '(\w+)'`),
	regexp.MustCompile(`(\w+)\(\) got an unexpected keyword argument '(\w+)'`),
	regexp.MustCompile(`(\w+)\(\) (?:takes|missing)`),
	regexp.MustCompile(`([\w$.]+) is not a (?:function|constructor)`),
	regexp.MustCompile(`has no exported member '(\w+)'`),
	regexp.MustCompile(`Property '(\w+)' does not exist`),
	regexp.MustCompile(`undefined: \w+\.(\w+)`),
	regexp.MustCompile(`\.(\w+) undefined \(type`),
	regexp.MustCompile(`arguments in call to [\w.]*?(\w+)\b`),
	regexp.MustCompile(`no method named ` + "`" + `(\w+)` + "`"),
	regexp.MustCompile(`cannot find (?:function|value) ` + "`" + `(\w+)` + "`"),
}

// dcErrSymbol is the symbol the error is about (the query to suggest).
func dcErrSymbol(text string) string {
	for _, re := range dcSymRE {
		if m := re.FindStringSubmatch(text); m != nil {
			s := m[1]
			if len(m) > 2 && m[2] != "" {
				s += " " + m[2]
			}
			return s
		}
	}
	return ""
}

// dcTurnImports: libraries imported by the code the model wrote or edited
// since the last user message.
func (a *Agent) dcTurnImports() []string {
	var out []string
	for i := len(a.Msgs) - 1; i >= 0 && len(out) < 12; i-- {
		m := a.Msgs[i]
		if m.Role == "user" {
			break
		}
		for _, tc := range m.ToolCalls {
			if c := CanonicalTool(tc.Name); c != "write" && c != "edit" {
				continue
			}
			for _, im := range dcImportRE.FindAllStringSubmatch(tc.Args, 20) {
				for _, g := range im[1:] {
					if g != "" {
						g = strings.ReplaceAll(g, `\n`, "")
						if !strings.Contains(g, "/") || dcGoModRE.MatchString(g) {
							g = strings.SplitN(g, ".", 2)[0]
						}
						if strings.Contains(g, "/") && !dcGoModRE.MatchString(g) && !strings.HasPrefix(g, "@") {
							g = strings.SplitN(g, "/", 2)[0]
						}
						out = append(out, g)
					}
				}
			}
		}
	}
	return out
}

// dcHintTarget: c is a library the project pins or has installed/vendored.
func dcHintTarget(root, c string) (dcTarget, bool) {
	c = strings.TrimSpace(c)
	if c == "" || dcStdlib[c] {
		return dcTarget{}, false
	}
	deps := dpLoad(root)
	if d, ok := dpFind(deps, c); ok {
		return dcTarget{Lib: d.Name, Eco: d.Eco, Version: d.Version, VerFrom: d.From}, true
	}
	tg := dcTarget{Lib: c, Eco: dcGuessEco(root, c)}
	if p, v := dcLocalDir(root, tg); p != "" {
		tg.Version, tg.VerFrom = v, "installed"
		if v == "" {
			tg.VerFrom = ""
		}
		return tg, true
	}
	return dcTarget{}, false
}

// dcStdlib: standard-library modules never worth a docs hint.
var dcStdlib = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields("os sys re json math time datetime collections itertools functools typing pathlib subprocess unittest logging " +
		"string io random decimal fractions enum dataclasses abc copy shutil tempfile socket http urllib csv sqlite3 threading asyncio " +
		"fmt strings strconv errors bytes context sort sync path filepath regexp testing net bufio encoding fs " +
		"fs path util events stream crypto child_process assert std core alloc builtins self super this window document console Math Object Array String Number Promise JSON") {
		m[w] = true
	}
	return m
}()

func (a *Agent) dcHinted(mk string) bool {
	for i := len(a.Msgs) - 1; i >= 0; i-- {
		if m := a.Msgs[i]; (m.Role == "tool" || m.Role == "user") && strings.Contains(m.Content, mk) {
			return true
		}
	}
	return false
}

// ---------- user-message notes (cache-safe) ----------

// dcInject is appended to the user message of a turn: the project API map
// note ("APIs do projeto relacionadas") and, in plan mode for large tasks,
// the cached/local docs of up to 3 libraries the request names.
func (a *Agent) dcInject(ctx context.Context, input string) string {
	if a.sub || a.tools == nil {
		return ""
	}
	var parts []string
	s := dcSettings()
	bt := bgClassify(input)
	if dcOn(s.APIMap) && bt.Kind != "question" && (amCodeShaped(input) || !a.dcHadUserTurn()) {
		if n := amNote(ctx, a.tools, input); n != "" {
			parts = append(parts, n)
		}
	}
	if dcToolOn() && dcOn(s.Prefetch) && a.mode() == "plan" && bt.Size == 'L' {
		if n := dcPrefetch(ctx, a.tools.Root, input); n != "" {
			parts = append(parts, n)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "\n\n<system-reminder>\n" + strings.Join(parts, "\n\n") + "\n</system-reminder>"
}

var dcWordRE = regexp.MustCompile(`[@A-Za-z0-9][\w./@-]*[\w]`)

// dcPrefetch: local/cached docs (never the network) for up to 3 pinned
// libraries the request names, ~400 tokens each.
func dcPrefetch(ctx context.Context, root, input string) string {
	deps := dpLoad(root)
	if len(deps) == 0 {
		return ""
	}
	seen := map[string]bool{}
	var libs []dpDep
	for _, w := range dcWordRE.FindAllString(acStripReminders(input), -1) {
		if len(w) < 3 || dcStop[strings.ToLower(w)] {
			continue
		}
		d, ok := dpFind(deps, w)
		if !ok || seen[d.Eco+d.Name] {
			continue
		}
		seen[d.Eco+d.Name] = true
		libs = append(libs, d)
		if len(libs) == 3 {
			break
		}
	}
	var out []string
	for _, d := range libs {
		ctx2, cancel := context.WithTimeout(ctx, 3*time.Second)
		tg := dcTarget{Lib: d.Name, Eco: d.Eco, Version: d.Version, VerFrom: d.From}
		r := dcLookup(ctx2, root, tg, input, dcLookupOpts{LocalOnly: true})
		cancel()
		if r.Err != "" {
			continue
		}
		picked, _, _ := dcSelect(r.Text, input, 1600)
		if len(picked) == 0 || picked[0].score <= 0 {
			continue
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Docs %s %s (%s, %s):", d.Name, d.Version, d.From, r.Source)
		for _, s := range picked {
			b.WriteString("\n## " + s.Title + "\n" + s.Body)
		}
		out = append(out, b.String())
	}
	if len(out) == 0 {
		return ""
	}
	return "Docs pré-carregadas das bibliotecas citadas (versão do lockfile; para mais, use a ferramenta docs):\n" + strings.Join(out, "\n\n")
}

// dcHadUserTurn: the conversation already has a user message (the API map
// note then needs a code-shaped request: a re-map per chat turn is waste).
func (a *Agent) dcHadUserTurn() bool {
	for _, m := range a.Msgs {
		if m.Role == "user" {
			return true
		}
	}
	return false
}
