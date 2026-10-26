package main

// buildtarget.go: build-target inspection (red-team round 6, scenario 11).
//
// `make setup`, `npm run build`, `just deploy` ... look harmless to the command
// classifiers, yet the recipe behind them can be `curl … | sh`. Before such a
// command runs, btCheck reads the recipe from the build file (Makefile,
// package.json, justfile, Taskfile, Rakefile, build.gradle, pom.xml,
// Makefile.toml, tox.ini, noxfile.py), follows prerequisites and nested
// invocations, follows scripts the recipe calls inside the repository (and
// setup.py for `pip install .`), and runs what it finds through the SAME
// classifiers used for direct commands (dangerWholeSh, downloadExecReason,
// segmentDanger, inlineCodeDanger, EgressTarget).
//
// Two levels: CRITICAL (download-and-execute, secrets read and sent out,
// shell start-up files rewritten, hostile archives) is blocked in every mode,
// yolo and turbo included, with the steering message; HIGH (destructive
// commands, credentials read, data sent to a host that is not allowed) asks
// through the one-key widget in every mode. A clean target never prompts.
// The hook is in Agent.permit (agent.go); everything else lives here.

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	btMaxFile  = 1 << 20 // bytes read from one build file or script
	btMaxDepth = 4       // nested build-tool hops (make → npm → script …)
)

var (
	reBtEnv      = lazyRx(`(\b(env|printenv)\b|/proc/[^/\s]+/environ)[^\n]*\b(curl|wget|nc|ncat)\b|\b(curl|wget|nc|ncat)\b[^\n]*(\$\(\s*(env|printenv)\b|` + "`\\s*(env|printenv)\\b" + `)`)
	reBtDotfile  = lazyRx(`\.(bashrc|zshrc|zprofile|zshenv|bash_profile|bash_aliases|profile)\b|authorized_keys|\.config/fish`)
	reBtWrite    = lazyRx(`open\(|\.write|append|>>|writeFile|appendFile|File\.(open|write)`)
	reBtList     = lazyRx(`\[\s*(?:"(?:[^"\\]|\\.)*"|'[^']*')(?:\s*,\s*(?:"(?:[^"\\]|\\.)*"|'[^']*'))+\s*\]`)
	reBtMkAssign = lazyRx(`^(?:export\s+|override\s+)?([A-Za-z_][\w.]*)\s*([:?+!]{0,2}=)\s*(.*)$`)
	reBtShellFn  = lazyRx(`\$[({]shell\s+([^)}]*)[)}]`)
	reBtBrace    = lazyRx(`\{\{[^}]*\}\}`)
	reBtTomlStr  = lazyRx(`"((?:[^"\\]|\\.)*)"|'([^']*)'`)
	reBtYamlList = lazyRx(`^\[(.*)\]$`)
)

type btCtx struct {
	root, realRoot string
	allow          []string
	supplyCheck    func(string, string) string
	crit, high     []string
	seen           map[string]bool
}

// btCheck inspects the build targets, scripts and archives that cmd would run
// from dir. crit is the first CRITICAL finding, high the HIGH ones joined.
func btCheck(dir, cmd string, allow []string) (crit, high string) {
	return btCheckWithSupply(dir, cmd, allow, nil)
}

func btCheckWithSupply(dir, cmd string, allow []string, supply func(string, string) string) (crit, high string) {
	if cmd == "" || dir == "" {
		return "", ""
	}
	c := &btCtx{root: dir, allow: allow, seen: map[string]bool{}, supplyCheck: supply}
	c.realRoot = dir
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		c.realRoot = r
	}
	c.shell(cmd, dir, 0, "", false)
	if len(c.crit) > 0 {
		crit = c.crit[0]
	}
	if len(c.high) > 0 {
		high = strings.Join(c.high, "; ")
		if len(c.high) > 3 {
			high = strings.Join(c.high[:3], "; ") + "; …"
		}
	}
	return crit, high
}

func (c *btCtx) add(crit bool, label, why, snippet string) {
	msg := why
	if label != "" {
		msg = label + ": " + why
	}
	if snippet != "" {
		msg += " (`" + oneLine(snippet, 90) + "`)"
	}
	list := &c.high
	if crit {
		list = &c.crit
	}
	for _, m := range *list {
		if m == msg {
			return
		}
	}
	*list = append(*list, msg)
}

// ---------- shell bodies ----------

// btLines normalizes a recipe body: joins continuations, drops comments and
// blank lines, strips make/just line prefixes (@ - +).
func btLines(body string) []string {
	var out []string
	cur := ""
	for _, l := range strings.Split(strings.ReplaceAll(body, "\r", ""), "\n") {
		s := strings.TrimSpace(l)
		if cur != "" {
			s = cur + " " + s
			cur = ""
		}
		if strings.HasSuffix(s, "\\") {
			cur = strings.TrimSuffix(s, "\\")
			continue
		}
		s = strings.TrimLeft(s, "@-+ \t")
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		out = append(out, strings.ReplaceAll(s, "$$", "$"))
	}
	if cur != "" {
		out = append(out, strings.TrimLeft(cur, "@-+ \t"))
	}
	return out
}

var reBtSplit = lazyRx(`\s*(?:&&|\|\||;|\|)\s*`)

func btSegs(line string) []string {
	if pr := shellParse(line); pr.ok {
		return pr.segs
	}
	return reBtSplit.Split(line, -1)
}

// shell analyses a shell snippet: classify (when asked) every line and the
// whole, then follow each invocation (build tools, repo scripts, archives).
func (c *btCtx) shell(body, cwd string, depth int, label string, classify bool) {
	lines := btLines(body)
	if len(lines) == 0 {
		return
	}
	if classify {
		if len(lines) > 1 {
			c.classify(strings.Join(lines, "\n"), label, "", true)
		}
		for _, l := range lines {
			c.classify(l, label, l, true)
		}
	}
	for _, l := range lines {
		for _, seg := range btSegs(l) {
			t := stripWrappers(shellFields(seg))
			if len(t) == 0 {
				continue
			}
			if t[0] == "cd" && len(t) > 1 {
				cwd = c.cd(cwd, t[1])
				continue
			}
			c.invoke(t, cwd, depth, label)
		}
	}
}

func (c *btCtx) cd(cwd, p string) string {
	if p == "" || p == "-" || strings.ContainsAny(p, "$`~") {
		return cwd
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(cwd, p)
}

func btCritWhole(r string) bool {
	return !strings.HasPrefix(r, "apaga arquivos vindos da entrada")
}

// classify runs the direct-command classifiers over text.
func (c *btCtx) classify(text, label, snip string, egress bool) {
	crit := false
	if r := dangerWholeSh(text); r != "" {
		cr := btCritWhole(r)
		c.add(cr, label, r, snip)
		crit = crit || cr
	}
	if pr := shellParse(text); pr.ok {
		if r := downloadExecReason(pr.segs); r != "" {
			c.add(true, label, r, snip)
			crit = true
		}
		for _, seg := range pr.segs {
			if r := segmentDanger(shellFields(seg)); r != "" {
				c.add(false, label, r, seg)
			}
		}
		for _, code := range pr.code {
			if r := inlineCodeDanger(code); r != "" {
				c.add(!strings.Contains(r, "apaga"), label, r, snip)
			}
		}
	}
	if reBtEnv.MatchString(text) {
		c.add(true, label, "envia variáveis de ambiente (segredos) pela rede", snip)
		crit = true
	}
	if !crit && reSecretFile.MatchString(text) {
		c.add(false, label, "lê credenciais ou config sensível", snip)
	}
	if egress {
		if h, eg := EgressTarget(text); eg && !egressAllowed(h, c.allow) {
			c.add(false, label, "envia dados para fora da máquina (destino: "+h+")", snip)
		}
	}
}

// ---------- non-shell code (setup.py, noxfile, Rakefile, scripts) ----------

func (c *btCtx) code(src, label string, strict bool) {
	if r := inlineCodeDanger(src); r != "" {
		c.add(!strings.Contains(r, "apaga"), label, r, "")
	}
	for _, m := range reBtList.FindAllString(src, 40) {
		var parts []string
		for _, s := range reBtTomlStr.FindAllStringSubmatch(m, -1) {
			parts = append(parts, s[1]+s[2])
		}
		c.classify(strings.Join(parts, " "), label, strings.Join(parts, " "), strict)
	}
	if reBtDotfile.MatchString(src) && reBtWrite.MatchString(src) {
		c.add(true, label, "grava em arquivo de inicialização do shell ou chaves SSH", "")
	}
	if strict && reCodeNet.MatchString(src) {
		for _, h := range egURLsIn(src) {
			if !egLocal(h) && !egressAllowed(h, c.allow) {
				c.add(false, label, "acessa a rede (host: "+h+")", "")
				break
			}
		}
	}
}

// litCmds turns the string literals of one source line into a shell command:
// the argument after "-c" when present, otherwise the literals joined.
func litCmds(line string) string {
	var lits []string
	for _, m := range reCodeStr.FindAllStringSubmatch(line, 16) {
		s := m[1]
		if s == "" {
			s = m[2]
		}
		lits = append(lits, s)
	}
	for i, s := range lits {
		if s == "-c" && i+1 < len(lits) {
			return lits[i+1]
		}
	}
	return strings.Join(lits, " ")
}

// ---------- invocation dispatch ----------

var btInterp = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "source": true, ".": true,
	"node": true, "nodejs": true, "ruby": true, "perl": true, "php": true, "deno": true, "bun": false}

func (c *btCtx) invoke(t []string, cwd string, depth int, label string) {
	if depth > btMaxDepth || len(t) == 0 {
		return
	}
	w := filepath.Base(t[0])
	args := t[1:]
	if depth > 0 && c.supplyCheck != nil {
		if why := c.supplyCheck(strings.Join(t, " "), cwd); why != "" {
			c.add(false, label, "dependência usada por alvo exige confirmação: "+why, strings.Join(t, " "))
		}
	}
	switch {
	case w == "make" || w == "gmake":
		c.make(args, cwd, depth)
	case w == "npm" || w == "yarn" || w == "pnpm" || w == "bun":
		c.npmLike(w, args, cwd, depth)
	case w == "just":
		c.just(args, cwd, depth)
	case w == "task" || w == "go-task":
		c.taskfile(args, cwd, depth)
	case w == "rake":
		c.rake(args, cwd, depth)
	case w == "gradle" || w == "gradlew":
		c.gradle(args, cwd, depth)
	case w == "mvn" || w == "mvnw":
		c.maven(cwd, depth)
	case w == "makers" || w == "cargo-make" || (w == "cargo" && len(args) > 0 && args[0] == "make"):
		if w == "cargo" {
			args = args[1:]
		}
		c.cargoMake(args, cwd, depth)
	case w == "tox":
		c.tox(args, cwd, depth)
	case w == "nox":
		c.nox(cwd, depth)
	case w == "tar" || w == "gtar" || w == "bsdtar" || w == "unzip":
		c.archive(w, args, cwd)
	case strings.HasPrefix(w, "pip") || (strings.HasPrefix(w, "python") && len(args) > 1 && args[0] == "-m" && args[1] == "pip"):
		if strings.HasPrefix(w, "python") {
			args = args[2:]
		}
		c.pip(args, cwd, depth)
	case btInterp[w] || strings.HasPrefix(w, "python"):
		for i, a := range args {
			if a == "-c" || a == "-m" || a == "-e" || a == "-" {
				return
			}
			if strings.HasPrefix(a, "-") {
				continue
			}
			c.script(a, cwd, depth, w)
			_ = i
			return
		}
	case strings.Contains(t[0], "/"):
		c.script(t[0], cwd, depth, "")
	}
}

// ---------- scripts inside the repo ----------

func (c *btCtx) readFile(p string) (string, bool) {
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > btMaxFile {
		return "", false
	}
	b, err := os.ReadFile(p)
	if err != nil || strings.IndexByte(string(b[:min(len(b), 4096)]), 0) >= 0 {
		return "", false
	}
	return string(b), true
}

func (c *btCtx) first(dir string, names ...string) (string, string) {
	for _, n := range names {
		p := filepath.Join(dir, n)
		if s, ok := c.readFile(p); ok {
			return p, s
		}
	}
	return "", ""
}

// script follows a file the command runs when it lives inside the project.
func (c *btCtx) script(p, cwd string, depth int, interp string) {
	if p == "" || strings.ContainsAny(p, "$`") {
		return
	}
	abs := p
	if !filepath.IsAbs(p) {
		abs = filepath.Join(cwd, p)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return
	}
	if !pathUnder(real, c.realRoot) {
		if pathUnder(abs, c.root) || pathUnder(abs, c.realRoot) {
			c.add(false, "", "o script "+filepath.Base(p)+" é um symlink para fora do projeto ("+real+")", "")
		}
		return
	}
	key := "script\x00" + real + "\x00" + filepath.Clean(cwd)
	if c.seen[key] {
		return
	}
	c.seen[key] = true
	src, ok := c.readFile(real)
	if !ok {
		return
	}
	label := "script " + filepath.ToSlash(relTo(c.realRoot, real))
	base := strings.ToLower(filepath.Base(real))
	first, _, _ := strings.Cut(src, "\n")
	isCode := strings.HasSuffix(base, ".py") || strings.HasSuffix(base, ".js") || strings.HasSuffix(base, ".mjs") || strings.HasSuffix(base, ".cjs") ||
		strings.HasSuffix(base, ".ts") || strings.HasSuffix(base, ".rb") || strings.HasSuffix(base, ".pl") || strings.HasSuffix(base, ".php") ||
		strings.HasPrefix(first, "#!") && regexp.MustCompile(`python|node|ruby|perl|php|deno`).MatchString(first) ||
		strings.HasPrefix(interp, "python") || interp == "node" || interp == "nodejs" || interp == "ruby" || interp == "perl" || interp == "php"
	if isCode {
		strict := depth > 0 || base == "setup.py" || base == "noxfile.py" || base == "conftest.py"
		c.code(src, label, strict)
		return
	}
	// Scripts inherit the caller's working directory. Resolving nested relative
	// paths from the script's own directory misses commands such as
	// `bash scripts/run.sh` followed by `bash scripts/payload.sh`.
	c.shell(src, cwd, depth+1, label, true)
}

func relTo(root, p string) string {
	if r, err := filepath.Rel(root, p); err == nil {
		return r
	}
	return p
}

// pip: `pip install .` / `-e .` / a project directory runs its setup.py.
func (c *btCtx) pip(args []string, cwd string, depth int) {
	install := false
	for _, a := range args {
		if a == "install" {
			install = true
			break
		}
	}
	if !install {
		return
	}
	for _, a := range args {
		if strings.HasPrefix(a, "-") || a == "install" || strings.Contains(a, "://") {
			continue
		}
		d := c.cd(cwd, a)
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			if _, err := os.Stat(filepath.Join(d, "setup.py")); err == nil {
				c.script(filepath.Join(d, "setup.py"), d, depth, "python")
			}
		}
	}
}

// ---------- archives ----------

func btEscapes(entryName, target string) bool {
	if filepath.IsAbs(target) || strings.HasPrefix(target, "/") {
		return true
	}
	j := filepath.Clean(filepath.Join(filepath.Dir(filepath.FromSlash(entryName)), filepath.FromSlash(target)))
	return j == ".." || strings.HasPrefix(j, ".."+string(filepath.Separator))
}

func btBadName(n string) bool {
	if strings.HasPrefix(n, "/") || filepath.IsAbs(n) {
		return true
	}
	for _, p := range strings.Split(filepath.ToSlash(n), "/") {
		if p == ".." {
			return true
		}
	}
	return false
}

// archive refuses to extract a tar/zip whose entries escape the destination
// (absolute or ../ names, symlinks and hard links pointing outside).
func (c *btCtx) archive(w string, args []string, cwd string) {
	if w == "tar" || w == "gtar" || w == "bsdtar" {
		extract := false
		for _, a := range args {
			if a == "--extract" || a == "--get" || (strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsAny(a, "x")) ||
				(!strings.HasPrefix(a, "-") && regexp.MustCompile(`^[a-zA-Z]*x[a-zA-Z]*$`).MatchString(a)) {
				extract = true
			}
		}
		if !extract {
			return
		}
	}
	for _, a := range args {
		l := strings.ToLower(a)
		if !(strings.HasSuffix(l, ".tar") || strings.HasSuffix(l, ".tar.gz") || strings.HasSuffix(l, ".tgz") || strings.HasSuffix(l, ".tar.bz2") || strings.HasSuffix(l, ".tbz2") || strings.HasSuffix(l, ".zip")) {
			continue
		}
		p := a
		if !filepath.IsAbs(p) {
			p = filepath.Join(cwd, p)
		}
		if why := btArchiveReason(p); why != "" {
			c.add(true, "arquivo "+filepath.Base(a), why, "")
		}
	}
}

func btArchiveReason(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	if strings.HasSuffix(strings.ToLower(p), ".zip") {
		fi, err := f.Stat()
		if err != nil {
			return ""
		}
		zr, err := zip.NewReader(f, fi.Size())
		if err != nil {
			return ""
		}
		for _, e := range zr.File {
			if btBadName(e.Name) {
				return "contém um caminho que escapa da pasta de destino (" + e.Name + ")"
			}
			if e.Mode()&os.ModeSymlink != 0 {
				if rc, err := e.Open(); err == nil {
					b, _ := io.ReadAll(io.LimitReader(rc, 4096))
					rc.Close()
					if btEscapes(e.Name, string(b)) {
						return "contém um symlink que aponta para fora da pasta (" + e.Name + " → " + string(b) + ")"
					}
				}
			}
		}
		return ""
	}
	var r io.Reader = f
	l := strings.ToLower(p)
	switch {
	case strings.HasSuffix(l, ".gz") || strings.HasSuffix(l, ".tgz"):
		gz, err := gzip.NewReader(f)
		if err != nil {
			return ""
		}
		r = gz
	case strings.HasSuffix(l, ".bz2") || strings.HasSuffix(l, ".tbz2"):
		r = bzip2.NewReader(f)
	}
	tr := tar.NewReader(r)
	for i := 0; i < 20000; i++ {
		h, err := tr.Next()
		if err != nil {
			return ""
		}
		if btBadName(h.Name) {
			return "contém um caminho que escapa da pasta de destino (" + h.Name + ")"
		}
		if (h.Typeflag == tar.TypeSymlink || h.Typeflag == tar.TypeLink) && btEscapes(h.Name, h.Linkname) {
			return "contém um symlink que aponta para fora da pasta (" + h.Name + " → " + h.Linkname + ")"
		}
	}
	return ""
}

// ---------- make ----------

type mkRule struct {
	deps []string
	body []string
}

func (c *btCtx) make(args []string, cwd string, depth int) {
	dir, file := cwd, ""
	var targets []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case (a == "-C" || a == "--directory") && i+1 < len(args):
			dir = c.cd(cwd, args[i+1])
			i++
		case (a == "-f" || a == "--file" || a == "--makefile") && i+1 < len(args):
			file = args[i+1]
			i++
		case strings.HasPrefix(a, "--directory="):
			dir = c.cd(cwd, strings.TrimPrefix(a, "--directory="))
		case strings.HasPrefix(a, "--file="):
			file = strings.TrimPrefix(a, "--file=")
		case strings.HasPrefix(a, "-C") && len(a) > 2:
			dir = c.cd(cwd, a[2:])
		case strings.HasPrefix(a, "-f") && len(a) > 2:
			file = a[2:]
		case strings.HasPrefix(a, "-"), strings.Contains(a, "="):
		case regexp.MustCompile(`^\d+$`).MatchString(a):
		default:
			targets = append(targets, a)
		}
	}
	var p, src string
	if file != "" {
		p, src = c.first(dir, file)
	} else {
		p, src = c.first(dir, "GNUmakefile", "makefile", "Makefile")
	}
	if src == "" {
		return
	}
	key := "make\x00" + p + "\x00" + strings.Join(targets, ",")
	if c.seen[key] {
		return
	}
	c.seen[key] = true
	rules, order, vars, eager := parseMake(src)
	name := filepath.Base(p)
	for _, e := range eager {
		c.shell(e, dir, depth+1, name+" (avaliado ao carregar)", true)
	}
	if len(targets) == 0 && len(order) > 0 {
		targets = []string{order[0]}
	}
	done := map[string]bool{}
	var visit func(t string, d int)
	visit = func(t string, d int) {
		r := rules[t]
		if r == nil || done[t] || d > 6 {
			return
		}
		done[t] = true
		for _, dep := range r.deps {
			visit(dep, d+1)
		}
		var body []string
		for _, l := range r.body {
			body = append(body, expandMk(l, vars))
		}
		c.shell(strings.Join(body, "\n"), dir, depth+1, "alvo `"+t+"` do "+name, true)
	}
	for _, t := range targets {
		visit(t, 0)
	}
}

func expandMk(s string, vars map[string]string) string {
	s = strings.NewReplacer("$(MAKE)", "make", "${MAKE}", "make").Replace(s)
	for i := 0; i < 3 && strings.Contains(s, "$"); i++ {
		old := s
		for k, v := range vars {
			s = strings.NewReplacer("$("+k+")", v, "${"+k+"}", v).Replace(s)
		}
		if s == old {
			break
		}
	}
	return s
}

func parseMake(src string) (rules map[string]*mkRule, order []string, vars map[string]string, eager []string) {
	rules, vars = map[string]*mkRule{}, map[string]string{}
	var lines []string
	cur := ""
	for _, l := range strings.Split(strings.ReplaceAll(src, "\r", ""), "\n") {
		if cur != "" {
			l = cur + " " + strings.TrimSpace(l)
			cur = ""
		}
		if strings.HasSuffix(l, "\\") {
			cur = strings.TrimSuffix(l, "\\")
			continue
		}
		lines = append(lines, l)
	}
	var curRules []*mkRule
	for _, l := range lines {
		if strings.HasPrefix(l, "\t") {
			for _, r := range curRules {
				r.body = append(r.body, l[1:])
			}
			continue
		}
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		curRules = nil
		for _, m := range reBtShellFn.FindAllStringSubmatch(t, -1) {
			eager = append(eager, m[1])
		}
		if m := reBtMkAssign.FindStringSubmatch(t); m != nil {
			vars[m[1]] = m[3]
			if m[2] == "!=" {
				eager = append(eager, m[3])
			}
			continue
		}
		if regexp.MustCompile(`^(ifeq|ifneq|ifdef|ifndef|else|endif|include|-include|sinclude|define|endef|export|unexport|vpath|override)\b`).MatchString(t) {
			continue
		}
		i := strings.Index(t, ":")
		if i <= 0 {
			continue
		}
		left, right := expandMk(t[:i], vars), t[i+1:]
		right = strings.TrimPrefix(right, ":")
		inline := ""
		if j := strings.Index(right, ";"); j >= 0 {
			right, inline = right[:j], right[j+1:]
		}
		var deps []string
		for _, d := range strings.Fields(expandMk(right, vars)) {
			if d != "|" {
				deps = append(deps, d)
			}
		}
		for _, tg := range strings.Fields(left) {
			if strings.ContainsAny(tg, "%$") {
				continue
			}
			r := rules[tg]
			if r == nil {
				r = &mkRule{}
				rules[tg] = r
				if !strings.HasPrefix(tg, ".") {
					order = append(order, tg)
				}
			}
			r.deps = append(r.deps, deps...)
			if inline != "" {
				r.body = append(r.body, inline)
			}
			curRules = append(curRules, r)
		}
	}
	return
}

// ---------- npm / yarn / pnpm / bun ----------

var btPkgBuiltins = map[string]bool{"install": true, "i": true, "add": true, "remove": true, "rm": true, "up": true, "upgrade": true, "update": true,
	"init": true, "link": true, "unlink": true, "publish": true, "pack": true, "audit": true, "outdated": true, "why": true, "ls": true, "list": true,
	"info": true, "config": true, "cache": true, "dlx": true, "exec": true, "create": true, "import": true, "fetch": true, "rebuild": true,
	"store": true, "prune": true, "dedupe": true, "patch": true, "set": true, "workspace": true, "workspaces": true, "global": true, "bin": true,
	"login": true, "logout": true, "version": true, "help": true, "x": true, "ci": true, "uninstall": true, "run": true, "run-script": true}

func (c *btCtx) npmLike(w string, args []string, cwd string, depth int) {
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		if a == "--prefix" || a == "--cwd" || a == "-C" {
			if i+1 < len(args) {
				cwd = c.cd(cwd, args[i+1])
				i++
			}
			continue
		}
		if strings.HasPrefix(a, "--prefix=") || strings.HasPrefix(a, "--cwd=") {
			cwd = c.cd(cwd, strings.SplitN(a, "=", 2)[1])
			continue
		}
		if !strings.HasPrefix(a, "-") {
			pos = append(pos, a)
		}
	}
	if len(pos) == 0 {
		return
	}
	sub, name := pos[0], ""
	switch {
	case sub == "run" || sub == "run-script" || sub == "rum" || sub == "urn":
		if len(pos) > 1 {
			name = pos[1]
		}
	case w == "npm" && (sub == "test" || sub == "t" || sub == "tst"):
		name = "test"
	case w == "npm" && (sub == "start" || sub == "stop" || sub == "restart"):
		name = sub
	case w != "npm" && w != "bun" && !btPkgBuiltins[sub]:
		name = sub
	}
	if name == "" {
		return
	}
	_, src := c.first(cwd, "package.json")
	if src == "" {
		return
	}
	key := "pkg\x00" + cwd + "\x00" + name
	if c.seen[key] {
		return
	}
	c.seen[key] = true
	var doc struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal([]byte(src), &doc) != nil {
		return
	}
	for _, n := range []string{"pre" + name, name, "post" + name} {
		if b := doc.Scripts[n]; b != "" {
			c.shell(b, cwd, depth+1, "script `"+n+"` do package.json", true)
		}
	}
}

// ---------- just ----------

type jfRecipe struct {
	deps []string
	body []string
}

func (c *btCtx) just(args []string, cwd string, depth int) {
	dir, file := cwd, ""
	var names []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case (a == "-f" || a == "--justfile") && i+1 < len(args):
			file = args[i+1]
			i++
		case (a == "-d" || a == "--working-directory") && i+1 < len(args):
			dir = c.cd(cwd, args[i+1])
			i++
		case strings.HasPrefix(a, "-"), strings.Contains(a, "="):
		default:
			names = append(names, a)
		}
	}
	var p, src string
	if file != "" {
		p, src = c.first(cwd, file)
	} else {
		p, src = c.first(dir, "justfile", "Justfile", ".justfile")
	}
	if src == "" {
		return
	}
	key := "just\x00" + p + "\x00" + strings.Join(names, ",")
	if c.seen[key] {
		return
	}
	c.seen[key] = true
	recipes, order, vars, eager := parseJust(src)
	for _, e := range eager {
		c.shell(e, dir, depth+1, "justfile (avaliado ao carregar)", true)
	}
	if len(names) == 0 && len(order) > 0 {
		names = []string{order[0]}
	}
	done := map[string]bool{}
	var visit func(n string, d int)
	visit = func(n string, d int) {
		r := recipes[n]
		if r == nil || done[n] || d > 6 {
			return
		}
		done[n] = true
		for _, dep := range r.deps {
			visit(dep, d+1)
		}
		body := strings.Join(r.body, "\n")
		for k, v := range vars {
			body = strings.ReplaceAll(body, "{{"+k+"}}", v)
			body = strings.ReplaceAll(body, "{{ "+k+" }}", v)
		}
		body = reBtBrace.ReplaceAllString(body, "x")
		lbl := "receita `" + n + "` do justfile"
		c.shell(body, dir, depth+1, lbl, true)
		if first := strings.TrimSpace(body); strings.HasPrefix(first, "#!") && !regexp.MustCompile(`^#!\s*(/usr/bin/env\s+)?(ba|z|da|k)?sh\b|^#!/\S*/(ba|z|da|k)?sh\b`).MatchString(first) {
			c.code(body, lbl, true)
		}
	}
	for _, n := range names {
		visit(n, 0)
	}
}

func parseJust(src string) (recipes map[string]*jfRecipe, order []string, vars map[string]string, eager []string) {
	recipes, vars = map[string]*jfRecipe{}, map[string]string{}
	var cur *jfRecipe
	reAssign := regexp.MustCompile(`^(?:export\s+)?([A-Za-z_][\w-]*)\s*:=\s*(.*)$`)
	for _, l := range strings.Split(strings.ReplaceAll(src, "\r", ""), "\n") {
		if strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t") {
			if cur != nil {
				cur.body = append(cur.body, l)
			}
			continue
		}
		if strings.TrimSpace(l) == "" {
			if cur != nil {
				cur.body = append(cur.body, "")
			}
			continue
		}
		cur = nil
		if strings.HasPrefix(l, "#") || strings.HasPrefix(l, "[") || regexp.MustCompile(`^(set|alias|import|mod)\b`).MatchString(l) {
			continue
		}
		if m := reAssign.FindStringSubmatch(l); m != nil {
			v := strings.TrimSpace(m[2])
			vars[m[1]] = strings.Trim(v, `"'`)
			for _, bt := range regexp.MustCompile("`([^`]*)`").FindAllStringSubmatch(v, -1) {
				eager = append(eager, bt[1])
			}
			continue
		}
		i := strings.Index(l, ":")
		if i <= 0 || (i+1 < len(l) && l[i+1] == '=') {
			continue
		}
		fields := strings.Fields(l[:i])
		if len(fields) == 0 {
			continue
		}
		name := strings.TrimPrefix(fields[0], "@")
		r := &jfRecipe{}
		for _, d := range strings.Fields(strings.ReplaceAll(l[i+1:], "&&", " ")) {
			d = strings.Trim(d, "()")
			if d != "" && !strings.ContainsAny(d, "=\"'") {
				r.deps = append(r.deps, d)
			}
		}
		recipes[name] = r
		order = append(order, name)
		cur = r
	}
	return
}

// ---------- Taskfile (go-task) ----------

func yIndent(l string) int { return len(l) - len(strings.TrimLeft(l, " \t")) }

func yUnquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}

func yList(v string) []string {
	m := reBtYamlList.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return nil
	}
	var out []string
	for _, p := range strings.Split(m[1], ",") {
		if p = yUnquote(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (c *btCtx) taskfile(args []string, cwd string, depth int) {
	var names []string
	file, dir := "", cwd
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		if (a == "-t" || a == "--taskfile" || a == "-d" || a == "--dir") && i+1 < len(args) {
			v := args[i+1]
			if a == "-t" || a == "--taskfile" {
				file = v
			} else {
				dir = c.cd(cwd, v)
			}
			i++
			continue
		}
		if strings.HasPrefix(a, "--taskfile=") {
			file = strings.TrimPrefix(a, "--taskfile=")
			continue
		}
		if strings.HasPrefix(a, "--dir=") {
			dir = c.cd(cwd, strings.TrimPrefix(a, "--dir="))
			continue
		}
		if !strings.HasPrefix(a, "-") && !strings.Contains(a, "=") {
			names = append(names, a)
		}
	}
	var p, src string
	if file != "" {
		p, src = c.first(dir, file)
	} else {
		p, src = c.first(dir, "Taskfile.yml", "Taskfile.yaml", "taskfile.yml", "taskfile.yaml", "Taskfile.dist.yml", "Taskfile.dist.yaml")
	}
	if src == "" {
		return
	}
	if len(names) == 0 {
		names = []string{"default"}
	}
	key := "task\x00" + p + "\x00" + strings.Join(names, ",")
	if c.seen[key] {
		return
	}
	c.seen[key] = true
	tasks := parseTaskfile(src)
	done := map[string]bool{}
	var visit func(n string, d int)
	visit = func(n string, d int) {
		t := tasks[n]
		if t == nil || done[n] || d > 6 {
			return
		}
		done[n] = true
		for _, dep := range t.deps {
			visit(dep, d+1)
		}
		c.shell(strings.Join(t.cmds, "\n"), dir, depth+1, "tarefa `"+n+"` do "+filepath.Base(p), true)
	}
	for _, n := range names {
		visit(n, 0)
	}
}

type tfTask struct{ cmds, deps []string }

func parseTaskfile(src string) map[string]*tfTask {
	out := map[string]*tfTask{}
	lines := strings.Split(strings.ReplaceAll(src, "\r", ""), "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimRight(l, " \t") == "tasks:" {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return out
	}
	var blocks [][]string
	var names []string
	tIndent := -1
	for _, l := range lines[start:] {
		s := strings.TrimSpace(l)
		if s == "" || strings.HasPrefix(s, "#") {
			if len(blocks) > 0 {
				blocks[len(blocks)-1] = append(blocks[len(blocks)-1], l)
			}
			continue
		}
		ind := yIndent(l)
		if ind == 0 {
			break
		}
		if tIndent < 0 {
			tIndent = ind
		}
		if ind == tIndent && !strings.HasPrefix(s, "-") {
			k, v, _ := strings.Cut(s, ":")
			names = append(names, yUnquote(k))
			b := []string{}
			if v = strings.TrimSpace(v); v != "" && !strings.HasPrefix(v, "|") && !strings.HasPrefix(v, ">") && !strings.HasPrefix(v, "{") {
				b = append(b, strings.Repeat(" ", ind+2)+"cmd: "+v)
			}
			blocks = append(blocks, b)
			continue
		}
		if len(blocks) > 0 {
			blocks[len(blocks)-1] = append(blocks[len(blocks)-1], l)
		}
	}
	for i, blk := range blocks {
		t := &tfTask{}
		mode, modeInd := "", 0
		for j := 0; j < len(blk); j++ {
			l := blk[j]
			s := strings.TrimSpace(l)
			if s == "" || strings.HasPrefix(s, "#") {
				continue
			}
			ind := yIndent(l)
			block := func(from int, base int) string {
				var sb []string
				for k := from; k < len(blk); k++ {
					if strings.TrimSpace(blk[k]) != "" && yIndent(blk[k]) <= base {
						break
					}
					sb = append(sb, strings.TrimSpace(blk[k]))
					j = k
				}
				return strings.Join(sb, "\n")
			}
			if s == "-" || strings.HasPrefix(s, "- ") {
				item := strings.TrimSpace(strings.TrimPrefix(s, "-"))
				k, v, hasKey := strings.Cut(item, ":")
				switch {
				case hasKey && (k == "task") && !strings.ContainsAny(k, " "):
					t.deps = append(t.deps, yUnquote(v))
				case hasKey && (k == "cmd" || k == "defer") && !strings.ContainsAny(k, " "):
					if v = strings.TrimSpace(v); strings.HasPrefix(v, "|") || strings.HasPrefix(v, ">") {
						t.cmds = append(t.cmds, block(j+1, ind))
					} else if mode == "cmds" {
						t.cmds = append(t.cmds, yUnquote(v))
					}
				case item == "|" || item == ">" || strings.HasPrefix(item, "|-") || strings.HasPrefix(item, ">-"):
					t.cmds = append(t.cmds, block(j+1, ind))
				case mode == "cmds" && !hasKey || mode == "cmds" && strings.HasPrefix(item, "\""):
					t.cmds = append(t.cmds, yUnquote(item))
				case mode == "cmds" && hasKey && strings.ContainsAny(k, " \"'"):
					t.cmds = append(t.cmds, yUnquote(item))
				case mode == "deps":
					t.deps = append(t.deps, yUnquote(item))
				}
				continue
			}
			k, v, _ := strings.Cut(s, ":")
			v = strings.TrimSpace(v)
			switch k {
			case "cmds":
				mode, modeInd = "cmds", ind
				t.cmds = append(t.cmds, yList(v)...)
			case "deps":
				mode, modeInd = "deps", ind
				t.deps = append(t.deps, yList(v)...)
			case "cmd":
				if strings.HasPrefix(v, "|") || strings.HasPrefix(v, ">") {
					t.cmds = append(t.cmds, block(j+1, ind))
				} else if v != "" {
					t.cmds = append(t.cmds, yUnquote(v))
				}
			case "task":
				t.deps = append(t.deps, yUnquote(v))
			default:
				if ind <= modeInd {
					mode = ""
				}
			}
		}
		out[names[i]] = t
	}
	return out
}

// ---------- rake ----------

func (c *btCtx) rake(args []string, cwd string, depth int) {
	var names []string
	for _, a := range args {
		if !strings.HasPrefix(a, "-") && !strings.Contains(a, "=") {
			names = append(names, a)
		}
	}
	if len(names) == 0 {
		names = []string{"default"}
	}
	_, src := c.first(cwd, "Rakefile", "rakefile", "Rakefile.rb", "rakefile.rb")
	if m, _ := filepath.Glob(filepath.Join(cwd, "rakelib", "*.rake")); len(m) > 0 {
		for _, f := range m[:min(len(m), 20)] {
			if s, ok := c.readFile(f); ok {
				src += "\n" + s
			}
		}
	}
	if src == "" {
		return
	}
	key := "rake\x00" + cwd + "\x00" + strings.Join(names, ",")
	if c.seen[key] {
		return
	}
	c.seen[key] = true
	done := map[string]bool{}
	var visit func(n string, d int)
	visit = func(n string, d int) {
		if i := strings.LastIndex(n, ":"); i >= 0 {
			n = n[i+1:]
		}
		if done[n] || d > 6 {
			return
		}
		done[n] = true
		re := regexp.MustCompile(`(?m)^[ \t]*task\s*\(?\s*[:'"]?` + regexp.QuoteMeta(n) + `\b`)
		loc := re.FindStringIndex(src)
		if loc == nil {
			return
		}
		rest := src[loc[0]:]
		end := len(rest)
		if nx := regexp.MustCompile(`(?m)^[ \t]{0,2}(task|desc|namespace|file|directory|rule)\b`).FindAllStringIndex(rest, 2); len(nx) > 1 {
			end = nx[1][0]
		}
		blk := rest[:end]
		hdr, _, _ := strings.Cut(blk, "\n")
		for _, m := range regexp.MustCompile(`:(\w+)`).FindAllStringSubmatch(hdr, -1) {
			if m[1] != n {
				visit(m[1], d+1)
			}
		}
		lbl := "tarefa `" + n + "` do Rakefile"
		for _, l := range strings.Split(blk, "\n") {
			if !regexp.MustCompile("\\b(sh|system|exec|spawn)\\b|`|%x").MatchString(l) {
				continue
			}
			if s := litCmds(l); s != "" {
				c.shell(s, cwd, depth+1, lbl, true)
			}
			for _, m := range regexp.MustCompile("`([^`]+)`|%x[\\[({]([^\\])}]+)").FindAllStringSubmatch(l, -1) {
				c.shell(m[1]+m[2], cwd, depth+1, lbl, true)
			}
		}
		c.code(blk, lbl, true)
	}
	for _, n := range names {
		visit(n, 0)
	}
}

// ---------- gradle ----------

func (c *btCtx) gradle(args []string, cwd string, depth int) {
	var names []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if (a == "-p" || a == "--project-dir" || a == "-b" || a == "-c") && i+1 < len(args) {
			i++
			continue
		}
		if !strings.HasPrefix(a, "-") && !strings.Contains(a, "=") {
			names = append(names, a)
		}
	}
	p, src := c.first(cwd, "build.gradle", "build.gradle.kts")
	if src == "" {
		return
	}
	key := "gradle\x00" + p + "\x00" + strings.Join(names, ",")
	if c.seen[key] {
		return
	}
	c.seen[key] = true
	var analyze func(blk, lbl string)
	analyze = func(blk, lbl string) {
		for _, l := range strings.Split(blk, "\n") {
			if regexp.MustCompile(`commandLine|\.execute\(|\bexec\(|ProcessBuilder|Runtime\.getRuntime`).MatchString(l) {
				if s := litCmds(l); s != "" {
					c.shell(s, cwd, depth+1, lbl, true)
				}
			}
		}
		c.code(blk, lbl, true)
	}
	found := false
	done := map[string]bool{}
	var visit func(n string, d int)
	visit = func(n string, d int) {
		n = strings.TrimPrefix(n, ":")
		if done[n] || d > 6 {
			return
		}
		done[n] = true
		blk := gradleBlock(src, n)
		if blk == "" {
			return
		}
		found = true
		for _, m := range regexp.MustCompile(`(?:dependsOn|finalizedBy|mustRunAfter)\s*[(=\s\[]\s*([^\n)]*)`).FindAllStringSubmatch(blk, -1) {
			for _, s := range reBtTomlStr.FindAllStringSubmatch(m[1], -1) {
				visit(s[1]+s[2], d+1)
			}
		}
		analyze(blk, "tarefa `"+n+"` do "+filepath.Base(p))
	}
	for _, n := range names {
		visit(n, 0)
	}
	if !found {
		analyze(src, filepath.Base(p))
	}
}

func gradleBlock(src, name string) string {
	q := regexp.QuoteMeta(name)
	re := regexp.MustCompile(`(?m)(?:\btask\s*\(?\s*["']?` + q + `["']?|\b(?:register|create|named)(?:<[^>]*>)?\s*\(\s*["']` + q + `["']|\b` + q + `\s*\.\s*(?:doLast|doFirst)|\b` + q + `\s*\{)[^\n{]*\{`)
	loc := re.FindStringIndex(src)
	if loc == nil {
		return ""
	}
	depth := 0
	end := min(len(src), loc[1]+30000)
	for i := loc[1] - 1; i < end; i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[loc[0] : i+1]
			}
		}
	}
	return src[loc[0]:end]
}

// ---------- maven ----------

func (c *btCtx) maven(cwd string, depth int) {
	p, src := c.first(cwd, "pom.xml")
	if src == "" || !regexp.MustCompile(`exec-maven-plugin|maven-antrun-plugin|groovy-maven|gmavenplus|<executable>`).MatchString(src) {
		return
	}
	if c.seen["mvn\x00"+p] {
		return
	}
	c.seen["mvn\x00"+p] = true
	x := strings.NewReplacer("&quot;", `"`, "&apos;", "'", "&amp;", "&", "&lt;", "<", "&gt;", ">", "<![CDATA[", "", "]]>", "")
	lbl := "pom.xml"
	for _, m := range regexp.MustCompile(`(?s)<configuration>(.*?)</configuration>`).FindAllStringSubmatch(src, -1) {
		cfg := m[1]
		em := regexp.MustCompile(`<executable>([^<]+)</executable>`).FindStringSubmatch(cfg)
		if em != nil {
			parts := []string{strings.TrimSpace(x.Replace(em[1]))}
			for _, a := range regexp.MustCompile(`<argument>([^<]*)</argument>`).FindAllStringSubmatch(cfg, -1) {
				parts = append(parts, x.Replace(a[1]))
			}
			for _, a := range regexp.MustCompile(`<commandlineArgs>([^<]*)</commandlineArgs>`).FindAllStringSubmatch(cfg, -1) {
				parts = append(parts, x.Replace(a[1]))
			}
			cmd := strings.Join(parts, " ")
			if i := indexOf(parts, "-c"); i >= 0 && i+1 < len(parts) {
				cmd = strings.Join(parts[i+1:], " ")
			}
			c.shell(cmd, cwd, depth+1, lbl, true)
		}
	}
	for _, m := range regexp.MustCompile(`(?s)<exec\b([^>]*?)(?:/>|>(.*?)</exec>)`).FindAllStringSubmatch(src, -1) {
		ex := regexp.MustCompile(`executable="([^"]+)"`).FindStringSubmatch(m[1])
		if ex == nil {
			continue
		}
		parts := []string{x.Replace(ex[1])}
		for _, a := range regexp.MustCompile(`<arg\s+(?:line|value)="([^"]*)"`).FindAllStringSubmatch(m[2], -1) {
			parts = append(parts, x.Replace(a[1]))
		}
		c.shell(strings.Join(parts, " "), cwd, depth+1, lbl, true)
	}
	for _, m := range regexp.MustCompile(`(?s)<(?:script|scripts|source)>(.*?)</(?:script|scripts|source)>`).FindAllStringSubmatch(src, -1) {
		c.code(x.Replace(m[1]), lbl, true)
	}
}

func indexOf(xs []string, s string) int {
	for i, x := range xs {
		if x == s {
			return i
		}
	}
	return -1
}

// ---------- cargo make ----------

func (c *btCtx) cargoMake(args []string, cwd string, depth int) {
	var names []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if (a == "--makefile" || a == "-m" || a == "--cwd") && i+1 < len(args) {
			i++
			continue
		}
		if !strings.HasPrefix(a, "-") && a != "make" {
			names = append(names, a)
		}
	}
	if len(names) == 0 {
		names = []string{"default"}
	}
	p, src := c.first(cwd, "Makefile.toml")
	if src == "" {
		return
	}
	key := "cm\x00" + p + "\x00" + strings.Join(names, ",")
	if c.seen[key] {
		return
	}
	c.seen[key] = true
	tasks := parseCargoMake(src)
	done := map[string]bool{}
	var visit func(n string, d int)
	visit = func(n string, d int) {
		t := tasks[n]
		if t == nil || done[n] || d > 6 {
			return
		}
		done[n] = true
		for _, dep := range t["dependencies"] {
			visit(dep, d+1)
		}
		for _, dep := range t["run_task"] {
			visit(dep, d+1)
		}
		lbl := "tarefa `" + n + "` do Makefile.toml"
		if cmd := t["command"]; len(cmd) > 0 {
			parts := append(append([]string{}, cmd...), t["args"]...)
			s := strings.Join(parts, " ")
			if i := indexOf(parts, "-c"); i >= 0 && i+1 < len(parts) {
				s = strings.Join(parts[i+1:], " ")
			}
			c.shell(s, cwd, depth+1, lbl, true)
		}
		if sc := t["script"]; len(sc) > 0 {
			body := strings.Join(sc, "\n")
			if r := strings.Join(t["script_runner"], " "); regexp.MustCompile(`python|node|ruby|perl`).MatchString(r) {
				c.code(body, lbl, true)
			} else {
				c.shell(body, cwd, depth+1, lbl, true)
			}
		}
	}
	for _, n := range names {
		visit(n, 0)
	}
}

// parseCargoMake reads [tasks.NAME] tables into key → string values.
func parseCargoMake(src string) map[string]map[string][]string {
	out := map[string]map[string][]string{}
	var cur map[string][]string
	lines := strings.Split(strings.ReplaceAll(src, "\r", ""), "\n")
	for i := 0; i < len(lines); i++ {
		s := strings.TrimSpace(lines[i])
		if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
			cur = nil
			h := strings.Trim(s, "[]")
			if strings.HasPrefix(h, "tasks.") {
				n := strings.TrimPrefix(h, "tasks.")
				if j := strings.Index(n, "."); j >= 0 {
					n = n[:j]
				}
				n = strings.Trim(n, `"'`)
				if out[n] == nil {
					out[n] = map[string][]string{}
				}
				cur = out[n]
			}
			continue
		}
		if cur == nil || s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		k, v, ok := strings.Cut(s, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch {
		case strings.HasPrefix(v, `'''`) || strings.HasPrefix(v, `"""`):
			q := v[:3]
			body := strings.TrimPrefix(v, q)
			for !strings.Contains(body, q) && i+1 < len(lines) {
				i++
				body += "\n" + lines[i]
			}
			cur[k] = append(cur[k], strings.SplitN(body, q, 2)[0])
		case strings.HasPrefix(v, "["):
			for strings.Count(v, "[") > strings.Count(v, "]") && i+1 < len(lines) {
				i++
				v += "\n" + lines[i]
			}
			for _, m := range reBtTomlStr.FindAllStringSubmatch(v, -1) {
				cur[k] = append(cur[k], m[1]+m[2])
			}
		default:
			if m := reBtTomlStr.FindStringSubmatch(v); m != nil {
				cur[k] = append(cur[k], m[1]+m[2])
			}
		}
	}
	return out
}

// ---------- tox / nox ----------

func (c *btCtx) tox(args []string, cwd string, depth int) {
	var envs []string
	for i := 0; i < len(args); i++ {
		if (args[i] == "-e" || args[i] == "--env") && i+1 < len(args) {
			envs = append(envs, strings.Split(args[i+1], ",")...)
			i++
		}
	}
	p, src := c.first(cwd, "tox.ini")
	if src == "" {
		return
	}
	key := "tox\x00" + p + "\x00" + strings.Join(envs, ",")
	if c.seen[key] {
		return
	}
	c.seen[key] = true
	sec := ""
	key2 := ""
	vals := map[string]map[string][]string{}
	for _, l := range strings.Split(strings.ReplaceAll(src, "\r", ""), "\n") {
		s := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]"):
			sec, key2 = strings.Trim(s, "[]"), ""
		case s == "" || strings.HasPrefix(s, "#") || strings.HasPrefix(s, ";"):
		case (strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t")) && key2 != "":
			vals[sec][key2] = append(vals[sec][key2], s)
		default:
			k, v, ok := strings.Cut(s, "=")
			if !ok {
				continue
			}
			key2 = strings.TrimSpace(k)
			if vals[sec] == nil {
				vals[sec] = map[string][]string{}
			}
			if v = strings.TrimSpace(v); v != "" {
				vals[sec][key2] = append(vals[sec][key2], v)
			}
		}
	}
	for s, kv := range vals {
		if s != "testenv" && !strings.HasPrefix(s, "testenv:") {
			continue
		}
		if s != "testenv" && len(envs) > 0 {
			hit := false
			for _, e := range envs {
				if s == "testenv:"+e {
					hit = true
				}
			}
			if !hit {
				continue
			}
		}
		for _, k := range []string{"commands", "commands_pre", "commands_post", "install_command"} {
			var body []string
			for _, l := range kv[k] {
				body = append(body, regexp.MustCompile(`\{[^}]*\}`).ReplaceAllString(l, "x"))
			}
			c.shell(strings.Join(body, "\n"), cwd, depth+1, "["+s+"] "+k+" do tox.ini", true)
		}
	}
}

func (c *btCtx) nox(cwd string, depth int) {
	p, src := c.first(cwd, "noxfile.py")
	if src == "" || c.seen["nox\x00"+p] {
		return
	}
	c.seen["nox\x00"+p] = true
	for _, l := range strings.Split(src, "\n") {
		if regexp.MustCompile(`session\.(run|run_always|install|run_install)\(`).MatchString(l) {
			if s := litCmds(l); s != "" {
				c.shell(s, cwd, depth+1, "noxfile.py", true)
			}
		}
	}
	c.code(src, "noxfile.py", true)
}
