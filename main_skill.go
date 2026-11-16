package main

// main_skill.go: the /skill slash command (skills.go holds the agent side).

import (
	"context"
	"path/filepath"
	"strings"
)

// skillCmd handles /skill: no argument lists the built-in skills; a name
// queues that skill for the next turn, and any text after the name is sent as
// that turn's request.
func (a *App) skillCmd(arg string) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		var b strings.Builder
		b.WriteString("skills embutidas (modo " + a.agent.SkillMode() + "; o jazz escolhe no máximo uma por turno):\n")
		for _, l := range SkillList() {
			b.WriteString("  " + l + "\n")
		}
		b.WriteString("use /skill <nome> [pedido] pra forçar uma; config \"skills\": auto · manual · off")
		a.ui.Notice(b.String())
		return
	}
	name, rest, _ := strings.Cut(arg, " ")
	switch name {
	case "install", "add", "list", "ls", "rm", "remove", "update":
		a.skillManage(name, strings.TrimSpace(rest))
		return
	}
	if err := a.agent.ForceSkill(name); err != nil {
		a.ui.Error(err.Error())
		return
	}
	if rest = strings.TrimSpace(rest); rest != "" {
		a.send(rest)
		return
	}
	a.ui.Notice("skill " + strings.ToLower(name) + " entra no próximo turno")
}

// skillManage: /skill install|list|rm|update inside the session.
func (a *App) skillManage(verb, rest string) {
	args := strings.Fields(rest)
	if verb != "install" && verb != "add" {
		var b strings.Builder
		if err := cmdSkill(a.root, append([]string{verb}, args...), strings.NewReader(""), &b); err != nil {
			a.ui.Error(err.Error())
			return
		}
		a.ui.Notice(strings.TrimRight(b.String(), "\n"))
		return
	}
	a.skillInstallInteractive(strings.Join(args, " "))
}

// skillInstallInteractive installs with a yes/no on the summary and tells the
// model where the new skill lives (the system prompt is built once).
func (a *App) skillInstallInteractive(arg string) bool {
	project := false
	var src []string
	for _, f := range strings.Fields(arg) {
		if f == "--project" || f == "-p" {
			project = true
			continue
		}
		src = append(src, f)
	}
	if len(src) != 1 {
		a.ui.Notice("uso: /skill install <url do GitHub | dono/repo | pasta> [--project]")
		return false
	}
	a.ui.Thinking("baixando a skill")
	ctx := siWithProgress(context.Background(), func(m string) { a.ui.Thinking(m) })
	found, err := siInstall(ctx, src[0], siDest(a.root, project), false, func(sum string) bool {
		a.ui.StopThinking()
		a.ui.Notice(sum)
		return a.ui.Choose("Instalar?", yesNo("Sim, instalar", "Não"), 0) == 0
	})
	a.ui.StopThinking()
	if err != nil {
		a.ui.Warn("skill não instalada: " + err.Error())
		return false
	}
	var names []string
	for _, f := range found {
		names = append(names, f.Name)
	}
	a.agent.Note(siNote(found, true))
	a.ui.Notice("skill instalada: " + strings.Join(names, ", "))
	return true
}

// skillOffer: the message cites a skill by URL. Already installed: the model
// is pointed at it; not installed: the summary and one yes/no, then install.
func (a *App) skillOffer(text string) {
	url := siChatURL(text)
	if url == "" {
		return
	}
	if d := siInstalledFrom(a.root, url); d != "" {
		name, desc := readFrontmatter(filepath.Join(d, "SKILL.md"))
		a.agent.Note(siNote([]siFound{{Name: name, Desc: desc, Dir: d}}, false))
		return
	}
	a.ui.Notice("a mensagem cita a skill " + url + " — vou mostrar o que ela traz antes de instalar")
	a.skillInstallInteractive(url)
}
