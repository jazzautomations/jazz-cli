package main

// Offline / no-key mode (docs/research/tocomas-plan-reuse.md A2) and the
// interactive/headless glue for fast paths (intents.go) and /script.
//
// When every model fails with a network or credential error (or there is no
// candidate at all), jazz stays usable instead of exiting: fast paths, the
// script tool via /script, !cmd, search with rg/fd and /undo keep working, a
// banner says so, and the next request after 60 s (or /online) probes the
// providers and goes back to model mode when one answers. JAZZ_OFFLINE=1 or
// --offline forces it (a local model, e.g. llama.cpp, still answers if it is
// the session's model).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// olProbeEvery is the minimum time between provider probes when a message is
// sent while offline (the background probe, olBgEvery, runs besides).
var olProbeEvery = 30 * time.Second

// olState is the App's offline mode.
type olState struct {
	on        bool
	forced    bool // JAZZ_OFFLINE=1, --offline or /offline
	reason    string
	lastProbe time.Time
}

// olOffWord: "off", "0", "false", "no"… turn a feature off.
func olOffWord(s string) bool {
	if pfOffWord(s) {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "0", "false", "no", "nao", "não":
		return true
	}
	return false
}

// olForcedEnv reports JAZZ_OFFLINE=1.
func olForcedEnv() bool {
	v := strings.TrimSpace(os.Getenv("JAZZ_OFFLINE"))
	return v != "" && !olOffWord(v)
}

// olIsOffline reports whether err means "no model is reachable or accepts our
// credentials" (network down, DNS, refused key, no key) rather than a
// transient provider problem (429, 5xx, stall) or a bad request.
func olIsOffline(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status == 0 || ae.Status == 401 || ae.Status == 403
	}
	var ne net.Error
	var de *net.DNSError
	var oe *net.OpError
	if errors.As(err, &de) || errors.As(err, &oe) || (errors.As(err, &ne) && ne.Timeout()) {
		return true
	}
	s := strings.ToLower(err.Error())
	for _, k := range []string{"sem chave para", "não logado", "nenhum modelo disponível", "precisa do id da conta",
		"no such host", "connection refused", "network is unreachable", "no route to host", "dial tcp", "tls handshake timeout"} {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

// olNoCandidates reports whether no candidate for ref has credentials.
func olNoCandidates(cfg *Config, ref string) bool {
	for _, c := range cfg.CandidatesFor(Need{}, ref) {
		p, _ := SplitModel(c)
		if cfg.HasCreds(p) {
			return false
		}
	}
	return true
}

// olIsLocalRef reports whether ref runs on a local/LAN server (llama.cpp, Ollama…).
func olIsLocalRef(cfg *Config, ref string) bool {
	p, _ := SplitModel(ref)
	pc := cfg.Providers[p]
	return pc != nil && pc.Local
}

// olLocalModels lists reachable local refs with a default model.
func olLocalModels(cfg *Config) []string {
	var out []string
	for _, n := range cfg.ProviderNames() {
		if pc := cfg.Providers[n]; pc != nil && pc.Local && pc.DefaultModel != "" && cfg.HasCreds(n) {
			out = append(out, n+"/"+pc.DefaultModel)
		}
	}
	return out
}

// olProbeHosts are the host:port of the first candidates' endpoints.
func olProbeHosts(cfg *Config, ref string) []string {
	if p := pvProxyFor(); p != "" {
		return []string{p} // every request goes through the proxy
	}
	seen := map[string]bool{}
	var out []string
	for _, c := range cfg.CandidatesFor(Need{}, ref) {
		p, _ := SplitModel(c)
		pc := cfg.Providers[p]
		if pc == nil || pc.BaseURL == "" {
			continue
		}
		u, err := url.Parse(strings.ReplaceAll(pc.BaseURL, "{ACCOUNT}", "x"))
		if err != nil || u.Hostname() == "" {
			continue
		}
		port := u.Port()
		if port == "" {
			port = map[string]string{"http": "80"}[u.Scheme]
			if port == "" {
				port = "443"
			}
		}
		hp := net.JoinHostPort(u.Hostname(), port)
		if !seen[hp] {
			seen[hp] = true
			out = append(out, hp)
		}
		if len(out) == 6 {
			break
		}
	}
	return out
}

// olDial is the probe's dialer (tests replace it).
var olDial = func(hp string, d time.Duration) error {
	c, err := net.DialTimeout("tcp", hp, d)
	if err == nil {
		c.Close()
	}
	return err
}

// olProbe dials the candidates' endpoints in parallel (1.5 s); true = one answered.
// It is cheap on purpose: no request, no quota, no tokens.
func olProbe(cfg *Config, ref string) bool {
	hosts := olProbeHosts(cfg, ref)
	if len(hosts) == 0 {
		return false
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	up := false
	for _, h := range hosts {
		wg.Add(1)
		go func(h string) {
			defer wg.Done()
			if olDial(h, 1500*time.Millisecond) == nil {
				mu.Lock()
				up = true
				mu.Unlock()
			}
		}(h)
	}
	wg.Wait()
	return up
}

// olNetReason: the reason is about the network (not credentials).
func olNetReason(reason string) bool {
	r := strings.ToLower(reason)
	for _, k := range []string{"rede", "conex", "dns", "host", "dial", "unreachable", "timeout", "proxy", "não responde"} {
		if strings.Contains(r, k) {
			return true
		}
	}
	return false
}

// olBanner is the offline explanation shown once when the mode starts: what
// still works without a model.
func olBanner(cfg *Config, reason string) string {
	var b strings.Builder
	if olNetReason(reason) {
		b.WriteString("sem rede → modo offline: atalhos determinísticos ativos; /online para tentar de novo")
		b.WriteString("\n  motivo: " + reason)
	} else {
		b.WriteString("modo offline")
		if reason != "" {
			b.WriteString(": " + reason)
		}
		b.WriteString(" — atalhos determinísticos ativos; /online para tentar de novo")
	}
	b.WriteString("\n  peça direto: \"rode os testes\" · \"mostra o git status\" · \"o que mudou\" · \"procura X no código\" · \"resumo do projeto\"")
	b.WriteString("\n  !comando roda no shell (rg/fd também) · /undo e /rewind desfazem · /resume abre sessões · /script [nome] [json]")
	if l := olLocalModels(cfg); len(l) > 0 {
		b.WriteString("\n  modelo local disponível: /model " + l[0])
	}
	b.WriteString("\n  verifico a rede a cada 30 s e volto pro modelo sozinho")
	return b.String()
}

// olScriptPT is the one-line purpose of each built-in script, in Portuguese.
var olScriptPT = map[string]string{
	"csv_stats":          "perfil de um CSV: linhas, tipos, mín/máx, valores comuns",
	"detect_project":     "tipo do projeto: linguagens, comandos de build/teste/lint",
	"diff_quality":       "checklist do diff: prints de debug, TODOs, código comentado",
	"extract_stacktrace": "último stack trace de um log, com os frames do projeto",
	"extract_versions":   "versões semver, URLs e e-mails num arquivo ou texto",
	"find_symbol":        "onde um símbolo é definido e quantas referências tem",
	"git_safe":           "mudanças git recuperáveis: stash, branch, revert de arquivo",
	"git_state":          "git num relance: branch, à frente/atrás, staged, conflitos",
	"http_check":         "GET/HEAD numa URL local: status, tempo, cabeçalhos",
	"json_edit":          "ler/alterar um valor de um JSON por caminho",
	"parse_build_errors": "erros de compilador/linter como arquivo:linha:coluna",
	"parse_log_lines":    "resumo de log: contagem por nível, mensagens mais comuns",
	"parse_test_output":  "resumo da saída de testes: falhas com arquivo:linha",
	"port_free":          "porta TCP livre? se não, o processo que escuta",
	"run_tests":          "roda os testes detectados e lista as falhas",
	"snapshot_cmd":       "teste de caracterização: salva a saída e compara depois",
	"tree_summary":       "visão das pastas: arquivos e linhas por extensão",
	"ui_lint":            "lint de UI (html/jsx/css): acessibilidade, clichês",
	"yaml_get":           "ler um valor de um YAML por caminho",
}

// olScriptsView lists the scripts that work offline, two per line when they fit.
func olScriptsView(t Theme, tb *Toolbox, w int) string {
	if tb == nil || !tb.Scripts {
		return ""
	}
	r := tb.scRegistry()
	if len(r.names) == 0 {
		return ""
	}
	dim, reset := t.fg(cGray, 90), t.sgr("0")
	var b strings.Builder
	b.WriteString("  " + dim + fmt.Sprintf("scripts sem modelo (/script nome) — %d:", len(r.names)) + reset + "\n")
	for _, n := range r.names {
		d := olScriptPT[n]
		if d == "" {
			d = r.defs[n].Description
		}
		b.WriteString(clipVisible(fmt.Sprintf("    %s%-18s%s %s%s%s", t.sgr("1"), n, reset, dim, d, reset), w-1) + "\n")
	}
	return b.String()
}

// olDoctorLine summarizes scripts, fast paths and offline mode for jazz doctor.
func olDoctorLine(cfg *Config, root string) string {
	sc := "off"
	if pfScriptsOn(cfg, "") {
		r := scLoad(root)
		sc = fmt.Sprintf("%d", len(r.names))
		if r.python == "" {
			sc += " (sem python3: só os de sh)"
		}
	}
	fp := "on"
	if !fpEnabled(cfg) {
		fp = "off"
	}
	off := "automático"
	if olForcedEnv() {
		off = "forçado (JAZZ_OFFLINE)"
	}
	return fmt.Sprintf("scripts: %s · atalhos: %s · offline: %s", sc, fp, off)
}

// ---------- Agent seam ----------

// PermitCommand runs the normal permission check (rules, mode, danger,
// smart classifier, approval prompt) for a bash command the harness wants to
// run on the user's behalf (fast paths). ok=false carries the reason.
func (a *Agent) PermitCommand(ctx context.Context, cmd string) (bool, string) {
	b, _ := json.Marshal(map[string]any{"command": cmd})
	tc := ToolCall{Name: "bash", Args: string(b)}
	ok, why, _ := a.permit(ctx, tc, toolDetail("bash", tc.Args), false, acHookVerdict{})
	return ok, why
}

// ---------- interactive ----------

// olStart runs once after the welcome screen.
func (a *App) olStart() {
	switch {
	case a.opts.offline || olForcedEnv():
		a.ol.forced = true
		a.olEnter("forçado (JAZZ_OFFLINE=1 / --offline)")
	case olNoCandidates(a.cfg, a.agent.Model):
		a.olEnter("nenhum provedor com credencial (/key ou /login)")
	}
}

func (a *App) olEnter(reason string) {
	first := !a.ol.on
	a.ol.on, a.ol.reason, a.ol.lastProbe = true, reason, time.Now()
	if first {
		a.olGen.Add(1)
		a.olBack.Store(false)
		a.ui.Warn(olBanner(a.cfg, reason))
		if v := olScriptsView(a.ui.T, a.tools, termWidth()); v != "" {
			a.ui.print(v)
		}
	} else {
		a.ui.Warn("ainda offline: " + reason)
	}
	a.olWatch()
}

func (a *App) olLeave(why string) {
	if !a.ol.on {
		return
	}
	a.ol = olState{}
	a.olGen.Add(1)
	a.ui.Notice("modelo de volta (" + why + ")")
}

// olSummaryRe: "resumo do projeto" and friends (answered by scripts offline).
var olSummaryRe = regexp.MustCompile(`(?i)^(?:(?:me )?(?:da|de|faz|faca|mostra|mostre)(?: um| o)? )?(?:resumo|resuma|visao geral|overview|sumario)(?: (?:do|o|deste|desse|of the|of this))? ?(?:projeto|repo|repositorio|codigo|project|codebase)?$`)

// olBefore runs before a message goes to the model: a fast path may answer
// it, and offline mode keeps it away from unreachable providers.
// handled = nothing else to do; otherwise text (possibly with the fast
// path's failing output appended) goes to the model.
func (a *App) olBefore(ctx context.Context, text string) (handled bool, out string) {
	if a.olBack.Swap(false) && a.ol.on && !a.ol.forced {
		a.ol = olState{} // the background probe found a provider (and said so)
		a.olGen.Add(1)
	}
	// a reply to a pending numbered menu runs the chosen action
	if act, ok := a.olTakePick(text); ok {
		a.olRun(ctx, act)
		return true, text
	}
	offline := a.ol.on || a.ol.forced
	if offline && olSummaryRe.MatchString(fpNormalize(text)) {
		a.olSummary(ctx)
		return true, text
	}
	if offline || fpEnabled(a.cfg) {
		if m, ok := fpIntents(a.root).Match(text, fpDetect(a.root), a.tools.hasRg); ok {
			done, note := a.olFastPath(ctx, m, offline)
			if done {
				return true, text
			}
			if note != "" {
				text += "\n\n" + note
			}
		}
	}
	if !offline {
		return false, text
	}
	if olIsLocalRef(a.cfg, a.agent.Model) {
		return false, text // a local model needs no network
	}
	if a.ol.forced {
		a.olOffer("offline (forçado): sem modelo — o que dá pra fazer agora:", olMenu(a.olMenuState(text, true, false)))
		return true, text
	}
	if wait := olProbeEvery - time.Since(a.ol.lastProbe); wait > 0 {
		a.olOffer(fmt.Sprintf("offline: sem modelo agora (testo a rede de novo em %s) — o que dá pra fazer:", wait.Round(time.Second)), olMenu(a.olMenuState(text, true, false)))
		return true, text
	}
	a.ol.lastProbe = time.Now()
	a.ui.Thinking("testando a rede")
	up := olProbe(a.cfg, a.agent.Model)
	a.ui.StopThinking()
	if !up {
		a.olOffer("offline: nenhum provedor alcançável — o que dá pra fazer sem modelo:", olMenu(a.olMenuState(text, true, false)))
		return true, text
	}
	return false, text // reachable: try the model; olAfter decides
}

// olAfter runs after a model turn: success leaves offline mode, a
// network/credential failure enters it. true = the error was reported.
func (a *App) olAfter(err error) bool {
	if err == nil {
		if a.ol.on && !a.ol.forced {
			a.olLeave("um provedor respondeu")
		}
		return false
	}
	if errors.Is(err, context.Canceled) || !olIsOffline(err) {
		return false
	}
	a.olEnter(friendlyErr(err))
	return true
}

// olFastPath confirms and runs a fast path. done = the request is answered;
// otherwise note is the failure report for the model ("" = user chose the model).
func (a *App) olFastPath(ctx context.Context, m fpMatch, offline bool) (done bool, note string) {
	if offline {
		// no model to send it to: run it now, shown like a tool call
		kind := "shell"
		if m.Intent == "test" {
			kind = "test"
		}
		a.olRun(ctx, olAction{Kind: kind, Cmd: m.Cmd, Label: "atalho " + m.Intent, Intent: m.Intent})
		return true, ""
	}
	q := "⚡ atalho: " + m.Cmd + "  [Enter] rodar  [Esc] mandar pro modelo"
	if r := a.ui.Ask(q, "\r\ns"); r == 0 {
		if offline {
			return true, ""
		}
		return false, ""
	}
	if ok, why := a.agent.PermitCommand(ctx, m.Cmd); !ok {
		a.ui.Warn("atalho não executado: " + oneLine(why, 200))
		return true, ""
	}
	cctx, cancel := context.WithCancel(ctx)
	stop := a.ui.WatchTurn(cancel, nil)
	a.ui.Thinking("rodando")
	t0 := time.Now()
	r := a.tools.Shell.Run(cctx, m.Cmd, 10*time.Minute, a.ui.ShellOutput)
	a.ui.StopThinking()
	stop()
	cancel()
	out := redactSecrets(r.Output)
	a.ui.print(sanitizeTerm(out))
	if out != "" && !strings.HasSuffix(out, "\n") {
		a.ui.print("\n")
	}
	a.ui.Notice(fmt.Sprintf("exit %d %s %s %s atalho %s", r.ExitCode, a.ui.T.G().dot, time.Since(t0).Round(100*time.Millisecond), a.ui.T.G().dot, m.Intent))
	dbg("fastpath", map[string]any{"intent": m.Intent, "cmd": m.Cmd, "exit": r.ExitCode, "offline": offline})
	clip := truncateHeadTail(out, 8000)
	if r.ExitCode == 0 && !r.Reset {
		a.agent.Note(fmt.Sprintf("<system-reminder>The harness ran `%s` for the user's request %q (fast path, exit 0). Output:\n%s</system-reminder>", m.Cmd, m.Intent, clip))
		return true, ""
	}
	if offline {
		a.ui.Notice("offline: sem modelo pra analisar a falha agora")
		a.agent.Note(fmt.Sprintf("<system-reminder>While offline the harness ran `%s` (fast path) and it failed (exit %d). Output:\n%s</system-reminder>", m.Cmd, r.ExitCode, clip))
		return true, ""
	}
	return false, fmt.Sprintf("<system-reminder>The harness already ran `%s` for this request (fast path) and it failed (exit %d). Do not re-run it before reading this output:\n%s</system-reminder>", m.Cmd, r.ExitCode, clip)
}

// olCommand handles /offline, /online and /script; false = not ours.
func (a *App) olCommand(cmd, arg string) bool {
	switch cmd {
	case "/offline":
		a.ol.forced = true
		a.olEnter("forçado (/offline)")
	case "/online":
		a.ol.forced = false
		a.ol.lastProbe = time.Time{}
		if !a.ol.on {
			a.ui.Notice("já está no modo com modelo")
			return true
		}
		a.ui.Thinking("procurando provedores")
		up := olProbe(a.cfg, a.agent.Model)
		a.ui.StopThinking()
		if up {
			a.ol.on = false
			a.ui.Notice("um provedor está alcançável: a próxima mensagem vai pro modelo")
		} else {
			a.ol.lastProbe = time.Now()
			a.ui.Warn("nenhum provedor alcançável ainda")
		}
	case "/script":
		a.scriptCmd(arg)
	default:
		return false
	}
	return true
}

// scriptCmd: "/script" lists, "/script name {json}" runs one.
func (a *App) scriptCmd(arg string) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		l := a.tools.scList()
		if len(l) == 0 {
			a.ui.Warn("nenhum script disponível (python3/sh ausentes?)")
			return
		}
		sort.Strings(l)
		a.ui.print("  " + strings.Join(l, "\n  ") + "\n")
		a.ui.Notice("uso: /script <nome> {\"arg\": valor}")
		return
	}
	name, rest, _ := strings.Cut(arg, " ")
	ctx, cancel := context.WithCancel(context.Background())
	stop := a.ui.WatchTurn(cancel, nil)
	a.ui.Thinking("rodando " + name)
	out, ok := a.tools.scRunDirect(ctx, name, rest)
	a.ui.StopThinking()
	stop()
	cancel()
	a.ui.print(sanitizeTerm(out) + "\n")
	if !ok {
		a.ui.Warn("script " + name + " falhou")
	}
	a.agent.Note(fmt.Sprintf("<system-reminder>The user ran the script %s themselves. Result:\n%s</system-reminder>", name, truncateHeadTail(out, 6000)))
}

// cmdScript implements `jazz script [name] ['{json}']`: list, or run one script
// with no model (offline use, evals). Project scripts follow `jazz trust`.
// Exit 0 = ok:true, 1 = failed, 2 = usage.
func cmdScript(root string, args []string) int {
	tb := NewToolbox(root)
	tb.Scripts = true
	defer tb.Shell.Close()
	if len(args) == 0 {
		l := tb.scList()
		if len(l) == 0 {
			fmt.Fprintln(os.Stderr, "jazz: nenhum script disponível (python3/sh ausentes?)")
			return 1
		}
		fmt.Println(strings.Join(l, "\n"))
		return 0
	}
	raw := strings.Join(args[1:], " ")
	if raw == "-" {
		b, _ := io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
		raw = string(b)
	}
	out, ok := tb.scRunDirect(context.Background(), args[0], raw)
	fmt.Println(out)
	if !ok {
		return 1
	}
	return 0
}

// ---------- headless ----------

// olHeadless handles fast paths and forced offline mode for `jazz -p`, after
// sink.Begin. res != nil = the run is over (main finishes it); otherwise
// prompt (with a failed fast path's output appended) goes to the model.
// Fast paths fire only with --fast-path or offline.
func olHeadless(ctx context.Context, ag *Agent, tools *Toolbox, ui UI, cfg *Config, prompt string, o pfOpts) (*RunResult, string) {
	offline := (o.offline || olForcedEnv()) && !olIsLocalRef(cfg, ag.Model) // a local model is allowed offline
	if !offline && !o.fastPath {
		return nil, prompt
	}
	m, ok := fpIntents(tools.Root).Match(prompt, fpDetect(tools.Root), tools.hasRg)
	if !ok {
		if !offline {
			return nil, prompt
		}
		return &RunResult{Status: StopError, Model: "offline",
			Err: "modo offline: nenhum atalho casa com o pedido e não há modelo (rode sem JAZZ_OFFLINE/--offline, ou use um modelo local com -m)"}, prompt
	}
	return olHeadlessRun(ctx, ag, tools, ui, m, offline, prompt)
}

// olHeadlessFallback rescues a run whose model failed with a network or
// credential error: when the prompt is a fast path, it runs that instead.
func olHeadlessFallback(ctx context.Context, ag *Agent, tools *Toolbox, ui UI, err error, prompt string) *RunResult {
	if !olIsOffline(err) {
		return nil
	}
	m, ok := fpIntents(tools.Root).Match(prompt, fpDetect(tools.Root), tools.hasRg)
	if !ok {
		return nil
	}
	fmt.Fprintln(os.Stderr, "jazz: sem modelo ("+friendlyErr(err)+"); rodando o atalho: "+m.Cmd)
	res, _ := olHeadlessRun(ctx, ag, tools, ui, m, true, prompt)
	return res
}

func olHeadlessRun(ctx context.Context, ag *Agent, tools *Toolbox, ui UI, m fpMatch, offline bool, prompt string) (*RunResult, string) {
	if ok, why := ag.PermitCommand(ctx, m.Cmd); !ok {
		return &RunResult{Status: StopDenied, Model: "fastpath", Err: "atalho negado: " + oneLine(why, 200)}, prompt
	}
	ui.ToolStart("fastpath", "bash", m.Cmd)
	r := tools.Shell.Run(ctx, m.Cmd, 10*time.Minute, nil)
	out := redactSecrets(r.Output)
	ui.ToolEnd("fastpath", "bash", ToolOutput{Text: out, IsErr: r.ExitCode != 0, Display: Display{Summary: fmt.Sprintf("exit %d · atalho %s", r.ExitCode, m.Intent)}}, r.Duration)
	dbg("fastpath", map[string]any{"intent": m.Intent, "cmd": m.Cmd, "exit": r.ExitCode, "offline": offline, "headless": true})
	text := strings.TrimRight(out, "\n")
	switch {
	case r.ExitCode == 0 && !r.Reset:
		return &RunResult{Status: StopDone, Text: text, Model: "fastpath", Tools: 1}, prompt
	case ctx.Err() != nil:
		return &RunResult{Status: StopInterrupted, Text: text, Model: "fastpath", Tools: 1}, prompt
	case offline:
		return &RunResult{Status: StopError, Text: text, Model: "fastpath", Tools: 1,
			Err: fmt.Sprintf("atalho %q falhou (exit %d) e não há modelo pra analisar", m.Cmd, r.ExitCode)}, prompt
	}
	return nil, prompt + "\n\n" + fmt.Sprintf("<system-reminder>The harness already ran `%s` for this request (fast path) and it failed (exit %d). Do not re-run it before reading this output:\n%s</system-reminder>", m.Cmd, r.ExitCode, truncateHeadTail(out, 8000))
}
