package main

// omni_status.go: `jazz omni status` — one row per provider (key source, tier
// models, last error class, last success, usage vs documented limits, when it
// comes back) and the "pool seco" warning. Owner: platform.

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

// ProvRow is one provider line of the status table.
type ProvRow struct {
	Name     string
	Title    string
	Free     bool
	Local    bool
	HasKey   bool
	KeySrc   string    // env VAR | auth.json | sem chave | local
	Models   [3]string // fast, smart, coder
	Ready    bool      // can take a request now
	State    string    // ok | 429 cota | 429 limite | 401 | sem chave | …
	LastKind string    // quota | daily | rate | auth | …
	LastErr  string
	LastOK   time.Time
	Until    time.Time // when it comes back (zero = unknown / not cooling)
	Usage    string    // "3/20 hoje" (rlBudget)
	Doc      string    // documented free limits: "20/min · 20/dia"
}

// keySource says where a provider's credential comes from (never the key itself).
func (c *Config) keySource(name string) string {
	pc := c.Providers[name]
	if pc == nil {
		return ""
	}
	switch {
	case pc.Local:
		return "local"
	case pc.Type == "codex":
		if c.HasCreds(name) {
			return "login"
		}
		return "—"
	}
	if pc.KeyEnv != "" {
		names := append([]string{pc.KeyEnv}, pvAltKeyEnv[pc.KeyEnv]...)
		for _, n := range names {
			if strings.TrimSpace(os.Getenv(n)) != "" {
				return "env " + n
			}
		}
	}
	if cr := LoadAuth()[name]; cr != nil && cr.Type == "api" && cr.Key != "" {
		return "jazz key"
	}
	if pc.NoKey {
		return "sem chave"
	}
	return "—"
}

// rlProvInfo reads the provider-level bucket (and its free-ids bucket) plus ref's own.
func rlProvInfo(prov, ref string) (kind, lastErr string, lastOK time.Time, until time.Time) {
	rl := omniRL
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.loadLocked()
	var at, ok, u int64
	for _, k := range []string{ref, prov, prov + "/*free"} {
		b := rl.B[k]
		if b == nil {
			continue
		}
		if b.LastAt > at {
			at, kind, lastErr = b.LastAt, b.LastKind, b.LastErr
		}
		ok = max(ok, b.OKAt)
		u = max(u, max(b.DeadUntil, b.CoolUntil))
	}
	if ok > 0 {
		lastOK = time.UnixMilli(ok)
	}
	if u > rlNow().UnixMilli() {
		until = time.UnixMilli(u)
	}
	return
}

func pvDocLimits(fl *FreeLimits) string {
	if fl == nil {
		return "?"
	}
	var p []string
	if fl.RPM > 0 {
		p = append(p, fmt.Sprintf("%d/min", fl.RPM))
	}
	if fl.RPD > 0 {
		p = append(p, fmt.Sprintf("%d/dia", fl.RPD))
	}
	if fl.TPD > 0 {
		p = append(p, fmt.Sprintf("%dk tok/dia", fl.TPD/1000))
	}
	if fl.Monthly > 0 {
		p = append(p, fmt.Sprintf("%d/mês", fl.Monthly))
	}
	if len(p) == 0 {
		return "?"
	}
	return strings.Join(p, " · ")
}

// OmniProviderStatus builds the per-provider rows (free and keyed providers).
func OmniProviderStatus(cfg *Config) []ProvRow {
	pool := cfg.omniPool()
	best := map[string]*[3]struct {
		q int
		m string
	}{}
	for ref, ms := range pool {
		prov, model := SplitModel(ref)
		if pvManualOnly[ref] {
			continue
		}
		for i, t := range []string{"fast", "smart", "coder"} {
			if !pvHas(ms.Tiers, t) {
				continue
			}
			if best[prov] == nil {
				best[prov] = &[3]struct {
					q int
					m string
				}{}
			}
			if ms.Q > best[prov][i].q || best[prov][i].m == "" {
				best[prov][i].q, best[prov][i].m = ms.Q, model
			}
		}
	}
	order := map[string]int{}
	for i, s := range pvSetupOrder {
		order[s] = i + 1
	}
	var rows []ProvRow
	for _, name := range cfg.ProviderNames() {
		pc := cfg.Providers[name]
		if pc == nil || isOmniRef(name+"/x") {
			continue
		}
		has := cfg.HasCreds(name)
		if !pc.Free && !has {
			continue // paid providers only show once they have a key
		}
		if pc.Local && !has {
			continue
		}
		r := ProvRow{Name: name, Title: pc.Title, Free: pc.Free || pc.Local, Local: pc.Local, HasKey: has, KeySrc: cfg.keySource(name)}
		if b := best[name]; b != nil {
			for i := range r.Models {
				r.Models[i] = b[i].m
			}
		}
		ref := name + "/" + pc.DefaultModel
		r.LastKind, r.LastErr, r.LastOK, r.Until = rlProvInfo(name, ref)
		st := cfg.pvState(ref, 0)
		r.Ready = has && st.Ready
		switch {
		case !has:
			r.State = "sem chave"
		case st.Dead && strings.HasPrefix(st.Why, "credencial"):
			r.State, r.LastKind = "401 credencial recusada", "auth"
		case st.Dead && r.LastKind == "quota":
			r.State = "429 cota"
		case st.Dead:
			r.State = "esgotado"
		case !st.Ready:
			r.State = "429 limite"
			if r.Until.IsZero() {
				r.Until = time.Now().Add(st.Wait)
			}
		default:
			r.State = "ok"
			r.Until = time.Time{}
		}
		if st.Dead && r.Until.IsZero() {
			r.Until = st.Until
		}
		r.Usage = cfg.rlBudget(ref)
		r.Doc = pvDocLimits(pc.FreeLimits)
		rows = append(rows, r)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		oi, oj := order[rows[i].Name], order[rows[j].Name]
		if oi == 0 {
			oi = 1000
		}
		if oj == 0 {
			oj = 1000
		}
		return oi < oj
	})
	return rows
}

// PoolDry reports whether keyed free providers exist and every one is cooling or dead,
// and the earliest time one comes back.
func PoolDry(rows []ProvRow) (bool, time.Time) {
	any := false
	var first time.Time
	for _, r := range rows {
		if !r.HasKey || !r.Free {
			continue
		}
		any = true
		if r.Ready {
			return false, time.Time{}
		}
		if !r.Until.IsZero() && (first.IsZero() || r.Until.Before(first)) {
			first = r.Until
		}
	}
	return any, first
}

func pvAgo(t time.Time) string {
	if t.IsZero() {
		return "nunca"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "agora"
	case d < time.Hour:
		return fmt.Sprintf("há %dmin", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("há %dh", int(d.Hours()))
	}
	return fmt.Sprintf("há %dd", int(d.Hours()/24))
}

// FormatProviderStatus renders the per-provider table, the dry-pool warning and the
// shortest path to more capacity.
func FormatProviderStatus(cfg *Config, rows []ProvRow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-13s %-15s %-23s %-14s %-10s %-16s %s\n", "provedor", "chave", "estado", "volta em", "último ok", "uso", "limite (doc)")
	short := func(s string, n int) string {
		if len([]rune(s)) > n {
			return string([]rune(s)[:n-1]) + "…"
		}
		return s
	}
	for _, r := range rows {
		back := ""
		if !r.Until.IsZero() && r.State != "ok" && r.State != "sem chave" {
			back = pvWhen(r.Until)
		}
		fmt.Fprintf(&b, "%-13s %-15s %-23s %-14s %-10s %-16s %s\n", r.Name, short(r.KeySrc, 15), r.State, back, pvAgo(r.LastOK), short(r.Usage, 16), r.Doc)
		if r.HasKey {
			var ms []string
			for i, t := range []string{"fast", "smart", "coder"} {
				if r.Models[i] != "" {
					ms = append(ms, t+" "+short(r.Models[i], 34))
				}
			}
			if len(ms) > 0 {
				fmt.Fprintf(&b, "%13s modelos: %s\n", "", strings.Join(ms, " · "))
			}
			if r.LastErr != "" && r.State != "ok" {
				fmt.Fprintf(&b, "%13s último erro (%s): %s\n", "", r.LastKind, short(r.LastErr, 90))
			}
		}
	}
	if dry, first := PoolDry(rows); dry {
		b.WriteString("\npool SECO: todo provedor grátis com chave está em espera")
		if !first.IsZero() {
			b.WriteString("; o primeiro volta " + pvWhen(first))
		}
		b.WriteString("\n  mais rápido para ganhar fôlego: jazz omni setup  (cole mais chaves grátis; veja docs/FREE-AI.md)\n")
	}
	return b.String()
}

// omniSetupList prints every wizard step without asking anything (`jazz omni setup --list`).
func omniSetupList(cfg *Config, w io.Writer) {
	fmt.Fprintf(w, "%-13s %-7s %-9s %-34s %s\n", "provedor", "chave", "grátis", "cadastro", "o que vem de graça")
	for _, st := range OmniSetupSteps(cfg) {
		key := "não"
		if st.Done {
			key = "sim"
		}
		free := "pago"
		switch {
		case st.NoKey:
			free = "sem chave"
		case st.Free && st.Card:
			free = "cartão"
		case st.Free:
			free = "sim"
		}
		fmt.Fprintf(w, "%-13s %-7s %-9s %-34s %s\n", st.Name, key, free, st.Signup, st.Note)
	}
	for _, msg := range pvRetired {
		fmt.Fprintln(w, "\n  aviso:", msg)
	}
}

// omniSetupAsk walks the free providers still without a key: prints the signup URL
// and offers to paste the key now (saved by cmdKey into the auth store). TTY only.
func omniSetupAsk(cfg *Config, w io.Writer) {
	if !isTTY(os.Stdin) {
		return
	}
	br := bufio.NewReader(os.Stdin)
	for _, st := range OmniSetupSteps(cfg) {
		if st.Done || !st.Free || st.NoKey || st.Card {
			continue
		}
		fmt.Fprintf(w, "\n  %s — crie a chave em %s\n", st.Title, st.Signup)
		if st.Note != "" {
			fmt.Fprintf(w, "    %s\n", st.Note)
		}
		fmt.Fprint(w, "  colar a chave agora? [s/N/q=parar] ")
		line, _ := br.ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "s", "y", "sim":
			cmdKey(cfg, st.Name, nil)
		case "q":
			return
		}
	}
}
