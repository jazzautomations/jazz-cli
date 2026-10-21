package main

// The script tool (docs/research/skills-personas-scripts.md §D): one tool
// {name, args} that validates args against an embedded JSON schema and runs a
// small Python 3 (stdlib, `python3 -I`) or POSIX sh script with a 60 s timeout,
// the scrubbed environment and stdout clipped to 8 KB. Scripts print one JSON
// object {ok, confidence, data, unparsed[], warnings[]}.
//
// Sources, in override order: built-in (go:embed jazz-scripts, extracted to
// dataDir()/scripts/<version>/ with sha256 stamps), user
// (~/.config/jazz-cli/scripts) and project (.jazz/scripts, only when trusted:
// trust kind "scripts"; a project script never replaces a built-in or user
// script of the same name). Off with `"scripts": "off"` or --scripts off.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

//go:embed jazz-scripts/*.json jazz-scripts/*.py jazz-scripts/*.sh jazz-scripts/lib/*
var scEmbed embed.FS

var (
	scTimeout     = 60 * time.Second // per script run
	scMaxOut      = 8 << 10          // bytes of JSON that reach the model
	scMaxRead     = 2 << 20          // stdout read from the script at most
	scNameRe      = regexp.MustCompile(`^[a-z][a-z0-9_]{0,40}$`)
	scLookPath    = exec.LookPath
	scEffects     = map[string]bool{"read": true, "write": true, "exec": true, "git": true, "net": true}
	scWrapperKeys = map[string]bool{"name": true, "script": true, "tool": true, "args": true, "arguments": true, "params": true, "input": true}
)

// scDef is one script: its JSON sidecar plus the assembled source.
type scDef struct {
	Name        string            `json:"name"`
	Version     int               `json:"version"`
	Run         string            `json:"run"`    // python3 | sh
	Effect      string            `json:"effect"` // read | write | exec | git | net
	EffectByOp  map[string]string `json:"effect_by_op,omitempty"`
	Description string            `json:"description"`
	Lib         []string          `json:"lib,omitempty"`
	Args        map[string]any    `json:"args"`

	source string // builtin | user | project
	origin string // sidecar path (user/project) or embed path
	body   []byte // prelude + libs + script body
}

// scRegistry holds the scripts of one Toolbox, loaded once so the tool spec
// stays byte-stable for the session.
type scRegistry struct {
	defs   map[string]*scDef
	names  []string
	python string // python3 path; "" = Python scripts unavailable
	sh     string
	warns  []string
	root   string
}

func (r *scRegistry) get(name string) *scDef {
	if r == nil {
		return nil
	}
	return r.defs[name]
}

// scRegistry returns the Toolbox's script registry, loading it on first use.
func (t *Toolbox) scRegistry() *scRegistry {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.scRegistryLocked()
}

func (t *Toolbox) scRegistryLocked() *scRegistry {
	if t.sc == nil {
		t.sc = scLoad(t.Root)
	}
	return t.sc
}

// scSpecLocked is the script tool spec (nil when no script can run); t.mu held.
func (t *Toolbox) scSpecLocked() []ToolSpec {
	r := t.scRegistryLocked()
	if len(r.names) == 0 {
		return nil
	}
	return []ToolSpec{r.spec()}
}

// scWindowsAppsStub: on Windows without Python, python3.exe / python.exe in
// …\Microsoft\WindowsApps are App Execution Alias stubs that open the Store.
func scWindowsAppsStub(p string) bool {
	return runtime.GOOS == "windows" && strings.Contains(strings.ToLower(p), `\microsoft\windowsapps\`)
}

// scLoad reads the built-in, user and (trusted) project scripts.
func scLoad(root string) *scRegistry {
	r := &scRegistry{defs: map[string]*scDef{}, root: root}
	if p, err := scLookPath("python3"); err == nil && !scWindowsAppsStub(p) {
		r.python = p
	} else if p, err := scLookPath("python"); err == nil && runtime.GOOS == "windows" && !scWindowsAppsStub(p) {
		r.python = p // the python.org installer for Windows ships python.exe only
	}
	if p, err := scLookPath("sh"); err == nil {
		r.sh = p
	} else if d := detectShell(); runtime.GOOS == "windows" && d.Kind == shBash {
		r.sh = d.Path // Git for Windows puts only bash.exe within reach; it runs sh scripts
	}
	ents, _ := scEmbed.ReadDir("jazz-scripts")
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p := "jazz-scripts/" + e.Name()
		side, _ := scEmbed.ReadFile(p)
		d, err := scParseDef(side, func(name string) ([]byte, error) { return scEmbed.ReadFile("jazz-scripts/" + name) })
		if err != nil {
			r.warns = append(r.warns, p+": "+err.Error())
			continue
		}
		d.source, d.origin = "builtin", p
		r.add(d, false)
	}
	r.loadDir(filepath.Join(configDir(), "scripts"), "user", false)
	if root != "" && IsTrusted(root, "scripts", pfScriptsBlob(root)) {
		r.loadDir(filepath.Join(root, ".jazz", "scripts"), "project", true)
	}
	for n := range r.defs {
		r.names = append(r.names, n)
	}
	sort.Strings(r.names)
	if len(r.warns) > 0 {
		dbg("scripts", map[string]any{"warnings": r.warns})
	}
	return r
}

// add registers d unless its interpreter is missing; keep=true never replaces
// an existing script (project scripts cannot shadow built-in or user ones).
func (r *scRegistry) add(d *scDef, keep bool) {
	if (d.Run == "python3" && r.python == "") || (d.Run == "sh" && r.sh == "") {
		r.warns = append(r.warns, d.Name+": "+d.Run+" não encontrado")
		return
	}
	if _, dup := r.defs[d.Name]; dup && keep {
		r.warns = append(r.warns, d.Name+": já existe um script com esse nome; o do projeto foi ignorado")
		return
	}
	r.defs[d.Name] = d
}

func (r *scRegistry) loadDir(dir, source string, keep bool) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || e.Type()&os.ModeSymlink != 0 {
			continue
		}
		p := filepath.Join(dir, e.Name())
		side, err := scReadSmall(p)
		if err != nil {
			continue
		}
		d, err := scParseDef(side, func(name string) ([]byte, error) {
			if strings.HasPrefix(name, "lib/") { // shared preludes/libs come from the binary
				return scEmbed.ReadFile("jazz-scripts/" + name)
			}
			return scReadSmall(filepath.Join(dir, name))
		})
		if err != nil {
			r.warns = append(r.warns, p+": "+err.Error())
			continue
		}
		d.source, d.origin = source, p
		r.add(d, keep)
	}
}

// scReadSmall reads a regular file of at most 256 KB (no symlinks).
func scReadSmall(p string) ([]byte, error) {
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > 256<<10 {
		return nil, fmt.Errorf("%s: não é um arquivo comum de até 256 KB", p)
	}
	return os.ReadFile(p)
}

// scParseDef parses a sidecar and assembles the script: prelude, libs, body.
func scParseDef(side []byte, read func(name string) ([]byte, error)) (*scDef, error) {
	d := &scDef{}
	if err := json.Unmarshal(side, d); err != nil {
		return nil, fmt.Errorf("JSON inválido: %w", err)
	}
	if !scNameRe.MatchString(d.Name) {
		return nil, fmt.Errorf("nome inválido %q", d.Name)
	}
	if d.Effect == "" {
		d.Effect = "exec"
	}
	if !scEffects[d.Effect] {
		return nil, fmt.Errorf("effect inválido %q", d.Effect)
	}
	for _, e := range d.EffectByOp {
		if !scEffects[e] {
			return nil, fmt.Errorf("effect_by_op inválido %q", e)
		}
	}
	if d.Args == nil {
		d.Args = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	ext := map[string]string{"python3": ".py", "sh": ".sh"}[d.Run]
	if ext == "" {
		return nil, fmt.Errorf("run deve ser python3 ou sh, não %q", d.Run)
	}
	body, err := read(d.Name + ext)
	if err != nil {
		return nil, fmt.Errorf("sem %s%s", d.Name, ext)
	}
	core, err := scEmbed.ReadFile("jazz-scripts/lib/core" + ext)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if d.Run == "sh" {
		b.WriteString("#!/bin/sh\n")
	}
	b.Write(core)
	for _, l := range d.Lib {
		if !scNameRe.MatchString(l) {
			return nil, fmt.Errorf("lib inválida %q", l)
		}
		lb, err := read("lib/" + l + ext)
		if err != nil {
			return nil, fmt.Errorf("sem lib/%s%s", l, ext)
		}
		b.WriteString("\n")
		b.Write(lb)
	}
	b.WriteString("\n# ---- " + d.Name + " ----\n")
	b.Write(body)
	d.body = b.Bytes()
	return d, nil
}

// spec is the tool spec: a 3-line description, the names as an enum and a
// compact signature list (one line per script would cost ~2x the tokens).
func (r *scRegistry) spec() ToolSpec {
	var sigs []string
	for _, n := range r.names {
		sigs = append(sigs, n+"("+scSignature(r.defs[n].Args)+")")
	}
	return ToolSpec{
		Name: "script",
		Description: "Run a bundled helper script; it returns compact JSON {ok,confidence,data,unparsed,warnings} in one call instead of several bash calls and long output.\n" +
			"Use it to detect the project, run/parse tests and builds, extract stack traces, summarize logs, find symbols, read JSON/YAML/CSV, check git state, ports and local HTTP.\n" +
			"Pass the script's arguments in args; a wrong argument returns the script's schema.",
		Params: obj(map[string]any{
			"name": map[string]any{"type": "string", "enum": r.names, "description": "Script and its args (? = optional, a|b = one of): " + strings.Join(sigs, " ")},
			"args": map[string]any{"type": "object", "description": "Arguments for the script"},
		}, "name"),
	}
}

// scSignature renders "symbol,kind?,path?" or "file|cmd|text,framework?":
// required args, then the anyOf alternatives, then the optional ones.
func scSignature(schema map[string]any) string {
	props, _ := schema["properties"].(map[string]any)
	req := map[string]bool{}
	for _, r := range scStrings(schema["required"]) {
		req[r] = true
	}
	alt := map[string]bool{}
	var alts []string
	if alts0, ok := schema["anyOf"].([]any); ok {
		for _, a := range alts0 {
			m, _ := a.(map[string]any)
			for _, r := range scStrings(m["required"]) {
				if !alt[r] && !req[r] {
					alt[r] = true
					alts = append(alts, r)
				}
			}
		}
	}
	var must, opt []string
	for _, k := range scKeys(props) {
		switch {
		case req[k]:
			must = append(must, k)
		case !alt[k]:
			opt = append(opt, k+"?")
		}
	}
	out := must
	if len(alts) > 0 {
		out = append(out, strings.Join(alts, "|"))
	}
	return strings.Join(append(out, opt...), ",")
}

func scStrings(v any) []string {
	var out []string
	if a, ok := v.([]any); ok {
		for _, x := range a {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	if a, ok := v.([]string); ok {
		out = append(out, a...)
	}
	return out
}

// ---------- arguments ----------

// scSplit pulls the script name and its args out of a tool call. Weak models
// send args as a JSON string, or flatten them next to "name": both work.
func scSplit(m map[string]any) (name string, args map[string]any, errMsg string) {
	for _, k := range []string{"name", "script", "tool"} {
		if s, ok := argStr(m, k); ok && strings.TrimSpace(s) != "" {
			name = strings.ToLower(strings.TrimSpace(s))
			break
		}
	}
	name = strings.TrimSuffix(strings.TrimSuffix(name, ".py"), ".sh")
	for _, k := range []string{"args", "arguments", "params", "input"} {
		switch x := m[k].(type) {
		case map[string]any:
			args = x
		case string:
			if strings.TrimSpace(x) == "" {
				continue
			}
			a, _, err := parseArgs(x)
			if err != nil {
				return name, nil, "args is not a JSON object: " + err.Error()
			}
			args = a
		}
		if args != nil {
			break
		}
	}
	if args == nil {
		args = map[string]any{}
		for k, v := range m {
			if !scWrapperKeys[k] {
				args[k] = v
			}
		}
	}
	return name, args, ""
}

// scValidate checks args against the schema subset the sidecars use (type,
// enum, required, anyOf-of-required, additionalProperties:false, minimum,
// maximum) and coerces what weak models get wrong: "5" for 5, "true" for true,
// a number for a string, a list of strings for a string, enum case.
func scValidate(schema, args map[string]any) (map[string]any, []string) {
	props, _ := schema["properties"].(map[string]any)
	out := map[string]any{}
	var errs []string
	strict := schema["additionalProperties"] == false
	for k, v := range args {
		ps, ok := props[k].(map[string]any)
		if !ok {
			if alt := scFoldKey(props, k); alt != "" {
				k, ps = alt, props[alt].(map[string]any)
			} else if strict {
				errs = append(errs, fmt.Sprintf("unknown argument %q (valid: %s)", k, strings.Join(scKeys(props), ", ")))
				continue
			} else {
				out[k] = v
				continue
			}
		}
		if v == nil {
			continue
		}
		cv, err := scCoerce(k, v, ps)
		if err != "" {
			errs = append(errs, err)
			continue
		}
		out[k] = cv
	}
	for _, r := range scStrings(schema["required"]) {
		if _, ok := out[r]; !ok {
			errs = append(errs, fmt.Sprintf("missing required argument %q", r))
		}
	}
	if anyOf, ok := schema["anyOf"].([]any); ok && len(anyOf) > 0 {
		met := false
		var need []string
		for _, a := range anyOf {
			m, _ := a.(map[string]any)
			req := scStrings(m["required"])
			need = append(need, strings.Join(req, "+"))
			all := true
			for _, r := range req {
				if _, ok := out[r]; !ok {
					all = false
				}
			}
			met = met || all
		}
		if !met {
			errs = append(errs, "pass one of: "+strings.Join(need, ", "))
		}
	}
	return out, errs
}

// scFoldKey finds a property that matches k ignoring case, dashes and a few
// common synonyms (only when unambiguous).
func scFoldKey(props map[string]any, k string) string {
	norm := func(s string) string {
		return strings.ReplaceAll(strings.ReplaceAll(strings.ToLower(s), "-", "_"), " ", "_")
	}
	syn := map[string]string{"command": "cmd", "filename": "file", "filepath": "file", "file_path": "file", "dir": "path", "directory": "path", "query": "symbol", "name": "symbol", "pattern": "symbol", "columns": "cols", "content": "text", "output": "text"}
	nk := norm(k)
	for p := range props {
		if norm(p) == nk {
			return p
		}
	}
	if s, ok := syn[nk]; ok {
		if _, ok := props[s]; ok {
			return s
		}
	}
	return ""
}

func scKeys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func scCoerce(k string, v any, ps map[string]any) (any, string) {
	typ, _ := ps["type"].(string)
	if enum, ok := ps["enum"].([]any); ok {
		s := fmt.Sprint(v)
		for _, e := range enum {
			if es := fmt.Sprint(e); strings.EqualFold(es, s) {
				return e, ""
			}
		}
		var opts []string
		for _, e := range enum {
			opts = append(opts, fmt.Sprint(e))
		}
		return nil, fmt.Sprintf("argument %q must be one of %s (got %v)", k, strings.Join(opts, ", "), v)
	}
	switch typ {
	case "string":
		switch x := v.(type) {
		case string:
			return x, ""
		case float64, bool:
			return fmt.Sprint(x), ""
		case []any:
			var parts []string
			for _, e := range x {
				s, ok := e.(string)
				if !ok {
					return nil, fmt.Sprintf("argument %q must be a string", k)
				}
				parts = append(parts, s)
			}
			return strings.Join(parts, " "), ""
		}
		return nil, fmt.Sprintf("argument %q must be a string", k)
	case "integer", "number":
		var f float64
		switch x := v.(type) {
		case float64:
			f = x
		case string:
			p, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
			if err != nil {
				return nil, fmt.Sprintf("argument %q must be a number (got %q)", k, x)
			}
			f = p
		default:
			return nil, fmt.Sprintf("argument %q must be a number", k)
		}
		if typ == "integer" && f != math.Trunc(f) {
			return nil, fmt.Sprintf("argument %q must be an integer", k)
		}
		if mn, ok := ps["minimum"].(float64); ok && f < mn {
			return nil, fmt.Sprintf("argument %q must be >= %v", k, mn)
		}
		if mx, ok := ps["maximum"].(float64); ok && f > mx {
			return nil, fmt.Sprintf("argument %q must be <= %v", k, mx)
		}
		return f, ""
	case "boolean":
		switch x := v.(type) {
		case bool:
			return x, ""
		case string:
			switch strings.ToLower(strings.TrimSpace(x)) {
			case "true", "1", "yes", "sim", "on":
				return true, ""
			case "false", "0", "no", "não", "nao", "off", "":
				return false, ""
			}
		case float64:
			return x != 0, ""
		}
		return nil, fmt.Sprintf("argument %q must be true or false", k)
	}
	return v, "" // untyped (any JSON)
}

// ---------- extraction and execution ----------

// scExtract writes the assembled script under dataDir()/scripts/<version>/
// (0700/0600) unless an identical copy is already there, and returns its path.
// The sha256 is checked on every run, so a stale or edited copy is replaced.
func scExtract(d *scDef, root string) (string, error) {
	dir := filepath.Join(dataDir(), "scripts", version)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	ext := map[string]string{"python3": ".py", "sh": ".sh"}[d.Run]
	name := fmt.Sprintf("%s.v%d%s", d.Name, d.Version, ext)
	switch d.source {
	case "user":
		name = "user-" + name
	case "project":
		h := sha256.Sum256([]byte(root))
		name = "proj-" + hex.EncodeToString(h[:4]) + "-" + name
	}
	p := filepath.Join(dir, name)
	sum := sha256.Sum256(d.body)
	want := hex.EncodeToString(sum[:])
	if cur, err := os.ReadFile(p); err == nil {
		got := sha256.Sum256(cur)
		if hex.EncodeToString(got[:]) == want {
			return p, nil
		}
	}
	if err := writeFileAtomic(p, d.body, 0o600); err != nil {
		return "", err
	}
	_ = writeFileAtomic(p+".sha256", []byte(want+"  "+name+"\n"), 0o600)
	return p, nil
}

// scCapBuf keeps the first max bytes written to it.
type scCapBuf struct {
	b    bytes.Buffer
	max  int
	over bool
}

func (w *scCapBuf) Write(p []byte) (int, error) {
	room := w.max - w.b.Len()
	switch {
	case room <= 0:
		w.over = w.over || len(p) > 0
	case len(p) > room:
		w.b.Write(p[:room])
		w.over = true
	default:
		w.b.Write(p)
	}
	return len(p), nil
}

// scResult is one script run.
type scResult struct {
	Stdout   []byte
	Stderr   string
	Exit     int
	TimedOut bool
	Err      error
}

// scExec runs a script: `python3 -I file` or `sh file`, cwd = root, args as
// JSON on stdin (sh scripts also get ARG_<NAME> variables), scrubbed env, its
// own process group (killed on timeout/cancel and after it exits).
func (r *scRegistry) scExec(ctx context.Context, d *scDef, args map[string]any, root string) scResult {
	file, err := scExtract(d, root)
	if err != nil {
		return scResult{Err: fmt.Errorf("não consegui extrair o script: %w", err)}
	}
	ctx, cancel := context.WithTimeout(ctx, scTimeout)
	defer cancel()
	var argv []string
	if d.Run == "python3" {
		argv = []string{r.python, "-I", file}
	} else {
		argv = []string{r.sh, file}
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = root
	env := append(scrubEnv(os.Environ()), "JAZZ_ROOT="+root, "PYTHONDONTWRITEBYTECODE=1", "PYTHONIOENCODING=utf-8")
	if d.Run == "sh" {
		for k, v := range args {
			if !scNameRe.MatchString(k) {
				continue
			}
			s, ok := v.(string)
			if !ok {
				b, _ := json.Marshal(v)
				s = string(b)
			}
			env = append(env, "ARG_"+strings.ToUpper(k)+"="+s)
		}
	}
	cmd.Env = env
	in, _ := json.Marshal(args)
	cmd.Stdin = bytes.NewReader(in)
	out := &scCapBuf{max: scMaxRead}
	errb := &scCapBuf{max: 16 << 10}
	cmd.Stdout, cmd.Stderr = out, errb
	procPrepare(cmd, false)
	sbWrapCmd(cmd)
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			procKillTree(cmd.Process.Pid)
		}
		return nil
	}
	cmd.WaitDelay = 2 * time.Second
	if err = cmd.Start(); err == nil {
		procAttach(cmd)
		err = cmd.Wait()
	}
	if cmd.Process != nil {
		procKillTree(cmd.Process.Pid) // stray children (a server the cmd started)
		procForget(cmd.Process.Pid)
	}
	res := scResult{Stdout: out.b.Bytes(), Stderr: errb.b.String(), Exit: cmd.ProcessState.ExitCode()}
	if ctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
	}
	if err != nil && res.Exit < 0 && !res.TimedOut {
		res.Err = err
	}
	return res
}

// ---------- output shaping ----------

// scClipJSON marshals v within max bytes by halving its largest array or
// string (never cutting JSON in the middle); clipped = something was cut.
func scClipJSON(v any, max int) (string, bool) {
	b, _ := json.Marshal(v)
	clipped := false
	for i := 0; len(b) > max && i < 80; i++ {
		if !scShrink(&v) {
			break
		}
		clipped = true
		b, _ = json.Marshal(v)
	}
	if clipped {
		if m, ok := v.(map[string]any); ok {
			w, _ := m["warnings"].([]any)
			m["warnings"] = append(w, fmt.Sprintf("output clipped to %d KB", max>>10))
			b, _ = json.Marshal(m)
		}
	}
	if len(b) > max {
		return scCut(string(b), max), true
	}
	return string(b), clipped
}

// scShrink halves the biggest array (by approximate JSON size) or string in v.
func scShrink(v *any) bool {
	var best func()
	bestSize := 0
	var walk func(x any, set func(any)) int
	walk = func(x any, set func(any)) int {
		switch t := x.(type) {
		case map[string]any:
			n := 2
			for k, c := range t {
				k := k
				n += len(k) + 4 + walk(c, func(nv any) { t[k] = nv })
			}
			return n
		case []any:
			n := 2
			for i, c := range t {
				i := i
				n += 1 + walk(c, func(nv any) { t[i] = nv })
			}
			if len(t) > 1 && n > bestSize {
				bestSize = n
				best = func() { set(append([]any(nil), t[:len(t)/2]...)) }
			}
			return n
		case string:
			if len(t) > 80 && len(t) > bestSize {
				bestSize = len(t)
				best = func() { set(scCut(t, len(t)/2)) }
			}
			return len(t) + 2
		}
		return 8
	}
	walk(*v, func(nv any) { *v = nv })
	if best == nil {
		return false
	}
	best()
	return true
}

// scCut cuts s to at most n bytes on a rune boundary, with an ellipsis.
func scCut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n > 3 {
		n -= 3
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// ---------- the tool ----------

// script executes the script tool call.
func (t *Toolbox) script(ctx context.Context, m map[string]any, raw string) ToolOutput {
	r := t.scRegistry()
	name, args, perr := scSplit(m)
	if perr != "" {
		return validationErr("script", perr, raw)
	}
	if name == "" {
		return validationErr("script", "missing required parameter 'name' (one of: "+strings.Join(r.names, ", ")+")", raw)
	}
	d := r.get(name)
	if d == nil {
		return ToolOutput{IsErr: true, Text: fmt.Sprintf("Unknown script %q. Available: %s.", name, strings.Join(r.names, ", ")),
			Display: Display{Summary: "script desconhecido: " + name}}
	}
	args, errs := scValidate(d.Args, args)
	if len(errs) > 0 {
		sch, _ := json.Marshal(d.Args)
		return ToolOutput{IsErr: true, Text: fmt.Sprintf("Validation failed for script %q: %s.\n%s: %s\nargs schema: %s", name, strings.Join(errs, "; "), name, d.Description, sch),
			Display: Display{Summary: "argumentos inválidos: " + strings.Join(errs, "; ")}}
	}
	if name == "run_tests" {
		if c, _ := argStr(args, "cmd"); strings.TrimSpace(c) == "" {
			c := fpTestCommand(fpDetect(t.Root), scArg(args, "target"))
			if c == "" {
				return ToolOutput{IsErr: true, Text: `{"ok":false,"confidence":0,"data":{},"unparsed":[],"warnings":[],"error":"no test command detected (no go.mod, package.json test script, Cargo.toml, Python project or Makefile test target); pass cmd"}`,
					Display: Display{Summary: "nenhum comando de teste detectado"}}
			}
			args["cmd"] = c
		}
	}
	pol := t.scPolicyFor(d, args)
	if pol.path != "" && pol.effect == "write" {
		t.snap(t.abs(pol.path)) // /undo covers json_edit
	}
	t0 := time.Now()
	res := r.scExec(ctx, d, args, t.Root)
	if pol.effect != "read" && pol.effect != "net" || pol.cmd != "" && !IsReadOnly(pol.cmd) {
		t.cpAfterBash("script " + name) // shadow-git: /undo sees what the script changed
	}
	dur := time.Since(t0).Round(100 * time.Millisecond)
	switch {
	case ctx.Err() == context.Canceled:
		return ToolOutput{IsErr: true, Text: "Script cancelled by the user.", Display: Display{Summary: "cancelado"}}
	case res.TimedOut:
		return ToolOutput{IsErr: true, Text: fmt.Sprintf(`{"ok":false,"confidence":0,"data":{},"unparsed":[],"warnings":[],"error":"timed out after %s"}`, scTimeout),
			Display: Display{Summary: "tempo esgotado (" + scTimeout.String() + ")"}}
	case res.Err != nil:
		return ToolOutput{IsErr: true, Text: "Script failed to start: " + res.Err.Error(), Display: Display{Summary: res.Err.Error()}}
	}
	var v any
	if err := json.Unmarshal(bytes.TrimSpace(res.Stdout), &v); err != nil {
		tail := strings.TrimSpace(res.Stderr)
		if tail == "" {
			tail = strings.TrimSpace(string(res.Stdout))
		}
		msg := fmt.Sprintf("Script %q did not print a JSON result (exit %d). Output:\n%s", name, res.Exit, truncateHeadTail(redactSecrets(tail), 2000))
		return ToolOutput{IsErr: true, Text: msg, Display: Display{Summary: fmt.Sprintf("sem JSON (exit %d)", res.Exit), Preview: lastLines(tail, 3)}}
	}
	text, _ := scClipJSON(v, scMaxOut)
	ok, conf, errMsg := scStatus(v)
	sum := fmt.Sprintf("ok · confiança %.2f · %s", conf, dur)
	if !ok {
		sum = "falhou: " + oneLine(errMsg, 120)
	}
	return ToolOutput{Text: text, IsErr: !ok, Display: Display{Summary: sum, Preview: scPreview(v)}}
}

// scDetail renders a script call as `name k=v …` (max > 0 clips to one line).
func scDetail(m map[string]any, max int) string {
	name, args, _ := scSplit(m)
	parts := []string{name}
	for _, k := range scKeys(args) {
		v := args[k]
		s, ok := v.(string)
		if !ok {
			b, _ := json.Marshal(v)
			s = string(b)
		}
		parts = append(parts, k+"="+s)
	}
	out := strings.Join(parts, " ")
	if max > 0 {
		return oneLine(out, max)
	}
	return out
}

// scArg is a string argument ("" when missing).
func scArg(args map[string]any, k string) string {
	s, _ := argStr(args, k)
	return strings.TrimSpace(s)
}

func scStatus(v any) (ok bool, conf float64, errMsg string) {
	m, _ := v.(map[string]any)
	ok, _ = m["ok"].(bool)
	conf, _ = m["confidence"].(float64)
	errMsg, _ = m["error"].(string)
	return
}

// scPreview shows up to 3 short "key: value" lines from data.
func scPreview(v any) []string {
	m, _ := v.(map[string]any)
	data, _ := m["data"].(map[string]any)
	var keys []string
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		switch x := data[k].(type) {
		case string, float64, bool:
			out = append(out, oneLine(fmt.Sprintf("%s: %v", k, x), 100))
		case []any:
			out = append(out, fmt.Sprintf("%s: %d", k, len(x)))
		}
		if len(out) == 3 {
			break
		}
	}
	return out
}

func lastLines(s string, n int) []string {
	l := nonEmptyLines(s)
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return l
}

// ---------- permissions ----------

// scPolicy is what a script call may do, for permit().
type scPolicy struct {
	known  bool
	name   string
	effect string // read | write | exec | git | net
	cmd    string // a shell command the script will run ("" = none)
	path   string // the file a write script changes
	remote bool   // http_check with allow_remote
}

func (p scPolicy) readOnly() bool {
	if !p.known {
		return true // fails as "unknown script" without side effects
	}
	if p.cmd != "" && !IsReadOnly(p.cmd) {
		return false
	}
	return p.effect == "read" || p.effect == "net" && !p.remote
}

func (t *Toolbox) scPolicyFor(d *scDef, args map[string]any) scPolicy {
	p := scPolicy{known: true, name: d.Name, effect: d.Effect}
	op, _ := argStr(args, "op") // "" = the script's default op
	if e, ok := d.EffectByOp[strings.TrimSpace(op)]; ok {
		p.effect = e
	}
	p.cmd, _ = argStr(args, "cmd")
	p.cmd = strings.TrimSpace(p.cmd)
	if p.effect == "write" {
		p.path, _ = argStr(args, "file")
	}
	p.remote = d.Name == "http_check" && argBool(args, "allow_remote")
	return p
}

// scPolicyArgs is scPolicyFor for raw tool-call args (run_tests' default
// command included, so a prompt shows what will run).
func (t *Toolbox) scPolicyArgs(m map[string]any) scPolicy {
	name, args, _ := scSplit(m)
	d := t.scRegistry().get(name)
	if d == nil {
		return scPolicy{}
	}
	args, _ = scValidate(d.Args, args)
	if name == "run_tests" {
		if c, _ := argStr(args, "cmd"); strings.TrimSpace(c) == "" {
			args["cmd"] = fpTestCommand(fpDetect(t.Root), scArg(args, "target"))
		}
	}
	return t.scPolicyFor(d, args)
}

// scGate runs before yolo/plan in permit(): the subagent and plan mode only
// get read-only scripts, and Bash deny rules also cover a script's cmd.
func (a *Agent) scGate(args map[string]any, mode string) (deny string, byRule bool) {
	p := a.tools.scPolicyArgs(args)
	if p.cmd != "" {
		if d, rule := a.Perms.Check("bash", map[string]any{"command": p.cmd}); d == PermDeny {
			return fmt.Sprintf("Blocked by permission rule %s (the script's cmd). Do not retry it; choose another approach or ask the user.", rule), true
		}
	}
	if p.readOnly() {
		return "", false
	}
	if a.sub {
		return "The research subagent may only run read-only scripts. Report what should change instead.", false
	}
	if mode == "plan" {
		return "Plan mode: only read-only scripts are allowed. Present your plan instead.", false
	}
	return "", false
}

// scPrompt decides whether a script call needs the user's confirmation.
func (a *Agent) scPrompt(args map[string]any, mode string) (reason string, need bool) {
	p := a.tools.scPolicyArgs(args)
	if p.cmd != "" {
		if why := DangerReason(p.cmd); why != "" {
			return why, true
		}
	}
	if p.path != "" && protectedPath(a.tools.abs(p.path), a.tools.Root) {
		return "arquivo sensível (credenciais ou configuração)", true
	}
	need = mode == "ask" && !p.readOnly()
	if p.cmd != "" {
		switch d, _ := a.Perms.Check("bash", map[string]any{"command": p.cmd}); d {
		case PermAsk:
			need = true
		case PermAllow:
			need = false
		}
	}
	return "", need
}

// scSmartNeedsPrompt is smart mode for scripts: read-only runs, a cmd goes
// through the bash classifier, git changes always ask, writes only in the project.
func (a *Agent) scSmartNeedsPrompt(ctx context.Context, args map[string]any) bool {
	p := a.tools.scPolicyArgs(args)
	if p.readOnly() {
		return false
	}
	if p.cmd != "" {
		return a.smartNeedsPrompt(ctx, "bash", map[string]any{"command": p.cmd}, p.cmd, "", false, false)
	}
	switch p.effect {
	case "git", "exec":
		return true
	case "write":
		return p.path == "" || !smWorkspaceEdit(a.tools.abs(p.path), a.tools.Root)
	case "net":
		return p.remote
	}
	return true
}

// scRuleMatch matches a Script(name) permission rule. A script that runs a
// shell command is never covered by it: its Bash rules apply instead.
func scRuleMatch(spec string, args map[string]any) bool {
	name, sargs, _ := scSplit(args)
	if c, _ := argStr(sargs, "cmd"); strings.TrimSpace(c) != "" || scRunsCmd[name] {
		return false
	}
	ok, _ := path.Match(spec, name)
	return ok
}

// scRunsCmd are the built-ins that always run a shell command.
var scRunsCmd = map[string]bool{"run_tests": true, "snapshot_cmd": true}

// scDeriveRules is the rule an "always" answer saves: the Bash rules of the
// script's cmd (run_tests: the detected test command), or Script(name).
func scDeriveRules(p *PermSet, args map[string]any) []string {
	name, sargs, _ := scSplit(args)
	cmd, _ := argStr(sargs, "cmd")
	if strings.TrimSpace(cmd) == "" && name == "run_tests" {
		tg, _ := argStr(sargs, "target")
		cmd = fpTestCommand(fpDetect(p.root), tg)
	}
	if strings.TrimSpace(cmd) != "" {
		return p.pfDeriveRules("bash", map[string]any{"command": cmd})
	}
	if !scNameRe.MatchString(name) || scRunsCmd[name] {
		return nil
	}
	return []string{"Script(" + name + ")"}
}

// ---------- project trust ----------

// scProjectBlob is what trust kind "scripts" hashes: every project script
// file and .jazz/intents.json (both make jazz run project-provided code).
func scProjectBlob(root string) []byte {
	var b bytes.Buffer
	dir := filepath.Join(root, ".jazz", "scripts")
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !(strings.HasSuffix(n, ".json") || strings.HasSuffix(n, ".py") || strings.HasSuffix(n, ".sh")) {
			continue
		}
		data, err := scReadSmall(filepath.Join(dir, n))
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "== scripts/%s\n", n)
		b.Write(data)
	}
	if data, err := scReadSmall(filepath.Join(root, ".jazz", "intents.json")); err == nil {
		b.WriteString("== intents.json\n")
		b.Write(data)
	}
	return b.Bytes()
}

// scTrustSummary lists project scripts and intents for the trust prompt.
func scTrustSummary(root string) string {
	var b strings.Builder
	ents, _ := os.ReadDir(filepath.Join(root, ".jazz", "scripts"))
	for _, e := range ents {
		if n := e.Name(); !e.IsDir() && strings.HasSuffix(n, ".json") {
			fmt.Fprintf(&b, "  script do projeto (.jazz/scripts): %s\n", visibleDetail(strings.TrimSuffix(n, ".json")))
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".jazz", "intents.json")); err == nil {
		b.WriteString("  atalhos de comando do projeto (.jazz/intents.json)\n")
	}
	return b.String()
}

// scRunDirect runs a script for the user (/script, offline mode) and returns
// its JSON (indented) or an error text.
func (t *Toolbox) scRunDirect(ctx context.Context, name, rawArgs string) (string, bool) {
	m := map[string]any{"name": name}
	if s := strings.TrimSpace(rawArgs); s != "" {
		m["args"] = s
	}
	b, _ := json.Marshal(m)
	out := t.Exec(ctx, "script", string(b))
	if !out.IsErr || strings.HasPrefix(strings.TrimSpace(out.Text), "{") {
		var v any
		if json.Unmarshal([]byte(out.Text), &v) == nil {
			if pb, err := json.MarshalIndent(v, "", "  "); err == nil {
				return string(pb), !out.IsErr
			}
		}
	}
	return out.Text, !out.IsErr
}

// scList is the one-line-per-script listing for /script.
func (t *Toolbox) scList() []string {
	r := t.scRegistry()
	var out []string
	for _, n := range r.names {
		d := r.defs[n]
		src := ""
		if d.source != "builtin" {
			src = " [" + map[string]string{"user": "usuário", "project": "projeto"}[d.source] + "]"
		}
		out = append(out, fmt.Sprintf("%-18s %s%s", n, d.Description, src))
	}
	return out
}
