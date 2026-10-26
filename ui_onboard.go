package main

// ui_onboard.go: the first run. `jazz` with no config.json and no key at all
// shows three steps before anything else: (1) how to run (free pool,
// Anthropic key, OpenAI key, ChatGPT login, local Ollama), (2) the key with
// hidden input (stored like `jazz key`), (3) one tiny real request with the
// result on screen (the same check as `jazz doctor --provider`). `jazz setup`
// opens it again at any time. Without a terminal (jazz -p, pipes) nothing
// here runs: headless never prompts.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// onbForce is set by `jazz setup`: the onboarding opens even when configured.
var onbForce bool

// onbChoice is one way to run, the first step's options.
type onbChoice struct {
	label, hint string
	prov        string // provider to configure ("" = the free pool)
	how         string // pool | key | login | local
}

var onbChoices = []onbChoice{
	{"Grátis (pool de modelos grátis)", "sem chave, começa já; a qualidade varia", "kilo", "pool"},
	{"Chave da Anthropic (Claude)", "pago; a chave começa com sk-ant-", "anthropic", "key"},
	{"Chave da OpenAI", "pago; a chave começa com sk-", "openai", "key"},
	{"Login do ChatGPT (Plus/Pro)", "usa o seu plano, sem chave", "chatgpt", "login"},
	{"Ollama local (no seu computador)", "sem internet nem chave; precisa do ollama rodando", "ollama", "local"},
}

// onbOff: JAZZ_ONBOARD=off (tests, scripted installs) never opens it.
func onbOff() bool { return olOffWord(os.Getenv("JAZZ_ONBOARD")) }

// onbFirstRun: no config.json yet and no credential anywhere (env or
// auth.json, API key or login). A local server alone does not count: the
// onboarding offers it.
func onbFirstRun(cfg *Config) bool {
	if onbOff() {
		return false
	}
	if _, err := os.Stat(filepath.Join(configDir(), "config.json")); err == nil {
		return false
	}
	if cfg.Model != "" || cfg.Omni.Enabled {
		return false
	}
	for name, pc := range cfg.Providers {
		if pc.NoKey || pc.Local {
			continue
		}
		if pc.Type == "codex" {
			if cfg.HasCreds(name) {
				return false
			}
			continue
		}
		if cfg.APIKey(name) != "" {
			return false
		}
	}
	return true
}

// readSecret reads a line of keys showing only bullets and the length (a
// pasted 100-character key never wraps the line). ok=false on Esc/Ctrl+C.
func (u *TermUI) readSecret(label string) (string, bool) {
	if u.keys == nil {
		return "", false
	}
	t := u.T
	var buf []rune
	draw := func() {
		dots := strings.Repeat("•", min(len(buf), 12))
		if t.ascii {
			dots = strings.Repeat("*", min(len(buf), 12))
		}
		n := ""
		if len(buf) > 0 {
			n = t.fg(cGray, 90) + fmt.Sprintf(" (%d caracteres)", len(buf)) + t.sgr("0")
		}
		u.print("\r\x1b[K" + clipVisible(label+dots+n, termWidth()-1)) // one row: \r redraws it
	}
	draw()
	for k := range u.keys.Keys {
		switch k.Code {
		case KEnter:
			u.print("\n")
			return string(buf), true
		case KEsc, KCtrlC, KCtrlD:
			u.print("\n")
			return "", false
		case KBackspace:
			if len(buf) > 0 {
				buf = buf[:len(buf)-1]
			}
		case KCtrlU:
			buf = nil
		case KRune:
			buf = append(buf, k.R)
		case KPaste:
			buf = append(buf, []rune(strings.TrimSpace(k.Text))...)
		default:
			continue
		}
		draw()
	}
	u.print("\n")
	return "", false
}

// onbStep is the tag of a step's box.
func onbStep(n int, what string) string { return fmt.Sprintf("%d/3 · %s", n, what) }

// runOnboarding shows the three steps. It returns the model to use ("" =
// keep ref) and saves config.json either way, so it does not open again by
// itself. cur is the model the session would start with.
func runOnboarding(cfg *Config, ui *TermUI, cur string) string {
	t, g := ui.T, ui.T.G()
	ed := NewEditor(ui)
	save := func() {
		if err := cfg.Save(); err != nil {
			ui.Warn("não salvei a config: " + err.Error())
		}
	}
	ui.print("\n  " + t.fg(cAccent, 36) + t.sgr("1") + g.star + " bem-vindo ao jazz" + t.sgr("0") + t.fg(cGray, 90) + "  três passos e você está programando (jazz setup refaz)" + t.sgr("0") + "\n")
	for {
		items := make([]mnItem, len(onbChoices))
		for i, c := range onbChoices {
			items[i] = mnItem{Label: c.label, Hint: c.hint}
		}
		i := ui.Menu(nil, &mnSpec{Tag: onbStep(1, "como rodar"), Title: "Como você quer rodar o jazz?", Items: items,
			Keys: g.up + g.down + " ou nº escolhe " + g.dot + " enter confirma " + g.dot + " esc pula (fica no grátis)"})
		if i < 0 || i >= len(onbChoices) {
			ui.menuDone(false, "configuração pulada: usando o grátis "+g.dot+" jazz setup abre de novo")
			save()
			return ""
		}
		c := onbChoices[i]
		ui.menuDone(true, onbStep(1, "")+c.label)
		ref, ok := onbConfigure(cfg, ui, ed, c)
		if !ok {
			continue // back to step 1
		}
		ui.print("  " + t.fg(cGray, 90) + onbStep(3, "testando "+ref+g.ell) + t.sgr("0") + "\n")
		var b strings.Builder
		code := pfDoctorProvider(cfg, ref, &b)
		for _, l := range strings.Split(strings.TrimRight(b.String(), "\n"), "\n") {
			if l = strings.TrimRight(l, " "); strings.TrimSpace(l) == "" {
				continue
			}
			col, line := t.fg(cGray, 90), mnClip(sanitizeTerm(l), termWidth()-1)
			switch {
			case strings.Contains(l, "✓"):
				col, line = t.fg(cGreen, 32), uiWrapMsg(sanitizeTerm(l), termWidth()-1)
			case strings.Contains(l, "✗") || strings.Contains(l, "→"):
				col, line = t.fg(cRed, 31), uiWrapMsg(sanitizeTerm(l), termWidth()-1)
			}
			ui.print(col + line + t.sgr("0") + "\n")
		}
		if code == 0 {
			cfg.Model = ref
			save()
			ui.Notice("pronto: modelo " + ref + " (salvo como padrão) " + g.dot + " /model troca quando quiser")
			return ref
		}
		j := ui.Choose("O teste falhou. E agora?", []mnItem{{Label: "Escolher outra opção", Key: 'o'}, {Label: "Seguir assim mesmo", Key: 's'}}, 0)
		if j == 1 {
			cfg.Model = ref
			save()
			return ref
		}
	}
}

// onbConfigure is step 2 for one choice: the key, the login or the local
// model. It returns the model to test.
func onbConfigure(cfg *Config, ui *TermUI, ed *Editor, c onbChoice) (string, bool) {
	t, g := ui.T, ui.T.G()
	pc := cfg.Providers[c.prov]
	if pc == nil {
		ui.Error("provedor " + c.prov + " não existe nesta versão")
		return "", false
	}
	def := c.prov + "/" + pc.DefaultModel
	switch c.how {
	case "pool":
		ui.print("  " + t.fg(cGray, 90) + onbStep(2, "nada a colar: o pool grátis não usa chave") + t.sgr("0") + "\n")
		return def, true
	case "key":
		if pc.Signup != "" {
			ui.print("  " + t.fg(cGray, 90) + "crie a chave em " + pc.Signup + t.sgr("0") + "\n")
		}
		key, ok := ui.readSecret("  " + onbStep(2, "cole a chave de "+c.prov+" (fica escondida) ") + g.prompt + " ")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			ui.Notice("sem chave: voltando ao passo 1")
			return "", false
		}
		warns, err := storeKey(cfg, c.prov, key)
		for _, w := range warns {
			ui.Warn(w)
		}
		if err != nil {
			ui.Error(err.Error())
			return "", false
		}
		ui.Notice(fmt.Sprintf("chave de %s salva (%s) em %s", c.prov, pfMaskKey(key), authPath()))
		return def, true
	case "login":
		ui.print("  " + t.fg(cGray, 90) + onbStep(2, "login no ChatGPT (esc cancela)") + t.sgr("0") + "\n")
		ctx, cancel := context.WithCancel(context.Background())
		stop := ui.WatchTurn(cancel, nil)
		err := LoginChatGPTDevice(ctx, func(s string) { ui.print(s) })
		stop()
		cancel()
		if err != nil {
			ui.Error("login falhou: " + err.Error())
			return "", false
		}
		refreshSecrets()
		return def, true
	case "local":
		ui.print("  " + t.fg(cGray, 90) + onbStep(2, "procurando o ollama em "+pc.BaseURL+g.ell) + t.sgr("0") + "\n")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		ms, err := ListModels(ctx, cfg, c.prov)
		cancel()
		if err != nil || len(ms) == 0 {
			why := "nenhum modelo baixado (rode: ollama pull qwen3-coder)"
			if err != nil {
				why = "não respondeu: instale em https://ollama.com e rode ollama serve"
			}
			ui.Warn("ollama " + why)
			return "", false
		}
		var items []pkItem
		for _, m := range ms {
			ref := c.prov + "/" + m.ID
			items = append(items, pkItem{Title: ref, Right: mpTags(cfg, ref, nil), Val: ref})
		}
		sp := &pkSpec{Head: onbStep(2, "qual modelo local?"), Hint: pkHint, Items: items}
		i := ed.Pick(sp)
		if i < 0 {
			return "", false
		}
		return sp.Items[i].Val.(string), true
	}
	return "", false
}
