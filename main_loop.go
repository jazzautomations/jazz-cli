package main

// main_loop.go: the /goal, /until and /review slash commands (agent_loop.go
// holds the agent side).

import (
	"fmt"
	"strings"
)

// pfOffWord reports whether an argument turns a setting off.
func pfOffWord(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off", "clear", "limpar", "desligar", "nenhum", "-":
		return true
	}
	return false
}

// loopCmd handles /goal, /until and /review; arg is the raw text after the command.
func (a *App) loopCmd(cmd, arg string) {
	arg = strings.TrimSpace(arg)
	switch cmd {
	case "/goal":
		switch {
		case arg == "":
			if g := a.agent.Goal(); g != "" {
				a.ui.Notice("objetivo: " + g + "  (/goal off limpa)")
			} else {
				a.ui.Notice("nenhum objetivo definido — /goal <objetivo>")
			}
		case pfOffWord(arg):
			a.agent.SetGoal("")
			a.ui.Notice("objetivo removido")
		default:
			a.agent.SetGoal(arg)
			a.ui.Notice("objetivo definido; ele volta como lembrete a cada turno e depois de compactar")
			a.send(arg)
		}
	case "/until":
		o := a.agent.LoopOpts()
		switch {
		case arg == "":
			if o.Until != "" {
				a.ui.Notice(fmt.Sprintf("--until: %s (até %d %s por turno)  (/until off desliga)", o.Until, pfUntilRounds(o), uiPl(pfUntilRounds(o), "rodada", "rodadas")))
			} else {
				a.ui.Notice("--until desligado — /until <comando> (ex.: /until go test ./...)")
			}
			return
		case pfOffWord(arg):
			o.Until = ""
			a.ui.Notice("--until desligado")
		default:
			o.Until = arg
			a.ui.Notice(fmt.Sprintf("o turno só termina quando `%s` sair com 0 (até %d %s)", arg, pfUntilRounds(o), uiPl(pfUntilRounds(o), "rodada", "rodadas")))
		}
		a.agent.SetLoopOpts(o)
		a.opts.loop = o
	case "/review":
		o := a.agent.LoopOpts()
		switch strings.ToLower(arg) {
		case "":
			o.Review = !o.Review
		case "on", "ligar", "sim":
			o.Review = true
		default:
			if !pfOffWord(arg) && strings.ToLower(arg) != "não" && strings.ToLower(arg) != "nao" {
				a.ui.Error("uso: /review [on|off]")
				return
			}
			o.Review = false
		}
		a.agent.SetLoopOpts(o)
		a.opts.loop = o
		if o.Review {
			a.ui.Notice("revisão ligada: depois de um turno com mudanças, um subagente só-leitura revisa o diff (precisa de git)")
		} else {
			a.ui.Notice("revisão desligada")
		}
	}
}

func pfUntilRounds(o LoopOpts) int {
	if o.UntilRounds > 0 {
		return o.UntilRounds
	}
	return acUntilDefaultRounds
}
