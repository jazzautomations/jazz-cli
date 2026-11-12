//go:build !windows

package main

// replay_test.go: o portão de replay (F0 do plano 0.14). Cada pasta de
// testdata/replay/<caso>/ guarda um case.json tirado de uma sessão real
// (scripts/session2case.py faz o rascunho, com segredo redigido) e o teste
// remonta a cena de forma determinística: provedor falso que registra cada
// request (harness_test.go), binários falsos no PATH quando o caso mexe no
// sistema (sudo, modprobe), sonda/runner de sudo trocados e, se pedido, um
// sandbox falso ativo. Nada sai pra rede nem roda como root.
//
// Casos cujo conserto ainda está em outra frente (F1–F4) ficam com t.Skip
// ("front" no case.json diz qual frente o habilita). JAZZ_REPLAY_ALL=1 roda
// todos — é como cada frente prova que fechou: tira o "front" do caso e o
// replay passa a valer como portão.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// rpCase is testdata/replay/<caso>/case.json.
type rpCase struct {
	Name   string `json:"name"`
	Origin string `json:"origin"` // máquina + sessão de onde veio (ou "sintético")
	Why    string `json:"why"`    // o que a sessão real mostrou
	Front  string `json:"front"`  // "" = ativo; "F1".."F4" = t.Skip até a frente fechar

	// Entry: "agent" (Agent.Run direto) ou "app" (online interativo: App.olBefore
	// decide primeiro, como no main loop; só o que ele não segura vai ao Run).
	Entry    string            `json:"entry"`
	Mode     string            `json:"mode"`     // auto | ask | turbo | yolo
	Headless bool              `json:"headless"` // -p
	Model    string            `json:"model"`    // ref pedido (default fake/m1)
	Fallback []string          `json:"fallback"` // cfg.Fallback (consentimento do usuário)
	Presets  []string          `json:"presets"`  // provedores de fábrica mantidos (todos apontados pro falso)
	Extra    []string          `json:"extra"`    // provedores falsos extras (openai, sem chave)
	Env      map[string]string `json:"env"`      // chaves de teste etc.
	Config   json.RawMessage   `json:"config"`   // pedaço de Config mesclado por último

	Dead  []rpDead          `json:"dead"`  // refs em cota antes do 1º turno
	Seed  rpSeed            `json:"seed"`  // plan cache / lições já gravados
	Files map[string]string `json:"files"` // arquivos do projeto (root)
	Stubs rpStubs           `json:"stubs"`

	Provider rpProvider `json:"provider"`
	Approve  string     `json:"approve"` // allow | always | deny (default deny)

	Turns []string `json:"turns"` // mensagens do usuário, em ordem, na mesma sessão
	// Each: cada frase vira um subteste independente (sessão nova); FrontBy
	// marca as frases que ainda dependem de uma frente.
	Each    []string          `json:"each"`
	FrontBy map[string]string `json:"front_by"`

	Expect rpExpect `json:"expect"`

	// Todo: rascunho do scripts/session2case.py ainda sem revisão humana.
	// Caso com todo não roda: o portão nunca aceita asserção não escrita.
	Todo []string `json:"todo"`
}

type rpDead struct {
	Ref     string `json:"ref"`
	Minutes int    `json:"minutes"`
}

type rpSeed struct {
	Plans []json.RawMessage `json:"plans"` // linhas de pcPlan
}

type rpStubs struct {
	Bins       []string `json:"bins"`        // binários falsos no PATH: registram a chamada e saem 0
	SudoProbe  string   `json:"sudo_probe"`  // "" (real: nunca usado em teste) | ok | fail
	SudoRunner bool     `json:"sudo_runner"` // troca nlSudoRun por um que só registra
	Sandbox    bool     `json:"sandbox"`     // sandbox falso ativo: o wrapper registra e roda; sudo falso lá dentro falha como no bwrap
}

// rpProvider: respostas roteiradas. ByRef vale por ref ("prov/model" ou só
// o model id quando o provedor é "fake"), em ordem; Seq vale pela ordem
// global dos requests; acabou o roteiro, a resposta é "ok".
type rpProvider struct {
	ByRef map[string][]rpResp `json:"by_ref"`
	Seq   []rpResp            `json:"seq"`
	// Repeat: a última resposta de cada lista se repete (ex.: 429 eterno).
	Repeat bool `json:"repeat"`
}

type rpResp struct {
	Status     int      `json:"status"`
	Body       string   `json:"body"`
	Text       string   `json:"text"`
	Calls      []rpCall `json:"calls"`
	ErrChunk   string   `json:"err_chunk"`
	ErrCodeStr string   `json:"err_code_str"`
	ErrCode    int      `json:"err_code"`
	Finish     string   `json:"finish"`
}

type rpCall struct {
	Name string `json:"name"`
	// Args: objeto JSON (serializado como veio) ou string (args crus, para
	// reproduzir JSON quebrado do modelo byte a byte).
	Args json.RawMessage `json:"args"`
}

type rpExpect struct {
	Err          string   `json:"err"`      // "" (não checa) | none | some
	Offline      *bool    `json:"offline"`  // olIsOffline(err)
	Requests     *int     `json:"requests"` // total exato
	MinRequests  int      `json:"min_requests"`
	MaxRequests  *int     `json:"max_requests"`
	ModelsOnly   []string `json:"models_only"`    // todo request vai a um destes (prefixo com * no fim)
	ModelsNever  []string `json:"models_never"`   // nenhum request a estes
	ModelsSome   []string `json:"models_some"`    // pelo menos 1 request a cada um
	FirstModel   string   `json:"first_model"`    // o 1º request vai a este (pin)
	ReqAbsent    []string `json:"request_absent"` // em nenhuma mensagem de nenhum request
	UIHas        []string `json:"ui_has"`         // texto+avisos+erros+friendlyErr
	UIHasAny     []string `json:"ui_has_any"`
	UIAbsent     []string `json:"ui_absent"`
	ErrHas       []string `json:"err_has"` // friendlyErr(err)
	ErrHasAny    []string `json:"err_has_any"`
	ErrAbsent    []string `json:"err_absent"`
	MinApprovals int      `json:"min_approvals"`
	ToolHas      []string `json:"tool_has"`    // em algum tool result
	ToolAbsent   []string `json:"tool_absent"` // em nenhum tool result
	FirstToolOK  bool     `json:"first_tool_ok"`
	BinsRan      []string `json:"bins_ran"`
	BinsNotRan   []string `json:"bins_not_ran"`
	SudoRun      *bool    `json:"sudo_run"`  // nlSudoRun chamado
	Sandboxed    *bool    `json:"sandboxed"` // o wrapper do sandbox falso foi chamado
	Handled      *bool    `json:"handled"`   // entry app: olBefore segurou a mensagem
}

func rpAll() bool { return os.Getenv("JAZZ_REPLAY_ALL") == "1" }

// rpLoad reads every case under testdata/replay.
func rpLoad(t *testing.T) []rpCase {
	t.Helper()
	dirs, err := filepath.Glob(filepath.Join("testdata", "replay", "*", "case.json"))
	if err != nil || len(dirs) == 0 {
		t.Fatalf("nenhum caso em testdata/replay (%v)", err)
	}
	sort.Strings(dirs)
	var out []rpCase
	for _, p := range dirs {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var c rpCase
		dec := json.NewDecoder(strings.NewReader(string(b)))
		dec.DisallowUnknownFields() // campo errado no case.json não pode virar asserção esquecida
		if err := dec.Decode(&c); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if want := filepath.Base(filepath.Dir(p)); c.Name != want {
			t.Fatalf("%s: name %q difere da pasta %q", p, c.Name, want)
		}
		if len(c.Todo) > 0 {
			t.Fatalf("%s: rascunho com todo %q — escreva why/front/expect e apague o todo", p, c.Todo)
		}
		if c.Origin == "" || c.Why == "" {
			t.Fatalf("%s: origin e why são obrigatórios (de onde veio o caso e o que ele prova)", p)
		}
		if len(c.Turns) == 0 && len(c.Each) == 0 {
			t.Fatalf("%s: sem turns nem each", p)
		}
		out = append(out, c)
	}
	return out
}

// TestReplay roda cada caso de testdata/replay.
func TestReplay(t *testing.T) {
	for _, c := range rpLoad(t) {
		t.Run(c.Name, func(t *testing.T) {
			if len(c.Each) > 0 {
				for _, phrase := range c.Each {
					t.Run(rpSubName(phrase), func(t *testing.T) {
						front := c.Front
						if f, ok := c.FrontBy[phrase]; ok {
							front = f
						}
						rpSkip(t, front)
						cc := c
						cc.Turns = []string{phrase}
						rpRun(t, cc)
					})
				}
				return
			}
			rpSkip(t, c.Front)
			rpRun(t, c)
		})
	}
}

func rpSkip(t *testing.T, front string) {
	t.Helper()
	if front != "" && !rpAll() {
		t.Skipf("depende de %s (JAZZ_REPLAY_ALL=1 roda mesmo assim)", front)
	}
}

func rpSubName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == ' ' || r == '/' {
			return '_'
		}
		return r
	}, s)
	if len(s) > 48 {
		s = strings.ToValidUTF8(s[:48], "")
	}
	return s
}

// rpRef is how a recorded request is named in the expectations.
func rpRef(r recReq) string {
	if r.Provider != "" {
		return r.Provider + "/" + r.Model
	}
	return r.Model
}

func rpMatch(pat, ref string) bool {
	if p, ok := strings.CutSuffix(pat, "*"); ok {
		return strings.HasPrefix(ref, p)
	}
	return pat == ref
}

func (r rpResp) fake() fakeResp {
	f := fakeResp{Status: r.Status, Body: r.Body, Text: r.Text, ErrChunk: r.ErrChunk, ErrCodeStr: r.ErrCodeStr, ErrCode: r.ErrCode, Finish: r.Finish}
	for _, c := range r.Calls {
		args := string(c.Args)
		var s string
		if json.Unmarshal(c.Args, &s) == nil {
			args = s // string: os bytes crus que o modelo mandou
		}
		if args == "" {
			args = "{}"
		}
		f.Calls = append(f.Calls, fakeCall{Name: c.Name, Args: args})
	}
	if f.Status == 0 && f.Text == "" && len(f.Calls) == 0 && f.ErrChunk == "" {
		f.Text = "ok"
	}
	return f
}

// handler scripts the fake server.
func (p rpProvider) handler() func(int, recReq) fakeResp {
	var mu sync.Mutex
	used := map[string]int{}
	pick := func(list []rpResp, i int) (rpResp, bool) {
		switch {
		case i < len(list):
			return list[i], true
		case p.Repeat && len(list) > 0:
			return list[len(list)-1], true
		}
		return rpResp{}, false
	}
	return func(n int, r recReq) fakeResp {
		mu.Lock()
		defer mu.Unlock()
		ref := rpRef(r)
		if list, ok := p.ByRef[ref]; ok {
			i := used[ref]
			used[ref]++
			if x, ok := pick(list, i); ok {
				return x.fake()
			}
			return fakeResp{Text: "ok"}
		}
		if x, ok := pick(p.Seq, n); ok {
			return x.fake()
		}
		return fakeResp{Text: "ok"}
	}
}

// rpBins writes fake binaries that log their argv to a file and exit 0. A
// fake sudo inside the fake sandbox fails the way bwrap makes the real one
// fail (d610 261109-211058): that is the bug the elevated path must avoid.
func rpBins(t *testing.T, names []string) (dir, logPath string) {
	t.Helper()
	dir = t.TempDir()
	logPath = filepath.Join(dir, "calls.log")
	for _, n := range names {
		sh := fmt.Sprintf("#!/bin/sh\necho \"%s $*\" >> %q\n", n, logPath)
		if n == "sudo" {
			sh += "if [ -n \"$JAZZ_FAKE_SANDBOX\" ]; then echo 'sudo: The \"no new privileges\" flag is set, which prevents sudo from running as root.' >&2; exit 1; fi\n"
		}
		sh += "exit 0\n"
		if err := os.WriteFile(filepath.Join(dir, n), []byte(sh), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir, logPath
}

// rpNetBins are programs that reach other machines. Every replay puts a
// stub of each first in PATH: it logs and fails, so no case ever touches the
// network or a real host (a "status do d610" read by the brain would ssh).
var rpNetBins = []string{"ssh", "scp", "sftp", "rsync", "tailscale", "ping", "curl", "wget", "nc", "nmcli", "mosh"}

func rpNetGuard(t *testing.T) (logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "net.log")
	for _, n := range rpNetBins {
		sh := fmt.Sprintf("#!/bin/sh\necho \"%s $*\" >> %q\necho 'replay: rede bloqueada' >&2\nexit 1\n", n, logPath)
		if err := os.WriteFile(filepath.Join(dir, n), []byte(sh), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

// rpSandbox installs a fake bwrap as the active sandbox: it logs its argv,
// marks the environment (JAZZ_FAKE_SANDBOX=1) and runs the wrapped command
// unconfined, so the persistent shell works and only what the real sandbox
// would break (sudo) breaks.
func rpSandbox(t *testing.T, root string) (logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "sandbox.log")
	wrap := filepath.Join(dir, "bwrap")
	sh := fmt.Sprintf("#!/bin/sh\necho \"$*\" >> %q\nwhile [ $# -gt 0 ] && [ \"$1\" != -- ]; do shift; done\nshift\nJAZZ_FAKE_SANDBOX=1 exec \"$@\"\n", logPath)
	if err := os.WriteFile(wrap, []byte(sh), 0o755); err != nil {
		t.Fatal(err)
	}
	sb := &Sandbox{Mode: "auto", Backend: sbBwrap, Bin: wrap, Tmp: t.TempDir(), Home: os.Getenv("HOME"), Roots: []string{root}}
	sbActive.Store(sb)
	t.Cleanup(func() { sbActive.Store(nil) })
	return logPath
}

type rpState struct {
	h        *harness
	err      error
	handled  bool
	sudoArgv [][]string
	binsLog  string
	sbLog    string
	netLog   string
	stdout   func() string // what the terminal UI printed (entry app)
}

func rpRun(t *testing.T, c rpCase) {
	t.Helper()
	r := &rpState{}
	// HOME de mentira: nada do ~ do desenvolvedor (ssh config, ~/.claude) entra no caso
	t.Setenv("HOME", t.TempDir())
	t.Setenv("JAZZ_NO_UPDATE_CHECK", "1")
	for k, v := range c.Env {
		t.Setenv(k, v)
	}
	r.netLog = rpNetGuard(t)
	if c.Entry == "app" {
		r.stdout = uiTestStdout(t)
	}
	if len(c.Stubs.Bins) > 0 {
		_, r.binsLog = rpBins(t, c.Stubs.Bins)
	}
	if c.Stubs.SudoProbe != "" || c.Stubs.SudoRunner {
		oldP, oldR := nlSudoProbe, nlSudoRun
		t.Cleanup(func() { nlSudoProbe, nlSudoRun = oldP, oldR })
		probeOK := c.Stubs.SudoProbe == "ok"
		nlSudoProbe = func(context.Context) error {
			if probeOK {
				return nil
			}
			return errors.New("a password is required")
		}
		// o runner real nunca roda em teste: sudo de verdade na máquina do dev, jamais
		var mu sync.Mutex
		nlSudoRun = func(_ context.Context, argv []string, _ string, _ []string, _ io.Reader, _ time.Duration) (string, int, error) {
			mu.Lock()
			r.sudoArgv = append(r.sudoArgv, append([]string(nil), argv...))
			mu.Unlock()
			return "ok (sudo falso)\n", 0, nil
		}
	}
	h := newHarness(t, c.Provider.handler(), func(cfg *Config) {
		rpConfig(t, c, cfg)
	})
	r.h = h
	if c.Stubs.Sandbox {
		r.sbLog = rpSandbox(t, h.root) // before the first command: the shell starts lazily, inside it
	}
	h.ag.Headless = c.Headless
	h.ag.retryDelay = func(err error, attempt int) time.Duration {
		if RetryDelay(err, attempt) > 0 {
			return time.Millisecond // mesma decisão (esperar ou não), sem o relógio de verdade
		}
		return 0
	}
	switch c.Approve {
	case "allow":
		h.ui.answer = Allow
	case "always":
		h.ui.answer = AllowAlways
	}
	for rel, body := range c.Files {
		p := filepath.Join(h.root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if len(c.Seed.Plans) > 0 {
		var plans []pcPlan
		for _, raw := range c.Seed.Plans {
			var p pcPlan
			if err := json.Unmarshal(raw, &p); err != nil {
				t.Fatalf("seed plan: %v", err)
			}
			plans = append(plans, p)
		}
		if err := pcSave(h.root, plans); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range c.Dead {
		MarkExhausted(d.Ref, time.Now().Add(time.Duration(max(d.Minutes, 1))*time.Minute))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	for _, turn := range c.Turns {
		text := turn
		if c.Entry == "app" {
			app := &App{cfg: h.cfg, ui: NewTermUI(h.cfg, nil), agent: h.ag, tools: h.ag.tools, root: h.root}
			handled, out := app.olBefore(ctx, text)
			if handled {
				r.handled = true
				continue
			}
			text = out
		}
		_, r.err = h.ag.Run(ctx, text)
	}
	rpCheck(t, c, r)
}

// rpConfig shapes the harness config for the case. Every provider the case
// keeps points at the fake server: nothing reaches a real endpoint.
func rpConfig(t *testing.T, c rpCase, cfg *Config) {
	t.Helper()
	base := cfg.Providers["fake"].BaseURL
	if len(c.Presets) > 0 {
		pvTestHome(t) // limpa as chaves de verdade do ambiente do dev
		for k, v := range c.Env {
			t.Setenv(k, v)
		}
		loaded, err := LoadConfig()
		if err != nil {
			t.Fatal(err)
		}
		keep := map[string]*ProviderConf{}
		for _, name := range c.Presets {
			pc := loaded.Providers[name]
			if pc == nil {
				t.Fatalf("preset %q não existe", name)
			}
			cp := *pc
			cp.Type, cp.BaseURL, cp.Local, cp.Ollama = "openai", base+"/p/"+name, false, false
			keep[name] = &cp
		}
		loaded.Providers = keep
		loaded.Mode, loaded.MaxSteps, loaded.ContextWindow, loaded.MaxTokens = cfg.Mode, cfg.MaxSteps, cfg.ContextWindow, cfg.MaxTokens
		*cfg = *loaded
	}
	for _, name := range c.Extra {
		cfg.Providers[name] = &ProviderConf{Type: "openai", BaseURL: base + "/p/" + name, NoKey: true}
	}
	if c.Model != "" {
		cfg.Model = c.Model
	}
	if c.Mode != "" {
		cfg.Mode = c.Mode
	}
	if c.Fallback != nil {
		cfg.Fallback = c.Fallback
	}
	if len(c.Config) > 0 {
		if err := json.Unmarshal(c.Config, cfg); err != nil {
			t.Fatalf("config: %v", err)
		}
	}
}

func rpCheck(t *testing.T, c rpCase, r *rpState) {
	t.Helper()
	e := c.Expect
	h := r.h
	reqs := h.fs.requests()
	var refs []string
	for _, q := range reqs {
		refs = append(refs, rpRef(q))
	}
	ctx := fmt.Sprintf("requests=%v err=%v", refs, r.err)

	if b, _ := os.ReadFile(r.netLog); len(b) > 0 {
		t.Errorf("o replay tentou sair da máquina (rede/host remoto): %q", b)
	}
	switch e.Err {
	case "none":
		if r.err != nil {
			t.Errorf("a run devia terminar sem erro: %v (%s)", r.err, friendlyErr(r.err))
		}
	case "some":
		if r.err == nil {
			t.Errorf("a run devia terminar com erro; %s", ctx)
		}
	}
	if e.Offline != nil && r.err != nil && olIsOffline(r.err) != *e.Offline {
		t.Errorf("olIsOffline(err)=%v, want %v: %v", !*e.Offline, *e.Offline, r.err)
	}
	if e.Handled != nil && r.handled != *e.Handled {
		t.Errorf("olBefore segurou a mensagem=%v, want %v (online: o modelo é dono do turno)", r.handled, *e.Handled)
	}
	if e.Requests != nil && len(reqs) != *e.Requests {
		t.Errorf("requests=%d, want %d; %s", len(reqs), *e.Requests, ctx)
	}
	if len(reqs) < e.MinRequests {
		t.Errorf("requests=%d, want >= %d; %s", len(reqs), e.MinRequests, ctx)
	}
	if e.MaxRequests != nil && len(reqs) > *e.MaxRequests {
		t.Errorf("requests=%d, want <= %d; %s", len(reqs), *e.MaxRequests, ctx)
	}
	for _, ref := range refs {
		if len(e.ModelsOnly) > 0 {
			ok := false
			for _, p := range e.ModelsOnly {
				ok = ok || rpMatch(p, ref)
			}
			if !ok {
				t.Errorf("request a %q, fora de %v (troca escondida); %s", ref, e.ModelsOnly, ctx)
			}
		}
		for _, p := range e.ModelsNever {
			if rpMatch(p, ref) {
				t.Errorf("request a %q não devia acontecer (%s); %s", ref, p, ctx)
			}
		}
	}
	if e.FirstModel != "" && (len(refs) == 0 || !rpMatch(e.FirstModel, refs[0])) {
		t.Errorf("o 1º request devia ir a %q; %s", e.FirstModel, ctx)
	}
	for _, p := range e.ModelsSome {
		found := false
		for _, ref := range refs {
			found = found || rpMatch(p, ref)
		}
		if !found {
			t.Errorf("nenhum request a %q; %s", p, ctx)
		}
	}
	for _, q := range reqs {
		for _, m := range q.Messages {
			body := rpContent(m)
			for _, bad := range e.ReqAbsent {
				if i := strings.Index(body, bad); i >= 0 {
					t.Errorf("request a %s levou %q (role %v): …%s…", rpRef(q), bad, m["role"], oneLine(body[max(0, i-120):min(len(body), i+180)], 400))
				}
			}
		}
	}

	errText := ""
	if r.err != nil {
		errText = friendlyErr(r.err)
	}
	h.ui.mu.Lock()
	term := ""
	if r.stdout != nil {
		term = stripANSI(r.stdout())
	}
	ui := strings.Join([]string{h.ui.text.String(), strings.Join(h.ui.notices, "\n"), strings.Join(h.ui.warns, "\n"), strings.Join(h.ui.errs, "\n"), errText, term}, "\n")
	approvals := len(h.ui.approvals)
	toolEnds := append([]ToolOutput(nil), h.ui.toolEnds...)
	h.ui.mu.Unlock()
	for _, w := range e.UIHas {
		if !strings.Contains(ui, w) {
			t.Errorf("o usuário devia ver %q; viu:\n%s", w, ui)
		}
	}
	if len(e.UIHasAny) > 0 && !rpAny(ui, e.UIHasAny) {
		t.Errorf("o usuário devia ver um de %q; viu:\n%s", e.UIHasAny, ui)
	}
	for _, w := range e.UIAbsent {
		if strings.Contains(ui, w) {
			t.Errorf("o usuário não devia ver %q; viu:\n%s", w, ui)
		}
	}
	for _, w := range e.ErrHas {
		if !strings.Contains(errText, w) {
			t.Errorf("a mensagem de erro devia citar %q: %q", w, errText)
		}
	}
	if len(e.ErrHasAny) > 0 && !rpAny(errText, e.ErrHasAny) {
		t.Errorf("a mensagem de erro devia citar um de %q: %q", e.ErrHasAny, errText)
	}
	for _, w := range e.ErrAbsent {
		if strings.Contains(errText, w) {
			t.Errorf("a mensagem de erro não devia conter %q: %q", w, errText)
		}
	}
	if approvals < e.MinApprovals {
		t.Errorf("aprovações=%d, want >= %d (devia perguntar)", approvals, e.MinApprovals)
	}
	var tools []string
	for _, o := range toolEnds {
		tools = append(tools, o.Text)
	}
	all := strings.Join(tools, "\n---\n")
	for _, w := range e.ToolHas {
		if !strings.Contains(all, w) {
			t.Errorf("nenhum resultado de ferramenta tem %q:\n%s", w, oneLine(all, 2000))
		}
	}
	for _, w := range e.ToolAbsent {
		if strings.Contains(all, w) {
			t.Errorf("resultado de ferramenta com %q:\n%s", w, oneLine(all, 2000))
		}
	}
	if e.FirstToolOK {
		if len(toolEnds) == 0 {
			t.Errorf("nenhuma ferramenta rodou")
		} else if toolEnds[0].IsErr {
			t.Errorf("a 1ª chamada devia rodar de primeira: %s", oneLine(toolEnds[0].Text, 400))
		}
	}
	binsLog := ""
	if r.binsLog != "" {
		b, _ := os.ReadFile(r.binsLog)
		binsLog = string(b)
	}
	for _, b := range e.BinsRan {
		if !rpBinCalled(binsLog, b) {
			t.Errorf("%s devia ter rodado; chamadas: %q", b, binsLog)
		}
	}
	for _, b := range e.BinsNotRan {
		if rpBinCalled(binsLog, b) {
			t.Errorf("%s não devia ter rodado no shell; chamadas: %q", b, binsLog)
		}
	}
	if e.SudoRun != nil && (len(r.sudoArgv) > 0) != *e.SudoRun {
		t.Errorf("runner elevado chamado=%v, want %v (argv %q)", len(r.sudoArgv) > 0, *e.SudoRun, r.sudoArgv)
	}
	if e.Sandboxed != nil && r.sbLog != "" {
		b, _ := os.ReadFile(r.sbLog)
		if got := len(b) > 0; got != *e.Sandboxed {
			t.Errorf("passou pelo sandbox=%v, want %v: %q", got, *e.Sandboxed, b)
		}
	}
}

func rpAny(s string, subs []string) bool {
	for _, w := range subs {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

func rpBinCalled(log, bin string) bool {
	for _, l := range strings.Split(log, "\n") {
		if l == bin || strings.HasPrefix(l, bin+" ") {
			return true
		}
	}
	return false
}

// rpContent flattens a request message (string or multi-part content, plus
// tool call arguments) for the "never sent" checks.
func rpContent(m map[string]any) string {
	var b strings.Builder
	switch c := m["content"].(type) {
	case string:
		b.WriteString(c)
	case []any:
		for _, p := range c {
			if pm, ok := p.(map[string]any); ok {
				if s, ok := pm["text"].(string); ok {
					b.WriteString(s)
					b.WriteByte('\n')
				}
			}
		}
	}
	if tcs, ok := m["tool_calls"].([]any); ok {
		j, _ := json.Marshal(tcs)
		b.Write(j)
	}
	return b.String()
}
