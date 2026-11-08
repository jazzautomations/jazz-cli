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

// nametgt/namepat: quoted + "in their name" é filtro de NOME (file_find),
// não padrão de conteúdo; "X files" com X palavra é *X* no namepat, não
// dir nem file.
func TestNLUNameTgtGuards(t *testing.T) {
	m, w := nuTestWorld(t)

	for _, tc := range []struct {
		text, intent, slot, want string
	}{
		{"find all the files that have the word \"fstab\" in their name in a folder", "file_find", "name", "fstab"},
		{"search all the files in the folder \"myfiles\" which have the word \"blue\" in their name", "file_find", "name", "blue"},
		{"Search the current directory tree for files containing \"string\" in their path names", "file_find", "name", "string"},
		{"Find all directories under current directory whose names are 33 characters long", "file_find", "name", "?????????????????????????????????"},
	} {
		in := m.Parse(tc.text, w)
		if in.Name != tc.intent || in.Slots[tc.slot] != tc.want {
			t.Errorf("%q -> %s %s=%q, queria %s %s=%q (%v)", tc.text, in.Name, tc.slot, in.Slots[tc.slot], tc.intent, tc.slot, tc.want, in.Slots)
		}
	}
	// namepat: "X files" → *X*, nunca dir nem conteúdo
	for _, tc := range []struct {
		text, intent string
	}{
		{"grep for 'timeout' in all config files", "text_grep"},
		{"grep for 'timeout' in all backup files", "text_grep"},
	} {
		in := m.Parse(tc.text, w)
		if in.Name != tc.intent || in.Slots["namepat"] == "" {
			t.Errorf("%q -> %s slots=%v, queria %s com namepat", tc.text, in.Name, in.Slots, tc.intent)
		}
		if in.Slots["dir"] != "" {
			t.Errorf("%q: dir=%q vazou (era filtro de nome)", tc.text, in.Slots["dir"])
		}
	}
}

// archive_extract mass + archive_create srcs + grep-de-linha: o cluster
// que misturava "unzip all *.zip" (each), "compacta a pasta X" (srcs via
// cue substantivo) e "lines containing 'x'" (text_grep, não line_slice).
func TestNLUArchiveMassGuards(t *testing.T) {
	m, w := nuTestWorld(t)

	for _, tc := range []struct {
		text, intent string
		check      func(map[string]string) bool
	}{
		{"Unzip all *.zip files under current directory", "archive_extract",
			func(s map[string]string) bool { return s["ext"] == "zip" && s["arc"] == "" }},
		{"unzip all zip files in current folder", "archive_extract",
			func(s map[string]string) bool { return s["ext"] == "zip" && s["arc"] == "" }},
		{"Unzip every \".gz\" file in the current directory", "archive_extract",
			func(s map[string]string) bool { return s["ext"] == "gz" && s["arc"] == "" }},
		{"unzip pack.tar.gz into dest", "archive_extract",
			func(s map[string]string) bool { return s["arc"] == "pack.tar.gz" && s["dir"] == "dest" }},
		{"extract site.zip into www", "archive_extract",
			func(s map[string]string) bool { return s["arc"] == "site.zip" && s["dir"] == "www" }},
		{"compacta a pasta logs em tar.gz", "archive_create",
			func(s map[string]string) bool { return s["srcs"] == "logs" && s["out"] == "tar.gz" && s["dir"] == "" }},
		{"compress the logs folder into a tarball", "archive_create",
			func(s map[string]string) bool { return s["srcs"] == "logs" && s["out"] == "tarball" }},
		{"Print all lines from file 'report.txt' containing 'error'", "text_grep",
			func(s map[string]string) bool { return s["file"] == "report.txt" && s["pattern"] == "error" }},
		{"Print all lines from file 'report.txt' containing any-cased 'error' pattern", "text_grep",
			func(s map[string]string) bool { return s["file"] == "report.txt" && s["pattern"] == "error" }},
	} {
		in := m.Parse(tc.text, w)
		if in.Name != tc.intent || !tc.check(in.Slots) {
			t.Errorf("%q -> %s slots=%v", tc.text, in.Name, in.Slots)
		}
	}
}

// nome-quoted sem ponto + suppress-phrase + find→delete: "locate file 'x'"
// e "filename is 'x'" são name de verdade; "suppress error messages" é
// frase-efeito (filler), não critério; "find files and delete" é mutação.
func TestNLUFindNameGuards(t *testing.T) {
	m, w := nuTestWorld(t)

	for _, tc := range []struct {
		text, intent string
		check        func(map[string]string) bool
	}{
		{`Locate file "file1"`, "file_find",
			func(s map[string]string) bool { return s["name"] == "file1" }},
		{`find regular files whose filename is "your_pattern", suppressing all error messages.`, "file_find",
			func(s map[string]string) bool {
				return s["name"] == "your_pattern" && s["name_re"] == "" && s["dir"] == ""
			}},
		{`find all the text files in current folder and force delete them`, "delete_file",
			func(s map[string]string) bool { return s["ext"] == "txt" && s["mass"] != "" }},
		{`Find all files/directories named 'apt' under current directory`, "file_find",
			func(s map[string]string) bool { return s["name"] == "apt" }},
		{`find files matching pattern 'file??'`, "file_find",
			func(s map[string]string) bool { return s["name"] == "file??" && s["name_re"] == "" }},
		{`find files whose names match regex '^ab.*z$'`, "file_find",
			func(s map[string]string) bool { return s["name_re"] == "^ab.*z$" }},
	} {
		in := m.Parse(tc.text, w)
		if in.Name != tc.intent || !tc.check(in.Slots) {
			t.Errorf("%q -> %s slots=%v", tc.text, in.Name, in.Slots)
		}
	}
}

// operandos quoted: "file1"/"ips.txt" são nomes de arquivo mesmo sem ponto
// ou precedidos de cue; texto multi-palavra quoted NUNCA é file slot;
// "lines in 'filename'" é operando de contagem, não escopo de diretório.
func TestNLUQuotedOperandGuards(t *testing.T) {
	m, w := nuTestWorld(t)

	for _, tc := range []struct {
		text, intent string
		check        func(map[string]string) bool
	}{
		{`Count the number of differing lines in "file1" and "file2"`, "wordcount_stats",
			func(s map[string]string) bool { return s["target"] == "file1" }},
		{`Print a count of duplicate lines in "filename"`, "wordcount_stats",
			func(s map[string]string) bool {
				return s["target"] == "filename" && s["dir"] == ""
			}},
		{`Print a count of all unique entries in "ips.txt"`, "text_count",
			func(s map[string]string) bool { return s["file"] == "ips.txt" }},
		{`List the number of occurrences of each unique character in "The quick brown fox"`, "text_count",
			func(s map[string]string) bool { return s["file"] == "" }},
		{`Print the contents of "numbers.txt"`, "open_file",
			func(s map[string]string) bool { return s["target"] == "numbers.txt" }},
	} {
		in := m.Parse(tc.text, w)
		if in.Name != tc.intent || !tc.check(in.Slots) {
			t.Errorf("%q -> %s slots=%v miss=%v", tc.text, in.Name, in.Slots, in.Missing)
		}
	}
}
