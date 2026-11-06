package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// cmdCatalog: a estante da Alexandria — o que o cérebro offline sabe fazer,
// agrupado por domínio, com os métodos que servem cada intenção, os slots
// que ela pede e os aprendidos (distill) marcados. `jazz catalog <intent>`
// abre o cartão: o que faz, o que precisa, e — honestamente — o que não faz.
func cmdCatalog(w io.Writer, args []string) int {
	m := nuGet()
	lib, _ := mhLibrary()
	if len(args) > 0 {
		return catalogCard(w, m, lib, args[0])
	}

	type entry struct {
		name, desc string
		required   []string
		optional   int
		methods    int
		learned    int
		risk       string
	}
	domains := map[string][]entry{}
	for _, in := range m.data.Intents {
		if in.Name == "none" || in.Name == "chitchat" {
			continue
		}
		e := entry{name: in.Name, desc: in.Desc}
		for s, d := range in.Slots {
			if d.Required {
				e.required = append(e.required, s)
			} else {
				e.optional++
			}
		}
		sort.Strings(e.required)
		for _, me := range lib[in.Name] {
			e.methods++
			if strings.HasPrefix(me.Name, dsLearnedPrefix) {
				e.learned++
			}
			if e.risk == "" || me.Risk == "high" {
				e.risk = me.Risk
			}
		}
		domains[catalogDomain(in.Name)] = append(domains[catalogDomain(in.Name)], e)
	}

	var names []string
	for d := range domains {
		names = append(names, d)
	}
	sort.Strings(names)
	total, learned := 0, 0
	for _, d := range names {
		es := domains[d]
		sort.Slice(es, func(i, j int) bool { return es[i].name < es[j].name })
		fmt.Fprintf(w, "%s (%d)\n", d, len(es))
		for _, e := range es {
			total++
			learned += e.learned
			line := "  " + e.name
			if e.desc != "" {
				line += " — " + e.desc
			}
			fmt.Fprintln(w, line)
			var meta []string
			if len(e.required) > 0 {
				meta = append(meta, "precisa: "+strings.Join(e.required, ", "))
			}
			if e.optional > 0 {
				meta = append(meta, fmt.Sprintf("+%d slots opcionais", e.optional))
			}
			if e.methods > 0 {
				s := fmt.Sprintf("%d método(s)", e.methods)
				if e.learned > 0 {
					s += fmt.Sprintf(", %d aprendido(s)", e.learned)
				}
				if e.risk != "" && e.risk != "low" {
					s += ", risco " + e.risk
				}
				meta = append(meta, s)
			} else {
				meta = append(meta, "sem método — só leitura da intenção")
			}
			fmt.Fprintln(w, "      "+strings.Join(meta, " · "))
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "%d intenções · %d métodos aprendidos\n", total, learned)
	return 0
}

// catalogCard: a ficha completa de uma intenção — descrição, slots, métodos
// com pré-condições e limites, e o que ela não cobre.
func catalogCard(w io.Writer, m *nuModel, lib map[string][]mhMethod, name string) int {
	var def *nuIntentDef
	for i := range m.data.Intents {
		if m.data.Intents[i].Name == name {
			def = &m.data.Intents[i]
			break
		}
	}
	if def == nil {
		fmt.Fprintf(w, "%q não é uma intenção conhecida — jazz catalog lista todas\n", name)
		return 2
	}
	fmt.Fprintf(w, "%s — %s\n\ndomínio: %s\n", def.Name, def.Desc, catalogDomain(name))
	if len(def.Slots) > 0 {
		var req, opt []string
		for s, d := range def.Slots {
			if d.Required {
				req = append(req, s)
			} else {
				opt = append(opt, s)
			}
		}
		sort.Strings(req)
		sort.Strings(opt)
		if len(req) > 0 {
			fmt.Fprintf(w, "precisa: %s\n", strings.Join(req, ", "))
		}
		if len(opt) > 0 {
			fmt.Fprintf(w, "opcionais: %s\n", strings.Join(opt, ", "))
		}
	}
	ms := lib[name]
	fmt.Fprintf(w, "\n%d método(s):\n", len(ms))
	for _, me := range ms {
		tag := ""
		if strings.HasPrefix(me.Name, dsLearnedPrefix) {
			tag = " [aprendido]"
		}
		fmt.Fprintf(w, "  · %s%s (risco %s)\n", me.Name, tag, me.Risk)
		if me.Doc != "" {
			fmt.Fprintf(w, "    %s\n", me.Doc)
		}
		var pres []string
		for _, c := range me.Pre {
			p := c.Fact + " " + c.Op
			if c.Val != "" {
				p += " " + c.Val
			}
			pres = append(pres, p)
		}
		if len(pres) > 0 {
			fmt.Fprintf(w, "    pré: %s\n", strings.Join(pres, "; "))
		}
		if len(me.Needs) > 0 {
			fmt.Fprintf(w, "    depende: %s\n", strings.Join(me.Needs, ", "))
		}
	}
	if len(def.Examples) > 0 {
		fmt.Fprintf(w, "\nexemplos que ela entende:\n")
		for _, e := range def.Examples[:min(4, len(def.Examples))] {
			fmt.Fprintf(w, "  %q\n", e)
		}
	}
	if len(def.Negatives) > 0 {
		fmt.Fprintf(w, "\nnão é isso (negatives):\n")
		for _, e := range def.Negatives[:min(3, len(def.Negatives))] {
			fmt.Fprintf(w, "  %q\n", e)
		}
	}
	return 0
}

// catalogDomain: o prefixo do nome da intenção vira a estante. Nomes sem
// prefixo conhecido caem em "operação"/"sistema" por uma lista curta.
func catalogDomain(name string) string {
	switch {
	case strings.HasPrefix(name, "file_"), strings.HasPrefix(name, "fs_"),
		name == "find", name == "list_files", name == "open_file",
		name == "delete_file", name == "move_file", name == "copy_file",
		name == "perm_find", name == "file_users", name == "archive_list",
		name == "dir_diff", name == "file_dupes", name == "create",
		name == "rename", name == "format", name == "hash_verify":
		return "arquivos"
	case strings.HasPrefix(name, "git_"), name == "blame":
		return "git"
	case strings.HasPrefix(name, "text_"), strings.HasPrefix(name, "line_"),
		name == "wordcount_stats", name == "count", name == "extract":
		return "texto"
	case strings.HasPrefix(name, "container_"):
		return "containers"
	case strings.HasPrefix(name, "service_"), strings.HasPrefix(name, "env_"),
		name == "disk_usage", name == "logged_in", name == "omni_status",
		name == "tool_status", name == "tool_info", name == "sys_info",
		name == "kill_process", name == "list_processes", name == "mem_usage",
		name == "uptime_load", name == "cron_list", name == "log_errors_recent",
		name == "pkg_installed", name == "port_owner":
		return "sistema"
	case strings.HasPrefix(name, "net_"), name == "connect_host",
		name == "download", name == "ports", name == "list_ports",
		name == "list_hosts", name == "host_status", name == "url_parse":
		return "rede"
	case strings.HasPrefix(name, "test"), name == "build", name == "lint",
		name == "run", name == "coverage", name == "fix", name == "run_tests",
		name == "symbol_refs", name == "dep_graph", name == "dep_outdated",
		name == "dup_code", name == "large_funcs", name == "todo_scan",
		name == "license_scan", name == "install_dep", name == "update_dep",
		name == "diff_apply_check", name == "dotenv_check":
		return "código"
	case strings.HasPrefix(name, "session_"), name == "undo",
		name == "redo", name == "explain", name == "project_summary":
		return "sessão"
	}
	return "outros"
}
