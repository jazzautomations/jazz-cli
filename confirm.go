package main

// confirm.go: the questions jazz asks outside approvals, all on the menu
// widget (menu.go): the default mode and its one-line explanation, the
// Shift+Tab cycle, the folder-trust question, the session and message
// pickers, and the yes/no questions of the subcommands (jazz trust, jazz
// worktree merge, jazz improve). Nothing here asks for a typed word.

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
)

// pfDefaultMode is the interactive mode when config sets none: turbo when an
// OS sandbox isolates what jazz runs (sandbox-exec, bwrap), else auto.
// Headless runs keep auto (nobody sees a mode there).
func pfDefaultMode() string {
	if turboSandboxOK() {
		return "turbo"
	}
	return "auto"
}

func pfHeadlessYoloNeedsAck(mode string, sandbox, headless bool) bool {
	return headless && mode == "yolo" && !sandbox
}

func pfYoloNoSandboxWarning() string {
	return "YOLO sem sandbox do sistema: confirmações rotineiras são dispensadas e os comandos não ficam isolados. As guardas de dados sensíveis continuam ativas. Eles podem alterar qualquer arquivo acessível a este usuário. Continuar?"
}

// pfConfirmYoloWithoutSandbox is called at each interactive transition into
// yolo and at startup when config or --yolo selected it. The safe choice is
// selected by default; without OS isolation, yolo removes routine tool
// confirmations and must be an explicit, informed choice.
func pfConfirmYoloWithoutSandbox() bool {
	if turboSandboxOK() {
		return true
	}
	i, ok := cliMenu(pfYoloNoSandboxWarning(), yesNo("Continuar em yolo", "Manter modo seguro"), 1)
	return ok && i == 0
}

// modeCycleNext is the mode after cur for Shift+Tab: turbo → auto → plan →
// ask → turbo. Without a sandbox turbo is auto's rules, so it is skipped.
// smart and yolo are not in the cycle (/mode smart, --yolo): from them the
// cycle starts over.
func modeCycleNext(cur string, sandbox bool) string {
	order := []string{"turbo", "auto", "plan", "ask"}
	if !sandbox {
		order = order[1:]
	}
	for i, m := range order {
		if m == cur {
			return order[(i+1)%len(order)]
		}
	}
	return order[0]
}

// modeIntroText explains a mode in one line (welcome box, first time).
func modeIntroText(mode string, sandbox bool) string {
	switch mode {
	case "turbo":
		if !sandbox {
			return turboFallbackMsg
		}
		return "sandbox ativo: roda sem perguntar o que fica no projeto; pergunta fora dele, rede nova, segredos, git destrutivo e pacote suspeito"
	case "auto":
		if sandbox {
			return "pergunta antes de comando perigoso, rede nova e fora do projeto · /mode turbo pergunta menos (sandbox ativo)"
		}
		return "pergunta antes de comando perigoso, rede nova e fora do projeto (sem sandbox do sistema: turbo indisponível)"
	case "ask":
		return "pergunta antes de cada mudança (arquivo, comando, rede)"
	case "smart":
		return "um modelo rápido decide o que precisa de confirmação"
	case "yolo":
		return "não pergunta nada: use só em pasta descartável"
	case "plan":
		return "só investiga e planeja; nada muda até você aprovar o plano"
	}
	return ""
}

// modeNote is the welcome's explanation of the start mode: shown once per
// mode (config mode_intro remembers it), then the turbo hint if it applies.
func modeNote(cfg *Config) (note, hint string) {
	m := cfg.EffMode()
	if cfg.ModeIntro != m {
		return modeIntroText(m, turboSandboxOK()), ""
	}
	return "", turboWelcomeHint(m)
}

// modeNoteSeen records that the start mode was explained (best effort).
func modeNoteSeen(cfg *Config) {
	if m := cfg.EffMode(); cfg.ModeIntro != m {
		cfg.ModeIntro = m
		_ = cfg.Save()
	}
}

// pfShortDir shows a path with ~ for the home.
func pfShortDir(p string) string {
	if home, _ := os.UserHomeDir(); home != "" && strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

// pfTrustSpec builds the folder-trust question for the snapshot st.
func pfTrustSpec(t Theme, root, mode string, st pfTrustState) (sp mnSpec, onlyFolder bool) {
	dim, gold := t.fg(cGray, 90), t.fg(cGold, 33)
	onlyFolder = len(st.kinds) == 1 && st.kinds[0] == "folder"
	dir := visibleDetail(pfShortDir(pfTrustRoot(root)))
	if onlyFolder {
		sp.Tag = "pasta nova"
		sp.Title = "Primeira vez nesta pasta. Confiar?"
		sp.Body = []string{dir, "", dim + "Confiando, a sessão roda no modo " + mode + ". Sem confiar, no modo ask (pergunta antes de cada mudança)."}
	} else {
		sp.Tag = "confiança"
		sp.Color = gold
		sp.Title = "Confiar nesta pasta e ativar a configuração dela?"
		sp.Body = []string{dir, "", gold + "Este projeto traz configuração que roda comandos, libera ferramentas ou dá instruções ao modelo:"}
		for _, l := range strings.Split(strings.TrimRight(st.summary, "\n"), "\n") {
			sp.Body = append(sp.Body, l)
		}
		sp.Body = append(sp.Body, "", dim+"Só confie se você conhece este repositório.")
		summary := st.summary
		sp.View = func() string { return summary }
	}
	sp.Items = []mnItem{
		{Label: "Sim, confiar nesta pasta", Hint: "lembrado nas próximas vezes", Key: 's', Keys: "y"},
		{Label: "Só nesta sessão", Key: 'o'},
		{Label: "Não, modo ask", Hint: "pergunta antes de cada mudança", Key: 'n'},
	}
	sp.Keys = t.G().up + t.G().down + " escolhe " + t.G().dot + " enter confirma " + t.G().dot + " esc não"
	return sp, onlyFolder
}

// pfTrustGateUI is the interactive start's trust question (F1): one menu,
// Enter trusts and remembers the folder; "só nesta sessão" trusts until jazz
// exits; "não" (or Esc) keeps the project's config off and the session in
// ask mode. A folder that brings no config is asked in a milder way, or not
// at all with trust_default "always". It returns true when the folder is
// trusted.
func pfTrustGateUI(root string, cfg *Config, ui *TermUI) bool {
	st := pfTrustCollect(root, true)
	if len(st.kinds) == 0 {
		return true
	}
	sp, onlyFolder := pfTrustSpec(ui.T, root, cfg.BaseMode(), st)
	if onlyFolder && strings.EqualFold(cfg.TrustDefault, "always") {
		if err := pfTrustStore(root, st); err == nil {
			return true
		}
	}
	dir := visibleDetail(pfShortDir(pfTrustRoot(root)))
	switch ui.Menu(context.Background(), &sp) {
	case 0:
		if err := pfTrustStore(root, st); err != nil {
			ui.Warn("não consegui salvar a confiança: " + err.Error())
		}
		ui.menuDone(true, "pasta confiável: "+dir)
		return true
	case 1:
		pfTrustSession(root, st)
		ui.menuDone(true, "pasta confiável só nesta sessão: "+dir)
		return true
	}
	ui.menuDone(false, "pasta não confiada: modo ask nesta sessão (/trust confia)")
	return false
}

// ---------- pickers ----------

// pickTurn opens the message picker (/rewind): the last messages, newest
// at the bottom and highlighted. It returns the index in turns or -1.
func (a *App) pickTurn(turns []TurnRef, title string) int {
	items := make([]mnItem, len(turns))
	for i, tr := range turns {
		items[i] = mnItem{Label: oneLine(sanitizeTerm(tr.Text), 90), Find: tr.Text}
	}
	i := a.ui.Menu(context.Background(), &mnSpec{Tag: "mensagens", Title: title, Items: items, Filter: true, Rows: 10, Sel: len(items) - 1})
	if i < 0 || i >= len(turns) {
		a.ui.menuDone(false, "cancelado")
		return -1
	}
	return i
}

// ---------- subcommands (no TUI running) ----------

// cliChoose asks a question with options on a terminal, with the same menu
// as the TUI. Without a terminal it reads one line from in: a digit, the
// option's letter or its first word ("s", "sim", "n", "não"); anything else,
// or an empty line, picks def. It returns the option index or mnCancel.
func cliChoose(in io.Reader, w io.Writer, question string, items []mnItem, def int) int {
	f, ok := in.(*os.File)
	if ok && isTTY(f) && f == os.Stdin && isTTY(os.Stdout) && w == io.Writer(os.Stdout) {
		if i, ok := cliMenu(question, items, def); ok {
			return i
		}
	}
	fmt.Fprint(w, question)
	for i, it := range items {
		fmt.Fprintf(w, "  %d) %s", i+1, it.Label)
	}
	fmt.Fprint(w, " › ")
	ans := strings.ToLower(strings.TrimSpace(pfReadLine(in)))
	fmt.Fprintln(w)
	if ans == "" {
		return def
	}
	r := []rune(ans)
	find := func(k rune) int {
		for i, it := range items {
			if it.Key == k {
				return i
			}
		}
		return mnCancel
	}
	for i, it := range items {
		if ans == fmt.Sprint(i+1) {
			return i
		}
		if len(r) == 1 && (it.Key != 0 && unicode.ToLower(it.Key) == r[0] || strings.ContainsRune(strings.ToLower(it.Keys), r[0])) {
			return i
		}
	}
	switch ans {
	case "sim", "yes":
		return find('s')
	case "não", "nao", "no":
		return find('n')
	}
	return mnCancel
}

// cliMenu runs the menu on the real terminal (cbreak while it is up).
func cliMenu(question string, items []mnItem, def int) (int, bool) {
	restore, err := enterCbreak(int(os.Stdin.Fd()))
	if err != nil {
		return 0, false
	}
	defer restore()
	kr := NewKeyReader()
	defer kr.Pause() // nothing else reads stdin after the question
	t := DetectTheme()
	io := mnTermIO{keys: kr.Keys, write: func(s string) { os.Stdout.WriteString(s) },
		size: func() (int, int) { return termWidth(), termHeight() }}
	i := mnRun(context.Background(), t, io, &mnSpec{Title: question, Items: items, Sel: def})
	col, ans := t.fg(cGreen, 32), "cancelado"
	if i >= 0 && i < len(items) {
		ans = items[i].Label
	} else {
		col = t.fg(cRed, 31)
	}
	fmt.Fprintf(os.Stdout, "  %s%s%s %s %s%s%s\n", col, t.G().tool, t.sgr("0"), stripANSI(question), t.fg(cGray, 90), ans, t.sgr("0"))
	return i, true
}

// yesNo is the two-option list most subcommand questions use.
func yesNo(yes, no string) []mnItem {
	return []mnItem{{Label: yes, Key: 's', Keys: "y"}, {Label: no, Key: 'n'}}
}
