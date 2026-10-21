package main

// security.go: glue for the open-source security round (docs/research/
// security-review-oss.md): the privacy notice for providers that may train on
// prompts (F4), folder trust at startup and /trust (F1).

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// privacyStderr is where headless runs print the privacy line (tests swap it).
var privacyStderr io.Writer = os.Stderr

// privacyOneLine is the headless form of the notice.
const privacyOneLine = "jazz: aviso de privacidade: %s pode registrar seus prompts ou usá-los para treinar modelos (privacy_ack: true na config para silenciar)"

// privacyNotice warns, before the first request to a provider whose free tier
// may train on prompts (ProviderConf.Train), that code and prompts may be
// logged. Interactive: the full notice once ever (config privacy_ack).
// Headless: one stderr line per run until privacy_ack is set. Only active when
// main enabled it (a.PrivacyCheck), so tests never write the user's config.
func (a *Agent) privacyNotice(provider string) {
	if !a.PrivacyCheck || a.sub || a.cfg == nil {
		return
	}
	pc := a.cfg.Providers[provider]
	if pc == nil || !pc.Train || a.privacyRef[provider] {
		return
	}
	if a.privacyRef == nil {
		a.privacyRef = map[string]bool{}
	}
	a.privacyRef[provider] = true
	if a.cfg.PrivacyAck {
		return
	}
	if a.Headless {
		fmt.Fprintf(privacyStderr, privacyOneLine+"\n", provider)
		return
	}
	a.ui.Warn(OmniPrivacyNotice)
	a.cfg.PrivacyAck = true
	a.cfg.Omni.PrivacyAck = true
	if err := a.cfg.Save(); err != nil {
		a.ui.Warn("não consegui salvar privacy_ack: " + err.Error())
	}
}

// Per-turn hard caps (review LLM10, wallet/DoS). Generous defaults; a hostile
// task or a loop that burns the key budget or fills the disk stops with a clear
// message instead of running unbounded. 0 in config = these defaults.
const (
	defTurnCost    = 5.0       // USD
	defTurnTokens  = 4_000_000 // in+out
	defTurnWriteMB = 200       // bytes the write tool may write in one turn
)

// overBudget reports why the current turn must stop, or "". Checked at the top
// of each step, so one more model call cannot push far past the cap.
func (a *Agent) overBudget() string {
	if a.sub {
		return a.saOverCap() // a parallel subagent's share of the parent's cap (subagent.go)
	}
	if a.cfg == nil {
		return ""
	}
	costCap := a.cfg.MaxTurnCost
	if costCap <= 0 {
		costCap = defTurnCost
	}
	tokCap := a.cfg.MaxTurnTokens
	if tokCap <= 0 {
		tokCap = defTurnTokens
	}
	if a.turnUsage.Cost > costCap {
		return fmt.Sprintf("custo do turno US$ %.2f passou do teto US$ %.2f (max_turn_cost).", a.turnUsage.Cost, costCap)
	}
	if n := a.turnUsage.In + a.turnUsage.Out; n > tokCap {
		return fmt.Sprintf("tokens do turno (%d) passaram do teto %d (max_turn_tokens).", n, tokCap)
	}
	if cap := a.turnWriteCap(); a.tools != nil && a.tools.TurnWritten() > cap {
		return fmt.Sprintf("escrita em disco no turno passou de %d MB (max_turn_write_mb).", cap/(1<<20))
	}
	return ""
}

// turnWriteCap is the per-turn write-byte ceiling for the write tool.
func (a *Agent) turnWriteCap() int64 {
	mb := defTurnWriteMB
	if a.cfg != nil && a.cfg.MaxTurnWriteMB > 0 {
		mb = a.cfg.MaxTurnWriteMB
	}
	return int64(mb) << 20
}

// lockDir is where lockfiles are looked for: the shell's cwd, else the root.
func (t *Toolbox) lockDir() string {
	if t.Shell != nil && t.Shell.Cwd != "" {
		return t.Shell.Cwd
	}
	return t.Root
}

// pfUntrustedMode returns the session mode for a folder that is not trusted:
// auto, turbo and smart become ask (F1); an explicit flag (--yolo, --ask,
// --smart, --turbo) or a persisted yolo/ask/plan choice is kept. "" = keep
// the current mode.
func pfUntrustedMode(cfg *Config) string {
	if cfg.ModeOverride != "" {
		return ""
	}
	switch cfg.EffMode() {
	case "auto", "smart", "turbo":
		return "ask"
	}
	return ""
}

// trustCmd implements /trust: grant (default), revoke, or status.
func (a *App) trustCmd(args []string) {
	ui := a.ui
	sub := ""
	if len(args) > 0 {
		sub = strings.ToLower(args[0])
	}
	switch sub {
	case "revoke", "off", "remove", "revogar":
		if err := pfUntrust(a.root); err != nil {
			ui.Error("não consegui remover a confiança: " + err.Error())
			return
		}
		a.untrusted = true
		if m := pfUntrustedMode(a.cfg); m != "" || a.cfg.ModeOverride == "" {
			a.cfg.ModeOverride = "ask"
			a.untrustedAsk = true
		}
		a.agent.Perms = LoadPerms(a.cfg, a.root, a.opts.allowTools)
		a.agent.Hooks = LoadHooks(a.cfg, a.root, false)
		ui.SetMode(a.cfg.EffMode())
		ui.Notice("confiança removida: modo " + a.cfg.EffMode() + "; hooks, regras allow e MCP do projeto desligados (MCP: a partir da próxima sessão)")
	case "status", "?":
		st := pfTrustCollect(a.root, true)
		if len(st.kinds) == 0 {
			ui.Notice("pasta confiável: " + a.root)
			return
		}
		ui.Notice("ainda não confiável (" + strings.Join(st.kinds, ", ") + "):\n" + strings.TrimRight(st.summary, "\n") + "\n/trust confia em tudo isso")
	default:
		st := pfTrustCollect(a.root, true)
		if len(st.kinds) == 0 {
			ui.Notice("pasta já confiável: " + a.root + "  (/trust revoke remove)")
			return
		}
		ui.Notice("confiando em:\n" + strings.TrimRight(st.summary, "\n"))
		if err := pfTrustStore(a.root, st); err != nil {
			ui.Error("não consegui salvar a confiança: " + err.Error())
			return
		}
		a.untrusted = false
		if a.untrustedAsk {
			a.cfg.ModeOverride = ""
			a.untrustedAsk = false
		}
		a.agent.Perms = LoadPerms(a.cfg, a.root, a.opts.allowTools)
		a.agent.Hooks = LoadHooks(a.cfg, a.root, pfProjectPolicyTrusted(a.root))
		ui.SetMode(a.cfg.EffMode())
		ui.Notice("pasta confiável: modo " + a.cfg.EffMode() + "; MCP e steering do projeto valem a partir da próxima sessão")
	}
}
