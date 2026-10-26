package main

// omni_cli.go: user-facing side of omni routing (`/omni`, `jazz omni setup`). Owner: platform.
// The routing itself (Config.Candidates, free-first chain, exhausted tracking)
// belongs to providers (omni.go); this file only reads and toggles cfg.Omni.

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// pfOmniStatus describes omni routing and the candidate chain for model.
func pfOmniStatus(cfg *Config, model string) string {
	var b strings.Builder
	state := "desligado"
	if cfg.Omni.Enabled {
		state = "ligado"
	}
	strat := cfg.Omni.Strategy
	if strat == "" {
		strat = "free-first"
	}
	fmt.Fprintf(&b, "omni: %s · estratégia %s", state, strat)
	if len(cfg.Omni.Exclude) > 0 {
		fmt.Fprintf(&b, " · exclui %s", strings.Join(cfg.Omni.Exclude, ", "))
	}
	b.WriteString("\n")
	var ready []string
	for _, p := range cfg.ProviderNames() {
		if cfg.HasCreds(p) {
			ready = append(ready, p)
		}
	}
	fmt.Fprintf(&b, "provedores com credencial: %s\n", pfJoinOr(ready, "nenhum"))
	if cfg.Omni.Enabled || isOmniRef(model) {
		// per-provider state and quota from the limiter (providers' omni.go)
		b.WriteString(FormatOmniStatus(OmniStatus(cfg)))
	}
	if rows := OmniProviderStatus(cfg); len(rows) > 0 {
		b.WriteString(FormatProviderStatus(cfg, rows))
	}
	b.WriteString("ordem de tentativa agora:\n")
	for i, c := range cfg.Candidates(model) {
		mark := ""
		if until, ok := ExhaustedUntil(c); ok && time.Now().Before(until) {
			mark = " (esgotado até " + until.Format("15:04") + ")"
		}
		fmt.Fprintf(&b, "  %d. %s%s\n", i+1, c, mark)
	}
	return b.String()
}

func pfJoinOr(l []string, empty string) string {
	if len(l) == 0 {
		return empty
	}
	return strings.Join(l, ", ")
}

// pfOmniCommand applies `/omni [on|off|status|strategy X|exclude a,b]` and
// returns the text to show. Changes are saved to config.json.
func pfOmniCommand(cfg *Config, model string, args []string) (string, error) {
	if len(args) == 0 || args[0] == "status" {
		return pfOmniStatus(cfg, model), nil
	}
	switch args[0] {
	case "on", "ligar":
		cfg.Omni.Enabled = true
	case "off", "desligar":
		cfg.Omni.Enabled = false
	case "strategy", "estrategia", "estratégia":
		if len(args) < 2 {
			return "", fmt.Errorf("uso: /omni strategy free-first|weighted|ordered|cheapest")
		}
		switch args[1] {
		case "free-first", "weighted", "ordered", "cheapest":
			cfg.Omni.Strategy = args[1]
		default:
			return "", fmt.Errorf("estratégias: free-first · weighted · ordered · cheapest")
		}
	case "reset":
		prov := ""
		if len(args) > 1 {
			prov = args[1]
		}
		ResetRateLimits(prov)
		return "contadores de limite zerados" + map[bool]string{true: " (todos)", false: " de " + prov}[prov == ""] + "\n" + pfOmniStatus(cfg, model), nil
	case "exclude", "excluir":
		cfg.Omni.Exclude = nil
		if len(args) > 1 {
			for _, x := range strings.Split(strings.Join(args[1:], ","), ",") {
				if x = strings.TrimSpace(x); x != "" {
					cfg.Omni.Exclude = append(cfg.Omni.Exclude, x)
				}
			}
		}
	default:
		return "", fmt.Errorf("uso: /omni [on|off|status|strategy X|exclude a,b|reset [provedor]]")
	}
	if err := cfg.Save(); err != nil {
		return "", err
	}
	return pfOmniStatus(cfg, model), nil
}

// cmdOmniSetup implements `jazz omni setup`: shows what omni would use,
// turns it on (free-first) and prints the next steps for missing keys.
func cmdOmniSetup(cfg *Config, w io.Writer) {
	fmt.Fprintln(w, "\n  omni: usa todos os provedores que você tem, grátis primeiro, e troca sozinho quando um esgota.")
	var have []string
	for _, p := range cfg.ProviderNames() {
		ok := cfg.HasCreds(p)
		mark := "  ✗"
		if ok {
			mark = "  ✓"
			have = append(have, p)
		}
		fmt.Fprintf(w, "%s %-11s %s\n", mark, p, cfg.Providers[p].DefaultModel)
	}
	pfOmniSetupNext(cfg, w)
	omniSetupAsk(cfg, w)
	have = have[:0]
	for _, p := range cfg.ProviderNames() {
		if cfg.HasCreds(p) {
			have = append(have, p)
		}
	}
	if len(have) == 0 {
		fmt.Fprintln(w, "  depois rode jazz omni setup de novo.")
		return
	}
	if !cfg.Omni.PrivacyAck {
		fmt.Fprintln(w, "\n  "+OmniPrivacyNotice)
		cfg.Omni.PrivacyAck = true
	}
	cfg.Omni.Enabled = true
	if cfg.Omni.Strategy == "" {
		cfg.Omni.Strategy = "free-first"
	}
	if err := cfg.Save(); err != nil {
		fatal("não salvei a config: " + err.Error())
	}
	model := cfg.Model
	if model == "" {
		model = cfg.PickDefaultModel()
	}
	fmt.Fprintln(w)
	fmt.Fprint(w, pfIndentLines(pfOmniStatus(cfg, model), "  "))
	fmt.Fprintln(w, "\n  ✓ omni ligado. Mais provedores = mais fôlego: jazz key <provedor> · jazz provider add (local)")
}

func pfIndentLines(s, p string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = p + l
	}
	return strings.Join(lines, "\n") + "\n"
}

// pfOmniSetupNext lists the free providers still without a key, in the order
// worth setting them up (providers' OmniSetupSteps), with where to get the key.
func pfOmniSetupNext(cfg *Config, w io.Writer) {
	var todo []OmniStep
	for _, st := range OmniSetupSteps(cfg) {
		if !st.Done && st.Free && !st.NoKey {
			todo = append(todo, st)
		}
	}
	if len(todo) == 0 {
		return
	}
	fmt.Fprintln(w, "\n  chaves grátis que ainda faltam (mais chaves = mais fôlego):")
	for i, st := range todo {
		if i == 6 {
			fmt.Fprintf(w, "    … e mais %d (jazz omni status)\n", len(todo)-i)
			break
		}
		fmt.Fprintf(w, "    jazz key %-12s %s\n", st.Name, st.Signup)
		if st.Note != "" {
			fmt.Fprintf(w, "      %s\n", st.Note)
		}
		if st.Account && st.AccountEnv != "" {
			fmt.Fprintf(w, "      também precisa de %s\n", st.AccountEnv)
		}
	}
}
