package main

// ui_models.go: the model picker. `/model` with no argument (and `/omni`
// role assignment, and the onboarding's local step) opens one list, grouped
// by provider: ↑↓ or the row number (1–9 picks at once; with ten or more rows
// a second digit names rows 10–99), letters filter, Enter picks, Esc
// cancels. Each row says free or paid, the measured latency (scores.go) and
// "sem chave" when the provider has no credential yet; picking such a row
// asks for the key right there (hidden input, cmdKey) instead of sending
// the user off to /key and back. Nothing here touches the network except
// the short reachability probe of local servers (cached, run in parallel).

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// mpRef is what a picker row returns.
type mpRef struct {
	Ref   string
	Prov  string
	Ready bool // the provider has a credential (or needs none and answers)
}

// mpMaxPerProvider caps the cached models listed per provider (the filter
// still finds the default and the current model).
const mpMaxPerProvider = 40

// mpProvState is one provider's row group.
type mpProvState struct {
	name  string
	ready bool
	local bool
	free  bool
	codex bool
}

// mpProviders reports every configured provider's state, probing local
// servers in parallel (each probe is cached by pvReachable).
func mpProviders(cfg *Config) []mpProvState {
	names := cfg.ProviderNames()
	out := make([]mpProvState, len(names))
	var wg sync.WaitGroup
	for i, n := range names {
		pc := cfg.Providers[n]
		out[i] = mpProvState{name: n, local: pc.Local, free: pc.Free || pc.Local || n == "kilo", codex: pc.Type == "codex"}
		wg.Add(1)
		safeGo(func() {
			defer wg.Done()
			out[i].ready = cfg.HasCreds(n)
		})
	}
	wg.Wait()
	return out
}

// mpLatency is the measured average of a successful request ("" = none yet).
func mpLatency(ref string) string {
	ms := bgScoreOf(ref).MS
	switch {
	case ms <= 0:
		return ""
	case ms < 1000:
		return fmt.Sprintf("%.0f ms", ms)
	}
	return fmt.Sprintf("%.1f s", ms/1000)
}

// mpTags is the dim text after a model: price class, context, latency.
func mpTags(cfg *Config, ref string, cached *pvCachedModel) string {
	var tags []string
	switch {
	case cfg.pvIsFreeish(ref) || cached != nil && cached.Free:
		tags = append(tags, "grátis")
	case cfg.isSubRef(ref):
		tags = append(tags, "assinatura")
	default:
		if in, out, ok := cfg.Price(ref); ok && in+out > 0 {
			tags = append(tags, fmt.Sprintf("pago $%.2f/M", (in+out)*1e6/2))
		} else {
			tags = append(tags, "pago")
		}
	}
	if cached != nil && cached.Ctx > 0 {
		tags = append(tags, fmtK(cached.Ctx))
	}
	if l := mpLatency(ref); l != "" {
		tags = append(tags, l)
	}
	return strings.Join(tags, " · ")
}

// mpItems builds the picker rows: omni first (when it has candidates), the
// providers with a credential, local servers, then the ones without a key
// (only their default model: picking it asks for the key). current is marked.
func mpItems(cfg *Config, current string) []pkItem {
	provs := mpProviders(cfg)
	curProv, _ := SplitModel(current)
	rank := func(p mpProvState) int {
		switch {
		case p.name == curProv:
			return 0
		case p.ready && !p.local:
			return 1
		case p.ready:
			return 2
		case p.local:
			return 3
		}
		return 4
	}
	sort.SliceStable(provs, func(i, j int) bool { return rank(provs[i]) < rank(provs[j]) })
	var items []pkItem
	add := func(group, ref, tags string, st mpProvState) {
		it := pkItem{Group: group, Title: ref, Right: tags, Key: group, Val: mpRef{Ref: ref, Prov: st.name, Ready: st.ready}}
		if ref == current {
			it.Mark = "atual"
		}
		items = append(items, it)
	}
	omniDone := false
	addOmni := func() {
		omniDone = true
		if len(cfg.CandidatesFor(Need{}, OmniProvider+"/free")) == 0 {
			return
		}
		st := mpProvState{name: OmniProvider, free: true, ready: true}
		g := "omni · todos os seus provedores, grátis primeiro"
		for _, tier := range omniTiers {
			add(g, OmniProvider+"/"+tier, "roteado", st)
		}
	}
	if curProv == OmniProvider {
		addOmni()
	}
	for k, p := range provs {
		if k > 0 && !omniDone { // omni right after the current provider
			addOmni()
		}
		pc := cfg.Providers[p.name]
		group := p.name
		switch {
		case p.name == "kilo" && p.ready:
			group += " · grátis (pool, sem chave)"
		case p.ready && p.local:
			group += " · local"
		case p.ready:
		case p.local:
			group += " · local, não respondeu"
		case p.codex:
			group += " · sem login (enter faz o login)"
		default:
			group += " · sem chave (enter pede a chave)"
		}
		if p.free && !p.local && p.name != "kilo" {
			group += " · cota grátis"
		}
		var refs []string
		seen := map[string]bool{}
		push := func(ref string) {
			if ref != "" && !seen[ref] && !strings.HasSuffix(ref, "/") {
				seen[ref] = true
				refs = append(refs, ref)
			}
		}
		if pc.DefaultModel != "" {
			push(p.name + "/" + pc.DefaultModel)
		}
		cached := pvCachedRefs(p.name)
		if p.ready {
			var more []string
			if p.name == curProv {
				more = append(more, current)
			}
			for ref, m := range cached {
				if m.Tools != "none" {
					more = append(more, ref)
				}
			}
			for ref := range cfg.Models {
				if pr, _ := SplitModel(ref); pr == p.name {
					more = append(more, ref)
				}
			}
			sort.SliceStable(more, func(i, j int) bool {
				fi, fj := cached[more[i]].Free, cached[more[j]].Free
				if fi != fj {
					return fi
				}
				return more[i] < more[j]
			})
			for _, ref := range more {
				if len(refs) >= mpMaxPerProvider && ref != current {
					break
				}
				push(ref)
			}
		}
		for _, ref := range refs {
			var cm *pvCachedModel
			if m, ok := cached[ref]; ok {
				cm = &m
			}
			add(group, ref, mpTags(cfg, ref, cm), p)
		}
	}
	return items
}

// mpPick opens the picker; head is the line above the list. It returns the
// chosen row (ok=false when cancelled). auto adds a first row "auto".
func mpPick(ed *Editor, cfg *Config, head, current string, auto string) (mpRef, bool) {
	items := mpItems(cfg, current)
	if auto != "" {
		it := pkItem{Group: "automático", Title: "auto", Right: auto, Val: mpRef{Ref: "auto", Ready: true}}
		if current == "" || current == "auto" {
			it.Mark = "atual"
		}
		items = append([]pkItem{it}, items...)
	}
	sel := 0
	for i, it := range items {
		if it.Mark == "atual" {
			sel = i
		}
	}
	sp := &pkSpec{Head: head, Hint: pkHint, Items: items, Sel: sel}
	i := ed.Pick(sp)
	if i < 0 {
		return mpRef{}, false
	}
	return sp.Items[i].Val.(mpRef), true
}

// mpEnsureReady makes the chosen provider usable: asks for the key (hidden)
// or runs the ChatGPT login, right there. It returns false when the user
// gave up or the local server does not answer.
func (a *App) mpEnsureReady(r mpRef) bool {
	if r.Ready || r.Ref == "auto" {
		return true
	}
	pc := a.cfg.Providers[r.Prov]
	if pc == nil {
		return false
	}
	switch {
	case pc.Local:
		a.ui.Warn(r.Prov + " não respondeu em " + pc.BaseURL + " — inicie o servidor (ex.: ollama serve) e escolha de novo")
		return false
	case pc.Type == "codex":
		a.ui.Notice("login no ChatGPT: siga o código abaixo (esc cancela)")
		a.command("/login")
		return a.cfg.HasCreds(r.Prov)
	}
	if pc.Signup != "" {
		a.ui.Notice(r.Prov + " precisa de uma chave: crie em " + pc.Signup)
	}
	cmdKey(a.cfg, r.Prov, a)
	return a.cfg.HasCreds(r.Prov)
}

// modelPick is /model with no argument on a terminal.
func (a *App) modelPick() {
	cur := a.agent.Model
	r, ok := mpPick(a.ed, a.cfg, "modelo atual: "+cur+" · escolha outro (↑↓, nº ou digite pra filtrar)", cur, "")
	if !ok {
		a.ui.Notice("modelo atual: " + cur + " (nada mudou)")
		return
	}
	if r.Ref == cur {
		a.ui.Notice("modelo atual: " + cur)
		return
	}
	if !a.mpEnsureReady(r) {
		a.ui.Notice("modelo atual: " + a.agent.Model + " (nada mudou)")
		return
	}
	a.setModel(r.Ref)
}

// setModel switches the session model and saves it as the default.
func (a *App) setModel(ref string) {
	ref, err := a.cfg.ResolveModel(ref)
	if err != nil {
		a.ui.Error(err.Error())
		return
	}
	a.agent.SetModel(ref)
	a.cfg.Model = ref
	safeGo(func() { // per-model window lands in the models cache (providers), never in the global config (bug 42)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, _ = DetectWindow(ctx, a.cfg, ref)
	})
	if err := a.cfg.Save(); err != nil {
		a.ui.Warn("não salvei como padrão: " + err.Error())
	}
	a.ui.Notice("modelo: " + ref + " (salvo como padrão)")
	if !a.cfg.HasCreds(mustProvider(ref)) {
		a.ui.Warn("sem credencial pra " + mustProvider(ref) + " — /key " + mustProvider(ref))
	}
}

// ---------- /omni: roles ----------

// omniRole is one assignable role (config "roles").
type omniRole struct {
	key, label, help string
	get              func(*RolesConf) *string
}

var omniRoles = []omniRole{
	{"planner", "Planejador", "pensa o plano antes de editar", func(r *RolesConf) *string { return &r.Planner }},
	{"editor", "Editor", "escreve as mudanças", func(r *RolesConf) *string { return &r.Editor }},
	{"verifier", "Verificador", "confere o resultado no fim", func(r *RolesConf) *string { return &r.Verifier }},
}

func omniRoleVal(v string) string {
	if p := ojPinned(v); p != "" {
		return p
	}
	return "auto"
}

// omniMenu is /omni with no argument on a terminal: on/off and a model per
// role in one menu; a role opens the model picker.
func (a *App) omniMenu() {
	for {
		state, toggle := "desligado", "Ligar o omni (grátis primeiro, troca sozinho quando um esgota)"
		if a.cfg.Omni.Enabled {
			state, toggle = "ligado", "Desligar o omni"
		}
		items := []mnItem{{Label: toggle, Key: 'o'}}
		for _, r := range omniRoles {
			items = append(items, mnItem{Label: r.label + ": " + omniRoleVal(*r.get(&a.cfg.Roles)), Hint: r.help})
		}
		items = append(items, mnItem{Label: "Ver o estado completo", Key: 'v'})
		i := a.ui.Menu(nil, &mnSpec{Tag: "omni", Title: "omni " + state + " · escolha o que mudar", Items: items})
		switch {
		case i < 0:
			a.ui.menuDone(false, "omni: nada mudou")
			return
		case i == 0:
			out, err := pfOmniCommand(a.cfg, a.agent.Model, []string{map[bool]string{true: "off", false: "on"}[a.cfg.Omni.Enabled]})
			if err != nil {
				a.ui.Error(err.Error())
				return
			}
			a.ui.menuDone(true, "omni "+map[bool]string{true: "ligado", false: "desligado"}[a.cfg.Omni.Enabled])
			_ = out
			continue
		case i == len(items)-1:
			out, _ := pfOmniCommand(a.cfg, a.agent.Model, nil)
			a.ui.print(a.ui.T.fg(cGray, 90) + pfIndentLines(out, "  ") + a.ui.T.sgr("0"))
			return
		}
		role := omniRoles[i-1]
		cur := omniRoleVal(*role.get(&a.cfg.Roles))
		r, ok := mpPick(a.ed, a.cfg, role.label+" ("+role.help+") · atual: "+cur, cur, "o omni escolhe por tarefa")
		if !ok {
			continue
		}
		if !a.mpEnsureReady(r) {
			continue
		}
		v := r.Ref
		if v == "auto" {
			v = ""
		}
		*role.get(&a.cfg.Roles) = v
		if err := a.cfg.Save(); err != nil {
			a.ui.Warn("não salvei: " + err.Error())
		}
		a.ui.menuDone(true, role.label+": "+omniRoleVal(v))
	}
}
