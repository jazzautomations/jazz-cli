package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// cmdCatalog: a estante da Alexandria — o que o cérebro offline sabe fazer,
// agrupado por domínio, com os métodos que servem cada intenção, os slots
// que ela pede e os aprendidos (distill) marcados.
func cmdCatalog(w io.Writer) int {
	m := nuGet()
	lib, _ := mhLibrary()

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
