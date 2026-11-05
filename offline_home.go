package main

// offline_home.go — the offline brain as buttons.
//
// "menu" (or "início", "botões", "?") opens a one-key menu of what jazz does
// with no model, grouped by subject; a number opens the group, another runs
// the action. Nobody has to know a command or a phrasing. Each leaf is a
// canonical request handed to the same offline brain a typed request goes
// through (understood with confidence, verified, read-only), so the buttons
// add no new execution path and no new guardrail exception.
//
// Home picks are NOT corrections: their Kind is "home"/"submenu", never
// "brain", so olTakePick records no menu provenance and the forge never sees
// them as labelled examples (a click on a button is not evidence that a
// sentence means an intent).

import (
	"context"
	"strings"
)

type ohGroup struct {
	key, label string
	items      [][2]string // label, canonical request
}

var ohGroups = []ohGroup{
	{"sistema", "Esta máquina (portas, disco, memória, carga)", [][2]string{
		{"portas abertas", "lista as portas abertas"},
		{"espaço em disco", "uso de disco"},
		{"quem come memória", "quem tá comendo memória"},
		{"carga e uptime", "há quanto tempo a máquina tá ligada"},
		{"interfaces e IPs", "quais interfaces de rede tenho"},
	}},
	{"git", "Git (status, histórico, branches, conflitos)", [][2]string{
		{"o que mudou (status)", "git status"},
		{"últimos commits", "últimos commits"},
		{"branches sem merge", "quais branches estão sem merge"},
		{"tem conflito?", "tem conflito de merge?"},
		{"o que mudou (diff)", "git diff"},
	}},
	{"projeto", "Este projeto (resumo, testes, TODOs, arquivos)", [][2]string{
		{"resumo do projeto", "resume o projeto"},
		{"rodar os testes", "roda os testes"},
		{"TODOs e FIXMEs", "lista os TODO do projeto"},
		{"arquivos mudados agora", "quais arquivos mudaram recentemente"},
		{"listar arquivos", "lista os arquivos"},
	}},
	{"qualidade", "Qualidade (funções longas, código duplicado, licença)", [][2]string{
		{"funções longas", "funções grandes do projeto"},
		{"código duplicado", "tem código duplicado?"},
		{"dependências desatualizadas", "dependências desatualizadas"},
		{"licença do projeto", "qual a licença do projeto"},
		{"conferir o .env", "confere o .env com o .env.example"},
	}},
}

// ohIsHome: the request asks for the button menu.
func ohIsHome(text string) bool {
	if strings.TrimSpace(text) == "?" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(strings.Trim(text, "!?. "))) {
	case "menu", "inicio", "início", "home", "botoes", "botões", "opcoes", "opções", "?", "o que voce faz", "o que você faz", "o que da pra fazer", "o que dá pra fazer":
		return true
	}
	return false
}

func ohHome() []olAction {
	var items []olAction
	for _, g := range ohGroups {
		items = append(items, olAction{Key: g.key, Label: g.label, Kind: "submenu", Script: g.key})
	}
	return append(items, olAction{Key: "ajuda", Label: "Tudo o que eu sei fazer sem modelo", Kind: "home", Cmd: "ajuda"})
}

func ohGroupItems(key string) ([]olAction, string) {
	for _, g := range ohGroups {
		if g.key != key {
			continue
		}
		var items []olAction
		for _, it := range g.items {
			items = append(items, olAction{Key: it[0], Label: it[0], Kind: "home", Cmd: it[1]})
		}
		return append(items, olAction{Key: "voltar", Label: "voltar ao menu", Kind: "submenu", Script: ""}), g.label
	}
	return nil, ""
}

// ohShow opens the home menu (key "") or one group.
func (a *App) ohShow(key string) {
	if key == "" {
		a.olOffer("o que você quer fazer? (digite o número)", ohHome())
		return
	}
	if items, title := ohGroupItems(key); len(items) > 0 {
		a.olOffer(title+" — digite o número:", items)
	}
}

// ohRun runs a home leaf through the regular offline brain.
func (a *App) ohRun(ctx context.Context, req string) bool {
	if a.brInteractive(ctx, req) {
		return true
	}
	a.ui.Notice("não consegui fazer isso agora sem modelo: " + req)
	return false
}
