package main

import "testing"

// Slot guards: a typed slot must never swallow a leftover word of the wrong
// kind — "regular/normal" is not a limit, "megabytes" is not a directory,
// "last week" is a time window. These were the dominant v14 failure
// signature (path not found: were/last/megabytes, limit must be a number).
func TestNLUSlotGuards(t *testing.T) {
	m, w := nuTestWorld(t)
	// number-kind slots: digits only, never a leftover name
	for _, text := range []string{
		"show all regular/normal the files in the current folder whose size is bigger than 0 bytes",
		"search files whose size is 10 bytes in regular format",
		"find files bigger than 5 megabytes and list the first 20",
	} {
		in := m.Parse(text, w)
		if v := in.Slots["limit"]; v != "" && !nuIsNumber(v) {
			t.Errorf("%q limit=%q — not a number (%v)", text, v, in.Slots)
		}
	}
	// units and time words after "in" are never a directory
	for text, bad := range map[string]string{
		"search files in megabytes":                 "megabytes",
		"find files in kilobytes":                   "kilobytes",
		"list files in bytes":                       "bytes",
		"find files modified in the last week":      "last",
		"show files from in the past month":         "past",
		"files modified in the previous 2 days":     "previous",
		"find files inside $DIR":                    "$DIR",
		"find files in the future releases":         "future",
	} {
		in := m.Parse(text, w)
		if in.Slots["dir"] == bad {
			t.Errorf("%q dir=%q — %q is not a directory (%v)", text, bad, bad, in.Slots)
		}
	}
	// a real cued dir still resolves
	in := m.Parse("acha arquivos na pasta downloads", w)
	if in.Slots["dir"] != "downloads" {
		t.Errorf("cued dir lost: %v", in.Slots)
	}
	// a numeric limit still fills from a real number
	in = m.Parse("find files limit 10", w)
	if v := in.Slots["limit"]; v != "" && !nuIsNumber(v) {
		t.Errorf("limit=%q not numeric", v)
	}
	// owner/group names: quoted names, conjunctions, paths and the
	// "no owner" EN-negation that the PT "no" preposition must not eat
	ownerw := map[string]map[string]string{
		"files owned by user 'takuya'":                    {"owner": "takuya"},
		"files that belong to the group adm":              {"group": "adm"},
		"files which belong to id":                        {"owner": "id"},
		"files with no owner or group":                    {"orphan": "true"},
		"files not owned by 'daniel' in the logs dir":     {"not_owner": "daniel"},
	}
	for text, wants := range ownerw {
		in := m.Parse(text, w)
		for k, v := range wants {
			if in.Slots[k] != v {
				t.Errorf("%q slots %s=%q want %q (%v)", text, k, in.Slots[k], v, in.Slots)
			}
		}
		if in.Slots["dir"] == "owner" || in.Slots["owner"] == "or" || in.Slots["group"] == "or" {
			t.Errorf("%q poisoned: %v", text, in.Slots)
		}
	}
	// pronouns, qualifiers and dotted filenames are never owner/group names;
	// "for group" near a mode word is a permission CLASS (g-w), not a chgrp
	permcases := map[string]map[string]string{
		"change owner to alice and group to staff on app.log":       {"file": "app.log", "owner": "alice", "group": "staff"},
		"remove write permission for group on data.csv":             {"file": "data.csv", "symnw": "g-w"},
		"remove write permission for the user on run.sh":            {"file": "run.sh", "symnw": "u-w"},
		"make script.sh executable":                                 {"file": "script.sh", "symexec": "a+x"},
		"Change all directories under ./storage/ to owner apache and group lara": {"dir": "./storage/", "owner": "apache", "group": "lara"},
	}
	for text, wants := range permcases {
		in := m.Parse(text, w)
		for k, v := range wants {
			if in.Slots[k] != v {
				t.Errorf("%q slots %s=%q want %q (%v)", text, k, in.Slots[k], v, in.Slots)
			}
		}
	}
	// "for group" é classe: o slot group NÃO pode engolir o arquivo
	in = m.Parse("remove write permission for group on data.csv", w)
	if in.Slots["group"] == "data.csv" {
		t.Errorf("group roubou o arquivo: %v", in.Slots)
	}
	// "known user" negado = órfão (dono sem conta); sem negação, não dispara
	for _, text := range []string{
		"display files which do not belong to a known user",
		"arquivos que não pertencem a um usuário conhecido",
	} {
		in := m.Parse(text, w)
		if in.Slots["orphan"] != "true" {
			t.Errorf("%q: orphan=%q want true (%v)", text, in.Slots["orphan"], in.Slots)
		}
	}
	in = m.Parse("arquivos do usuário conhecido", w)
	if in.Slots["orphan"] != "" {
		t.Errorf("orphan sem negação: %v", in.Slots)
	}
	// "usuário conhecido"/"their owner" falam do dono genérico — não são nomes
	for _, text := range []string{
		"arquivos que não pertencem a um usuário conhecido",
		"find files writable by either their owner or their group",
	} {
		in := m.Parse(text, w)
		for _, k := range []string{"owner", "group", "not_owner", "not_group"} {
			if v := in.Slots[k]; v == "conhecido" || v == "their" || v == "either" {
				t.Errorf("%q: %s=%q é qualificador, não nome (%v)", text, k, v, in.Slots)
			}
		}
	}
	// contagem de linhas: exata, mínimo ("pelo menos"/"more than") e máximo
	// ("menos de" é estrito = N-1; "no máximo" é N)
	linecases := map[string]map[string]string{
		"find all PHP files that contain only one line": {"lines": "1"},
		"find files with more than 10 lines":            {"min_lines": "11"},
		"arquivos com pelo menos 5 linhas":              {"min_lines": "5"},
		"lista arquivos com menos de 20 linhas":         {"max_lines": "19"},
		"files with no more than 20 lines":              {"max_lines": "20"},
	}
	for text, wants := range linecases {
		in := m.Parse(text, w)
		if in.Name != "file_find" {
			t.Errorf("%q intent=%s want file_find", text, in.Name)
		}
		for k, v := range wants {
			if in.Slots[k] != v {
				t.Errorf("%q slots %s=%q want %q (%v)", text, k, in.Slots[k], v, in.Slots)
			}
		}
	}
	// o verbo do subcomando ("jazz ask X") vaza pro texto: não pode virar name
	in = m.Parse("ask find all PHP files that contain only one line", w)
	if in.Slots["name"] == "ask" {
		t.Errorf("name roubou o verbo ask: %v", in.Slots)
	}
	if in.Slots["lines"] != "1" {
		t.Errorf("lines perdido com ask: %v", in.Slots)
	}
	// auto-referência: "current user"/"usuário atual"/"to me" resolve pra
	// conta real — nunca literal "pro"/"atual"/"me"; grupo não herda
	for _, text := range []string{
		"change the owner of file x.txt to the current user",
		"change the owner of file x.txt to me",
		"muda o dono da pasta data pro usuário atual",
	} {
		in := m.Parse(text, w)
		own := in.Slots["owner"]
		if own == "" || own == "pro" || own == "atual" || own == "me" || own == "current" {
			t.Errorf("%q: owner=%q devia resolver a conta atual (%v)", text, own, in.Slots)
		}
		if in.Slots["group"] == own {
			t.Errorf("%q: group herdou o self de owner (%v)", text, in.Slots)
		}
	}
}

// Perm-read/exec natural-language filters + glob/regex split (v18 mining):
// "execute permission for everybody" é a+x (0111), "read for user" é u+r,
// glob "'file??'"/'*.rb' é name/ext — nunca name_re — e "not matching" é
// exclude, não match invertido.
func TestNLUPermGlobGuards(t *testing.T) {
	m, w := nuTestWorld(t)

	if in := m.Parse("Search the current directory tree for directories lacking execute permissions for user, group, or others", w); in.Slots["nx"] == "" || in.Slots["dir"] != "" {
		t.Errorf("lacking-exec: %v", in.Slots)
	}
	if in := m.Parse("Search the current directory tree for directories with the execute permission set for everybody", w); in.Slots["exall"] == "" {
		t.Errorf("exec-for-everybody: %v", in.Slots)
	}
	if in := m.Parse("display all the files which have read permission to the user", w); in.Slots["uread"] == "" {
		t.Errorf("read-for-user: %v", in.Slots)
	}
	if in := m.Parse("Search the current directory recursively for regular files with the read permission set for everybody", w); in.Slots["aread"] == "" {
		t.Errorf("read-for-everybody: %v", in.Slots)
	}

	// glob quoted: vai pra name/exclude/ext — nunca name_re (regex quebra)
	for _, c := range []struct {
		text, slot, want string
	}{
		{"find files matching pattern 'file??'", "name", "file??"},
		{"Find files not matching shell pattern '*/vendor/*'", "exclude", "*/vendor/*"},
		{"Search the current directory tree for all files matching pattern '*.rb'", "ext", "rb"},
	} {
		in := m.Parse(c.text, w)
		if in.Slots["name_re"] != "" {
			t.Errorf("%q: glob vazou pro name_re (%v)", c.text, in.Slots)
		}
		if in.Slots[c.slot] != c.want {
			t.Errorf("%q: %s=%q, queria %q (%v)", c.text, c.slot, in.Slots[c.slot], c.want, in.Slots)
		}
	}
}
