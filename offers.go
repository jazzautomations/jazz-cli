package main

// offers.go: when there is no model to answer (offline) or the model is slow
// to start, jazz offers a short numbered menu of deterministic actions built
// from the request and the project, instead of staying silent or guessing.
//
//   - offline, a request with no fast path: the menu is printed above the
//     input; a digit (or the first letters of an item) runs that action, any
//     other text is a normal prompt (the menu never blocks typing).
//   - online, no first token after 5 s: the same menu, collapsed to one line
//     in the status ("[1 testes · 2 git · 3 procurar · esc cancela]"); a digit
//     runs the action while the request keeps going.
//   - offline, a failed !command: repeat with sudo / manual / search the code.
//
// Nothing here touches the system prompt or the message history beyond the
// usual <system-reminder> note of what ran (cache-safe).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// olAction is one deterministic action of a menu.
type olAction struct {
	Key    string // short label for the one-line menu ("testes", "git")
	Label  string // menu text
	Kind   string // shell | test | script | summary | online | wait | sudo | slash
	Cmd    string // shell command (shell, test, sudo)
	Script string // script name (script)
	Args   string // script args (JSON)
	Intent string // for the note and the debug log
}

// olMenuIn is everything the menu is built from (a pure function of it).
type olMenuIn struct {
	Text     string
	Offline  bool
	Waiting  bool // online but the model is slow
	Proj     fpProject
	GitDirty bool
	LastErr  string // last error text of the session (tool output)
	Root     string
	HasRg    bool
}

// olMenuMax caps the menu (one screenful, 3-5 items).
const olMenuMax = 5

var (
	olFileTok = lazyRx(`(?:^|[\s"'(])((?:[\w.-]+/)*[\w-]+\.[A-Za-z0-9]{1,6})(?::(\d+))?`)
	olErrLoc  = lazyRx(`((?:[\w.-]+/)*[\w-]+\.[A-Za-z0-9]{1,6}):(\d+)`)
	olQuoted  = lazyRx(`["'“”‘’]([^"'“”‘’]{2,60})["'“”‘’]`)
)

// olStop are words never offered as a search term.
var olStop = map[string]bool{}

func init() {
	for _, w := range strings.Fields("o a os as um uma de do da dos das em no na nos nas que e é pra para por com sem se " +
		"me mim meu minha isso esse essa este esta aqui ali ai como onde qual quais quando porque porquê pq não nao sim " +
		"faz faça fazer ver vê mostra mostre explica explique corrige corrija conserta conserte arruma arrume cria crie " +
		"adiciona adicione remove remova muda mude melhora melhore deixa deixe tem ter tá ta está esta estão erro erros " +
		"bug bugs problema código codigo arquivo arquivos projeto função funcao favor oi olá ola obrigado valeu " +
		"quebra quebrou quebrando falha falhou funciona funcionou rodar roda rodou dá deu dando pode consegue precisa " +
		"the a an of to in on is are and or for with this that it fix make add show explain what how why where") {
		olStop[w] = true
	}
}

// olSearchTerm picks the word of text most likely to be a code identifier
// (quoted text first; then snake_case/camelCase/dotted; then the longest
// non-stopword of 4+ letters). "" when nothing fits.
func olSearchTerm(text string) string {
	if m := olQuoted.FindStringSubmatch(text); m != nil {
		return strings.TrimSpace(m[1])
	}
	best, bestScore := "", 0
	for _, w := range strings.FieldsFunc(text, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.' || r == '$')
	}) {
		w = strings.Trim(w, ".")
		lw := strings.ToLower(w)
		if len([]rune(w)) < 3 || olStop[lw] || olFileTok.MatchString(" "+w) {
			continue
		}
		score := 1
		hasUpper := false
		for i, r := range w {
			if unicode.IsUpper(r) && i > 0 {
				hasUpper = true
			}
		}
		switch {
		case strings.ContainsAny(w, "_$") || hasUpper:
			score = 4
		case strings.ContainsAny(w, "0123456789."):
			score = 3
		case len(w) >= 4:
			score = 2
		}
		if score > bestScore || score == bestScore && len(w) > len(best) {
			best, bestScore = w, score
		}
	}
	if bestScore < 2 {
		return ""
	}
	return best
}

// olMentionedFiles are the file names in text that exist under root.
func olMentionedFiles(text, root string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range olFileTok.FindAllStringSubmatch(text, -1) {
		f := m[1]
		if seen[f] || strings.HasPrefix(f, "..") {
			continue
		}
		seen[f] = true
		if st, err := os.Stat(filepath.Join(root, f)); err == nil && st.Mode().IsRegular() {
			out = append(out, f)
		}
	}
	return out
}

// olErrLocation finds "file:line" of an existing file in an error text.
func olErrLocation(text, root string) (string, int) {
	for _, m := range olErrLoc.FindAllStringSubmatch(text, 20) {
		if st, err := os.Stat(filepath.Join(root, m[1])); err == nil && st.Mode().IsRegular() {
			n, _ := strconv.Atoi(m[2])
			return m[1], n
		}
	}
	return "", 0
}

func olSearchCmd(term string, hasRg bool) string {
	if hasRg {
		return "rg -n --no-heading -S -- " + fpShQuote(term) + " . | head -n 60"
	}
	return "grep -rnI --exclude-dir=.git --exclude-dir=node_modules --exclude-dir=vendor -- " + fpShQuote(term) + " . | head -n 60"
}

// olMenu composes the menu for in: specific items (files and terms from the
// request, the last error), then project items (tests, git, summary), and
// last the way out ("esperar o modelo" / "/online"). At most olMenuMax.
func olMenu(in olMenuIn) []olAction {
	var items []olAction
	add := func(a olAction) {
		for _, x := range items {
			if x.Cmd == a.Cmd && x.Kind == a.Kind && x.Script == a.Script {
				return
			}
		}
		items = append(items, a)
	}
	for _, f := range olMentionedFiles(in.Text, in.Root) {
		add(olAction{Key: "ler", Label: "abrir/ler " + f, Kind: "shell", Cmd: "head -n 80 -- " + fpShQuote(f), Intent: "read"})
		if len(items) >= 2 {
			break
		}
	}
	if f, n := olErrLocation(in.LastErr, in.Root); f != "" {
		lo := max(n-6, 1)
		add(olAction{Key: "erro", Label: fmt.Sprintf("localizar o erro em %s:%d", f, n), Kind: "shell",
			Cmd: fmt.Sprintf("awk 'NR>=%d && NR<=%d {printf \"%%5d  %%s\\n\", NR, $0}' %s", lo, n+6, fpShQuote(f)), Intent: "error_location"})
	}
	if term := olSearchTerm(in.Text); term != "" {
		add(olAction{Key: "procurar", Label: "procurar '" + term + "' no código", Kind: "shell", Cmd: olSearchCmd(term, in.HasRg), Intent: "search"})
	}
	if in.Proj.Test != "" {
		add(olAction{Key: "testes", Label: "rodar os testes e listar falhas", Kind: "test", Cmd: in.Proj.Test, Intent: "test"})
	}
	if in.Proj.Git && in.GitDirty {
		add(olAction{Key: "git", Label: "mostrar git status/diff resumido", Kind: "shell", Cmd: "git status --short --branch && git diff --stat", Intent: "git_status"})
	} else if in.Proj.Git {
		add(olAction{Key: "git", Label: "mostrar os últimos commits", Kind: "shell", Cmd: "git log --oneline -n 10", Intent: "log"})
	}
	add(olAction{Key: "resumo", Label: "resumo do projeto", Kind: "summary", Intent: "summary"})
	var last olAction
	switch {
	case in.Offline:
		last = olAction{Key: "online", Label: "/online — tentar o modelo de novo", Kind: "online", Intent: "online"}
	case in.Waiting:
		last = olAction{Key: "esperar", Label: "esperar o modelo", Kind: "wait", Intent: "wait"}
	}
	keep := olMenuMax
	if last.Kind != "" {
		keep--
	}
	if len(items) > keep {
		items = items[:keep]
	}
	if last.Kind != "" {
		items = append(items, last)
	}
	return items
}

// olShellFailMenu: a failed !command while offline.
func olShellFailMenu(cmd, out, root string, hasRg bool) []olAction {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return nil
	}
	prog := fields[0]
	var items []olAction
	if prog != "sudo" {
		items = append(items, olAction{Key: "sudo", Label: "repetir com sudo", Kind: "sudo", Cmd: "sudo " + cmd, Intent: "sudo"})
	}
	if regexp.MustCompile(`^[\w.+-]+$`).MatchString(prog) {
		items = append(items, olAction{Key: "manual", Label: "mostrar o manual de " + prog, Kind: "shell",
			Cmd: fmt.Sprintf("(man -P cat %[1]s 2>/dev/null | col -b 2>/dev/null || %[1]s --help 2>&1) | head -n 60", prog), Intent: "manual"})
	}
	if f, n := olErrLocation(out, root); f != "" {
		items = append(items, olAction{Key: "erro", Label: fmt.Sprintf("abrir %s:%d", f, n), Kind: "shell",
			Cmd: fmt.Sprintf("awk 'NR>=%d && NR<=%d {printf \"%%5d  %%s\\n\", NR, $0}' %s", max(n-6, 1), n+6, fpShQuote(f)), Intent: "error_location"})
	}
	term := olSearchTerm(olLastLine(out))
	if term == "" && len(fields) > 1 {
		term = olSearchTerm(strings.Join(fields[1:], " "))
	}
	if term != "" {
		items = append(items, olAction{Key: "procurar", Label: "procurar '" + term + "' no código", Kind: "shell", Cmd: olSearchCmd(term, hasRg), Intent: "search"})
	}
	if len(items) > olMenuMax {
		items = items[:olMenuMax]
	}
	return items
}

func olLastLine(s string) string {
	s = strings.TrimRight(s, "\n ")
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// olPick resolves a reply to a menu: a digit, or the first letters (2+) of
// exactly one item's label or key. -1 = not a pick (send it as a prompt).
func olPick(items []olAction, reply string) int {
	r := strings.TrimSpace(strings.ToLower(reply))
	if r == "" || len(items) == 0 {
		return -1
	}
	if n, err := strconv.Atoi(r); err == nil {
		if n >= 1 && n <= len(items) {
			return n - 1
		}
		return -1
	}
	if len([]rune(r)) < 2 || len(r) > 24 || strings.Contains(r, " ") && len(r) > 12 {
		return -1
	}
	hit := -1
	for i, it := range items {
		if strings.HasPrefix(strings.ToLower(it.Label), r) || strings.HasPrefix(strings.ToLower(it.Key), r) {
			if hit >= 0 {
				return -1 // ambiguous: a prompt
			}
			hit = i
		}
	}
	return hit
}

// olMenuView renders the menu (Nord look, one screenful).
func olMenuView(t Theme, title string, items []olAction, w int) string {
	g := t.G()
	dim, reset, acc := t.fg(cGray, 90), t.sgr("0"), t.fg(cAccent, 36)
	var b strings.Builder
	b.WriteString(clipVisible("  "+t.fg(cGold, 33)+g.warn+reset+" "+t.sgr("1")+title+reset, w-1) + "\n")
	for i, it := range items {
		cmd := ""
		if it.Cmd != "" && it.Kind != "sudo" {
			cmd = "  " + dim + oneLine(it.Cmd, 40) + reset
		}
		b.WriteString(clipVisible(fmt.Sprintf("    %s%s%d%s  %s%s", acc, t.sgr("1"), i+1, reset, it.Label, cmd), w-1) + "\n")
	}
	b.WriteString("  " + dim + "digite o número (ou o começo da opção); qualquer outro texto vai como pedido normal" + reset + "\n")
	return b.String()
}

// olMenuLine is the collapsed one-line form for the status line.
func olMenuLine(items []olAction) string {
	var parts []string
	for i, it := range items {
		if it.Kind == "wait" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%d %s", i+1, it.Key))
	}
	if len(parts) == 0 {
		return ""
	}
	return "[" + strings.Join(parts, " · ") + " · esc cancela]"
}

// ---------- App glue ----------

// olMenuState gathers the inputs for olMenu (git dirty with a 2 s cap).
func (a *App) olMenuState(text string, offline, waiting bool) olMenuIn {
	p := fpDetect(a.root)
	in := olMenuIn{Text: text, Offline: offline, Waiting: waiting, Proj: p, Root: a.root, HasRg: a.tools != nil && a.tools.hasRg}
	if a.ui != nil {
		in.LastErr = a.ui.lastErrText()
	}
	if p.Git {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		out, err := exec.CommandContext(ctx, "git", "-C", a.root, "status", "--porcelain").Output()
		cancel()
		in.GitDirty = err == nil && len(strings.TrimSpace(string(out))) > 0
	}
	return in
}

// olOffer prints a menu above the input and remembers it for the next reply.
func (a *App) olOffer(title string, items []olAction) {
	if len(items) == 0 {
		return
	}
	a.menu = items
	a.ui.print(olMenuView(a.ui.T, title, items, termWidth()))
}

// olTakePick: the reply picks an item of the pending menu (which is cleared
// either way). ok=false = send the reply as a normal prompt.
func (a *App) olTakePick(reply string) (olAction, bool) {
	items := a.menu
	a.menu = nil
	if i := olPick(items, reply); i >= 0 {
		return items[i], true
	}
	return olAction{}, false
}

// olRun runs a deterministic action and shows it like a tool call of the
// model path (call line, result line, then the output as an answer block).
func (a *App) olRun(ctx context.Context, act olAction) bool {
	switch act.Kind {
	case "slash":
		a.command(act.Cmd)
		return true
	case "wait":
		a.ui.Notice("ok, esperando o modelo")
		return true
	case "online":
		a.olCommand("/online", "")
		return true
	case "summary":
		return a.olSummary(ctx)
	case "script":
		a.ui.ToolStart("offer", "script", act.Script)
		t0 := time.Now()
		out, ok := a.tools.scRunDirect(ctx, act.Script, act.Args)
		a.ui.ToolEnd("offer", "script", ToolOutput{Text: out, IsErr: !ok, Display: Display{Summary: "script " + act.Script}}, time.Since(t0))
		a.ui.ActionBody(out)
		return ok
	case "sudo":
		// sudo may ask for a password: run it on the real terminal
		a.ui.Notice("rodando no terminal: " + act.Cmd)
		var code int
		uiCooked(func() {
			c := exec.Command("sh", "-c", act.Cmd)
			c.Dir, c.Stdin, c.Stdout, c.Stderr = a.root, os.Stdin, os.Stdout, os.Stderr
			if err := c.Run(); err != nil {
				code = 1
				if ee, ok := err.(*exec.ExitError); ok {
					code = ee.ExitCode()
				}
			}
		})
		a.ui.Notice(fmt.Sprintf("exit %d", code))
		return code == 0
	}
	if ok, why := a.agent.PermitCommand(ctx, act.Cmd); !ok {
		a.ui.Warn("ação não executada: " + oneLine(why, 200))
		return false
	}
	a.ui.ToolStart("offer", "bash", act.Cmd)
	t0 := time.Now()
	out, code := olExec(ctx, a.root, act.Cmd)
	out = redactSecrets(out)
	sum := fmt.Sprintf("exit %d · %s · %s", code, time.Since(t0).Round(100*time.Millisecond), act.Label)
	body := out
	if act.Kind == "test" {
		if s := a.olTestSummary(ctx, out); s != "" {
			sum = fmt.Sprintf("exit %d · %s", code, firstLine(s))
			body = s + "\n\n" + truncateHeadTail(out, 3000)
		}
	}
	if h := niEmptySearchHint(a.root, act.Intent, act.Cmd, out, code); h != "" { // typo in the identifier (nameindex.go)
		out += "\n" + h
		body += "\n" + h
	}
	a.ui.ToolEnd("offer", "bash", ToolOutput{Text: out, IsErr: code != 0, Display: Display{Summary: sum}}, time.Since(t0))
	a.ui.ActionBody(body)
	dbg("fastpath", map[string]any{"intent": act.Intent, "cmd": act.Cmd, "exit": code, "menu": true})
	a.agent.Note(fmt.Sprintf("<system-reminder>The harness ran `%s` for the user (%s, exit %d). Output:\n%s</system-reminder>", act.Cmd, act.Intent, code, truncateHeadTail(out, 6000)))
	return code == 0
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// olExec runs cmd with sh in root (its own process: safe while the agent's
// shell is busy), output capped at 256 KB.
func olExec(ctx context.Context, root, cmd string) (string, int) {
	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	c.Dir = root
	c.Env = append(os.Environ(), "GIT_PAGER=cat", "PAGER=cat", "NO_COLOR=1")
	b, err := c.CombinedOutput()
	if len(b) > 256<<10 {
		b = append(b[:256<<10], []byte("\n… (saída cortada em 256 KB)")...)
	}
	code := 0
	if err != nil {
		code = 1
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
	}
	return string(b), code
}

// olTestSummary parses test output with the parse_test_output script:
// "N falhas (framework)" and one file:line line per failure.
func (a *App) olTestSummary(ctx context.Context, out string) string {
	if a.tools == nil || !a.tools.Scripts {
		return ""
	}
	args, _ := json.Marshal(map[string]any{"text": truncateHeadTail(out, 200000)})
	res, ok := a.tools.scRunDirect(ctx, "parse_test_output", string(args))
	if !ok {
		return ""
	}
	return olFormatTests(res)
}

// olFormatTests turns parse_test_output JSON into a short report.
func olFormatTests(js string) string {
	var r struct {
		Data struct {
			Framework string `json:"framework"`
			Status    string `json:"status"`
			Passed    int    `json:"passed"`
			Failed    int    `json:"failed"`
			Skipped   int    `json:"skipped"`
			Failures  []struct {
				Test, File, Message string
				Line                int
			} `json:"failures"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(js), &r) != nil || r.Data.Framework == "" {
		return ""
	}
	d := r.Data
	var b strings.Builder
	if d.Failed == 0 && d.Status != "fail" {
		fmt.Fprintf(&b, "testes passaram: %d ok, %d pulados (%s)", d.Passed, d.Skipped, d.Framework)
		return b.String()
	}
	fmt.Fprintf(&b, "%d %s, %d ok (%s)", d.Failed, uiPl(d.Failed, "falha", "falhas"), d.Passed, d.Framework)
	for i, f := range d.Failures {
		if i == 15 {
			fmt.Fprintf(&b, "\n… +%d", len(d.Failures)-15)
			break
		}
		loc := f.File
		if f.Line > 0 {
			loc += ":" + strconv.Itoa(f.Line)
		}
		fmt.Fprintf(&b, "\n- %s %s %s", f.Test, loc, oneLine(f.Message, 120))
	}
	return b.String()
}

// olSummary: detect_project + tree_summary, as text.
func (a *App) olSummary(ctx context.Context) bool {
	if a.tools == nil || !a.tools.Scripts {
		a.ui.Warn("resumo do projeto precisa dos scripts (python3)")
		return false
	}
	a.ui.ToolStart("offer", "script", "detect_project + tree_summary")
	t0 := time.Now()
	dp, ok1 := a.tools.scRunDirect(ctx, "detect_project", "")
	ts, ok2 := a.tools.scRunDirect(ctx, "tree_summary", `{"depth":1}`)
	text := olFormatSummary(dp, ts)
	a.ui.ToolEnd("offer", "script", ToolOutput{Text: text, IsErr: !ok1 && !ok2, Display: Display{Summary: "resumo do projeto"}}, time.Since(t0))
	a.ui.ActionBody(text)
	return ok1 || ok2
}

// olFormatSummary turns detect_project and tree_summary JSON into text.
func olFormatSummary(dp, ts string) string {
	var p struct {
		Data struct {
			Langs                  []string `json:"langs"`
			Test, Build, Lint, Fmt string
			Entry                  []string `json:"entry_points"`
			Files                  int      `json:"files"`
			Git                    bool     `json:"git"`
		} `json:"data"`
	}
	var tr struct {
		Data struct {
			Files int `json:"files"`
			LOC   int `json:"loc"`
			Dirs  []struct {
				Path  string `json:"path"`
				Files int    `json:"files"`
				LOC   int    `json:"loc"`
			} `json:"dirs"`
		} `json:"data"`
	}
	_ = json.Unmarshal([]byte(dp), &p)
	_ = json.Unmarshal([]byte(ts), &tr)
	var b strings.Builder
	if len(p.Data.Langs) > 0 {
		fmt.Fprintf(&b, "linguagens: %s\n", strings.Join(p.Data.Langs, ", "))
	}
	for _, kv := range [][2]string{{"testes", p.Data.Test}, {"build", p.Data.Build}, {"lint", p.Data.Lint}, {"formatação", p.Data.Fmt}} {
		if kv[1] != "" {
			fmt.Fprintf(&b, "%s: %s\n", kv[0], kv[1])
		}
	}
	if len(p.Data.Entry) > 0 {
		fmt.Fprintf(&b, "entrada: %s\n", strings.Join(p.Data.Entry, ", "))
	}
	if tr.Data.Files > 0 {
		fmt.Fprintf(&b, "%d arquivos, %d linhas\n", tr.Data.Files, tr.Data.LOC)
		for i, d := range tr.Data.Dirs {
			if i == 12 {
				fmt.Fprintf(&b, "… +%d pastas\n", len(tr.Data.Dirs)-12)
				break
			}
			fmt.Fprintf(&b, "  %-24s %4d arq  %6d linhas\n", d.Path, d.Files, d.LOC)
		}
	}
	if b.Len() == 0 {
		return strings.TrimSpace(dp + "\n" + ts)
	}
	return strings.TrimRight(b.String(), "\n")
}

// ---------- waiting for a slow model ----------

// olWaitOffers installs the collapsed menu for this turn: it shows in the
// status line once the first token is 5 s late; a digit runs the action
// while the request goes on.
func (a *App) olWaitOffers(text string) {
	if a.ui == nil || !a.ui.lv.on {
		return
	}
	items := olMenu(a.olMenuState(text, false, true))
	a.ui.SetOffers(items, func(act olAction) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		a.olRun(ctx, act)
	})
}

// ---------- offline background probe ----------

// olBgEvery is how often offline mode probes the providers in the background.
var olBgEvery = 30 * time.Second

// olWatch probes quietly while offline; when a provider answers it says so
// at once (above the input) and the next message goes to the model.
func (a *App) olWatch() {
	if a.ol.forced || !a.olWatching.CompareAndSwap(false, true) {
		return
	}
	cfg, ref := a.cfg, a.agent.Model
	first, _ := a.agent.FirstChoice()
	gen := a.olGen.Load()
	every := olBgEvery // read once here: tests swap the global
	safeGo(func() {
		defer a.olWatching.Store(false)
		for {
			a.olNext.Store(time.Now().Add(every).UnixNano())
			time.Sleep(every)
			if a.olGen.Load() != gen {
				return // offline mode ended (or restarted) meanwhile
			}
			if olProbe(cfg, ref) {
				a.olBack.Store(true)
				a.ui.AsyncNotice("rede de volta → modo online: a próxima mensagem vai pro modelo (" + uiShortModel(first) + ")")
				return
			}
		}
	})
}

// ---------- startup line ----------

// FirstChoice is the model the next main request starts with, and the chain.
func (a *Agent) FirstChoice() (string, []string) {
	need := Need{Tools: !a.textMode(a.Model), Role: "main", Prefer: a.Model}
	cands := a.acDemoteStalled(a.bgOrderCands(a.cfg.CandidatesFor(need, a.Model)))
	if a.FastFirst {
		cands = a.acFastFirst(cands)
	}
	if len(cands) == 0 {
		return a.Model, nil
	}
	return cands[0], cands
}

// startLine is the one line printed at startup: the model that answers
// first, the provider state from the last rate-limit/doctor records, and a
// quiet background network probe (a notice follows if it fails).
func (a *App) startLine() string {
	t, g := a.ui.T, a.ui.T.G()
	dim, reset := t.fg(cGray, 90), t.sgr("0")
	first, chain := a.agent.FirstChoice()
	parts := []string{"modelo " + reset + t.sgr("1") + uiShortModel(first) + reset + dim}
	if first != a.agent.Model {
		parts[0] += " (interativo: o mais rápido; reserva " + uiShortModel(a.agent.Model) + ")"
	}
	if s := bgScoreOf(first); s.OK >= bgScoreMinObs && s.MS > 0 {
		parts = append(parts, fmt.Sprintf("~%.0f s por resposta", s.MS/1000))
	}
	var bad []string
	for _, r := range append([]string{first}, chain...) {
		if until, dead := ExhaustedUntil(r); dead {
			bad = append(bad, uiShortModel(r)+" sem cota até "+until.Format("15:04"))
		} else if n, last, _ := rlInfo(r); n >= 3 && last != "" {
			bad = append(bad, uiShortModel(r)+" falhando ("+oneLine(last, 40)+")")
		}
		if len(bad) == 2 {
			break
		}
	}
	if len(bad) > 0 {
		parts = append(parts, t.fg(cGold, 33)+strings.Join(bad, "; ")+dim)
	} else {
		parts = append(parts, fmt.Sprintf("%d %s na fila de reserva", max(len(chain)-1, 0), uiPl(max(len(chain)-1, 0), "modelo", "modelos")))
	}
	if uiLowPower() {
		parts = append(parts, "modo econômico")
	}
	if !a.ol.forced && !a.opts.offline && !olForcedEnv() {
		cfg, ref := a.cfg, a.agent.Model
		safeGo(func() {
			if !olProbe(cfg, ref) {
				a.ui.AsyncNotice("sem rede agora (a sonda não alcançou os provedores) — se continuar, o jazz entra no modo offline sozinho; atalhos e !comando funcionam")
			}
		})
	}
	return "  " + dim + g.dot + " " + uiWrapMsg(strings.Join(parts, " "+g.dot+" "), termWidth()-1) + reset + "\n"
}

// olFooter is the offline part of the input footer: a live countdown to the
// next background probe ("" when online).
func (a *App) olFooter() string {
	if !a.ol.on && !a.ol.forced {
		return ""
	}
	if a.ol.forced {
		return "offline (forçado) · /online volta"
	}
	if a.olBack.Load() {
		return "rede de volta · a próxima mensagem vai pro modelo"
	}
	left := time.Until(time.Unix(0, a.olNext.Load())).Round(time.Second)
	if left < 0 {
		left = 0
	}
	return fmt.Sprintf("sem rede · nova tentativa em %s · /online agora", left)
}
