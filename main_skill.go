package main

// main_skill.go: the /skill slash command (skills.go holds the agent side).

import (
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
