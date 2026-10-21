package main

// fuzzy_cmd.go: tolerant slash commands and menu options. "/modle" runs
// /model, "/mod" offers a numbered menu (mode, model, models), "/mode trubo"
// runs "/mode turbo". The menu reuses offers.go (Kind "slash").

import (
	"sort"
	"strings"
)

// fzAliases maps every alias to the command it stands for (a menu never
// offers two names of the same command).
var fzAliases = map[string]string{
	"/?": "/help", "/quit": "/exit", "/q": "/exit", "/perms": "/permissions", "/aprovacoes": "/approvals", "/aprovações": "/approvals",
	"/new": "/clear", "/sessions": "/resume", "/skills": "/skill", "/usage": "/cost",
}

// fzCommandNames lists every spelling the dispatcher knows (built-in names,
// aliases, custom commands).
func fzCommandNames(custom []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(n string) {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for _, c := range slashCommands {
		add(c.name)
	}
	for a := range fzAliases {
		add(a)
	}
	sort.Strings(out[len(slashCommands):])
	for _, c := range custom {
		add("/" + c)
	}
	return out
}

// fzCanon maps an alias to its command.
func fzCanon(n string) string {
	if c, ok := fzAliases[n]; ok {
		return c
	}
	return n
}

// fzResolve matches typed against names. exact = typed is a known name;
// otherwise cands are the plausible commands, best first; sure = exactly one
// clear winner (safe to run without asking).
func fzResolve(typed string, names []string) (cands []string, sure bool) {
	return fzResolveMin(typed, names, 2)
}

// fzResolveMin: like fzResolve with the shortest prefix that counts (1 for the
// closed argument sets: "/mode a" -> auto or ask).
func fzResolveMin(typed string, names []string, minPrefix int) (cands []string, sure bool) {
	t := strings.TrimPrefix(fzFold(typed), "/")
	if t == "" {
		return nil, false
	}
	type sc struct {
		name  string
		score int
	}
	best := map[string]int{} // canonical -> best score
	order := map[string]int{}
	for i, n := range names {
		fn := strings.TrimPrefix(fzFold(n), "/")
		if fn == t {
			return []string{fzCanon(n)}, true
		}
		if _, ok := order[fzCanon(n)]; !ok {
			order[fzCanon(n)] = i
		}
	}
	var prefixes []string
	rt := len([]rune(t))
	for _, n := range names {
		fn := strings.TrimPrefix(fzFold(n), "/")
		c := fzCanon(n)
		if rt >= minPrefix && strings.HasPrefix(fn, t) {
			dup := false
			for _, p := range prefixes {
				dup = dup || p == c
			}
			if !dup {
				prefixes = append(prefixes, c)
			}
			continue
		}
		if rt < 3 {
			continue
		}
		if d, ok := fzNearWord(t, fn); ok {
			if old, has := best[c]; !has || d < old {
				best[c] = d
			}
		}
	}
	if len(prefixes) == 1 {
		return prefixes, true
	}
	if len(prefixes) > 1 {
		sort.SliceStable(prefixes, func(i, j int) bool {
			if len(prefixes[i]) != len(prefixes[j]) {
				return len(prefixes[i]) < len(prefixes[j])
			}
			return order[prefixes[i]] < order[prefixes[j]]
		})
		if len(prefixes) > 3 {
			prefixes = prefixes[:3]
		}
		return prefixes, false
	}
	var all []sc
	for c, d := range best {
		all = append(all, sc{c, d})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].score != all[j].score {
			return all[i].score < all[j].score
		}
		return order[all[i].name] < order[all[j].name]
	})
	for i, a := range all {
		if i < 3 {
			cands = append(cands, a.name)
		}
	}
	sure = len(all) == 1 || len(all) > 1 && all[1].score-all[0].score >= fzSlip
	return cands, sure
}

// fzArgOptions are the closed sets of the first argument of some commands.
var fzArgOptions = map[string][]string{
	"/mode":   {"auto", "turbo", "smart", "ask", "yolo", "plan"},
	"/effort": {"low", "medium", "high", "off"},
	"/export": {"md", "html"},
	"/review": {"on", "off"},
}

// fzFixCommand repairs the command word and the first argument of a closed
// set. fixed = the line to run instead; menu = candidates when ambiguous (each
// a full line); note = what was interpreted. Zero values = leave it alone.
func fzFixCommand(line string, custom []string) (fixed string, menu []string, note string) {
	f := strings.Fields(line)
	if len(f) == 0 || !strings.HasPrefix(f[0], "/") {
		return
	}
	names := fzCommandNames(custom)
	rest := strings.TrimSpace(strings.TrimPrefix(line, f[0]))
	cmd := f[0]
	known := false
	for _, n := range names {
		if n == cmd {
			known = true
			break
		}
	}
	if !known {
		cands, sure := fzResolve(cmd, names)
		switch {
		case len(cands) == 0:
			return
		case !sure:
			for _, c := range cands {
				menu = append(menu, strings.TrimSpace(c+" "+rest))
			}
			return "", menu, ""
		}
		note = "interpretei " + cmd + " como " + cands[0]
		cmd = cands[0]
	}
	// closed-set argument
	canon := fzCanon(cmd)
	if opts, ok := fzArgOptions[canon]; ok && len(f) > 1 {
		a := f[1]
		cands, sure := fzResolveMin(a, opts, 1)
		exact := false
		for _, o := range opts {
			exact = exact || o == a
		}
		if canon == "/mode" && tbModeName(a) != a {
			exact = true // turbo aliases (full-auto)
		}
		if !exact && len(cands) > 0 {
			tail := strings.TrimSpace(strings.TrimPrefix(rest, a))
			mk := func(o string) string { return strings.TrimSpace(cmd + " " + o + " " + tail) }
			if !sure {
				for _, c := range cands {
					menu = append(menu, mk(c))
				}
				return "", menu, ""
			}
			if note != "" {
				note += ", "
			} else {
				note = "interpretei "
			}
			note += a + " como " + cands[0]
			return mk(cands[0]), nil, note
		}
	}
	if note != "" {
		return strings.TrimSpace(cmd + " " + rest), nil, note
	}
	return
}

// customNames lists the custom slash commands (without the slash).
func (a *App) customNames() []string {
	var out []string
	for _, c := range customCommands(a.root) {
		out = append(out, c.name)
	}
	return out
}

// fzOffer shows the candidates for an ambiguous command as a numbered menu.
func (a *App) fzOffer(typed string, lines []string) {
	help := map[string]string{}
	for _, c := range slashCommands {
		help[c.name] = c.help
	}
	var items []olAction
	for _, l := range lines {
		name := strings.Fields(l)[0]
		h := oneLine(help[name], 48)
		if h == "" {
			h = l
		}
		items = append(items, olAction{Key: name, Label: h, Kind: "slash", Cmd: l, Intent: "typo"})
	}
	a.olOffer("não entendi “"+oneLine(typed, 40)+"” — você quis dizer:", items)
}
