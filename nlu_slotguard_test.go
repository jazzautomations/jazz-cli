package main

import (
	"strings"
	"testing"
)

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
		{"Search the current directory tree for files containing \"string\" in their path names", "file_find", "namepat", "*string*"},
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
		{`List all *.ogg files under your home directory along with their size`, "file_find",
			func(s map[string]string) bool {
				return s["dir"] == "~" && s["name"] == "" && s["ext"] == "ogg"
			}},
		{`Find directory "your/dir" if it is empty`, "file_find",
			func(s map[string]string) bool {
				return s["dir"] == "" && s["name"] == "your/dir" && s["empty"] == "true"
			}},
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
		{`Print the contents of "numbers.txt"`, "open_file",
			func(s map[string]string) bool { return s["target"] == "numbers.txt" }},
	} {
		in := m.Parse(tc.text, w)
		if in.Name != tc.intent || !tc.check(in.Slots) {
			t.Errorf("%q -> %s slots=%v miss=%v", tc.text, in.Name, in.Slots, in.Missing)
		}
	}
}

// multi-ext: ".cpp, .c, .h" e ".c or .cpp" viram lista vírgula pro
// file_find (que aceita a lista desde este batch); "files here" não
// deixa "here" vazar pro name; mkdir/make-directory vão pro create.dir.
func TestNLUExtListGuards(t *testing.T) {
	m, w := nuTestWorld(t)

	for _, tc := range []struct {
		text, intent string
		check        func(map[string]string) bool
	}{
		{`show all .cpp, .c, .h, .hpp files in the folder src`, "list_files",
			func(s map[string]string) bool {
				return s["ext"] == "cpp,c,h,hpp" && s["dir"] == "src"
			}},
		{`find all .c or .cpp files here`, "file_find",
			func(s map[string]string) bool {
				return s["ext"] == "c,cpp" && s["name"] == ""
			}},
		{`list all .txt and .log files`, "list_files",
			func(s map[string]string) bool { return s["ext"] == "txt,log" }},
		{`Make directory "aaa/bbb"`, "create",
			func(s map[string]string) bool {
				return s["target"] == "aaa/bbb" && s["type"] == "dir"
			}},
		{`mkdir "data"`, "create",
			func(s map[string]string) bool {
				return s["target"] == "data" && s["type"] == "dir"
			}},
		{`create a file hello.py`, "create",
			func(s map[string]string) bool {
				return s["target"] == "hello.py" && s["type"] != "dir"
			}},
	} {
		in := m.Parse(tc.text, w)
		if in.Name != tc.intent || !tc.check(in.Slots) {
			t.Errorf("%q -> %s slots=%v miss=%v", tc.text, in.Name, in.Slots, in.Missing)
		}
	}
}

// âncoras de nome: "names begin/start/end with X" → glob X*/*X preservando
// a borda; "names contain 'log'" é predicado de nome (nametgt→name),
// não grep; "a git repository" é ESTE projeto, não dir "git".
func TestNLUNameAnchorGuards(t *testing.T) {
	m, w := nuTestWorld(t)

	for _, tc := range []struct {
		text, intent string
		check        func(map[string]string) bool
	}{
		{`Delete all the files whose names begin with "heapdump"`, "delete_file",
			func(s map[string]string) bool { return s["name"] == "heapdump*" }},
		{`files whose names end with "backup"`, "list_files",
			func(s map[string]string) bool { return s["namepat"] == "*backup" }},
		{`display all the directories which start with processor`, "list_files",
			func(s map[string]string) bool {
				return s["namepat"] == "processor*" && s["type"] == "dir"
			}},
		{`search for all the files which start with "file2015-0"`, "file_find",
			func(s map[string]string) bool { return s["name"] == "file2015-0*" }},
		{`find files whose names contain "log"`, "file_find",
			func(s map[string]string) bool {
				return s["namepat"] == "*log*" && s["ext"] == ""
			}},
		{`Counts total number of lines in a git repository`, "wordcount_stats",
			func(s map[string]string) bool { return s["dir"] == "" }},
	} {
		in := m.Parse(tc.text, w)
		if in.Name != tc.intent || !tc.check(in.Slots) {
			t.Errorf("%q -> %s slots=%v", tc.text, in.Name, in.Slots)
		}
	}
}

func TestNLUOutputTargetGuards(t *testing.T) {
	m, w := nuTestWorld(t)

	for _, tc := range []struct {
		text, intent string
		check        func(map[string]string) bool
	}{
		{`Find all *.swp files and print the list to a file named 'rm'`, "file_find",
			func(s map[string]string) bool {
				return s["ext"] == "swp" && s["out"] == "rm" && s["name"] == ""
			}},
		{`save the list to "output.txt"`, "list_files",
			func(s map[string]string) bool { return s["out"] == "output.txt" }},
		{`move 'a.txt' to 'b.txt'`, "file_move",
			func(s map[string]string) bool { return s["dest"] == "b.txt" }},
		{`sort data.csv to sorted.csv`, "sort_op",
			func(s map[string]string) bool { return s["to"] == "sorted.csv" && s["file"] == "data.csv" }},
		{`Ungzip and untar all files matching "www-backup.tar.*"`, "archive_extract",
			func(s map[string]string) bool {
				return s["namepat"] == "www-backup.tar.*" && s["arc"] == ""
			}},
		{`mkdir "data"`, "create",
			func(s map[string]string) bool { return s["target"] == "data" && s["type"] == "dir" }},
	} {
		in := m.Parse(tc.text, w)
		if in.Name != tc.intent || !tc.check(in.Slots) {
			t.Errorf("%q -> %s slots=%v", tc.text, in.Name, in.Slots)
		}
	}

	// "Output only the filetype suffix of X" não é busca — sem capacidade
	// determinística a resposta honesta é confiança baixa, não file_find.
	in := m.Parse(`Output only the filetype suffix of "foo.tar.gz", in this case "gz"`, w)
	if in.Name == "file_find" {
		t.Errorf("metaask vazou pra file_find: slots=%v", in.Slots)
	}
}

func TestBrSaveTailNoSplit(t *testing.T) {
	m, w := nuTestWorld(t)
	if parts := brSplitMulti(`Find all *.swp files and print the list to a file named 'rm'`, m, w); len(parts) != 0 {
		t.Errorf("cauda de persistência virou split: %v", parts)
	}
	// split legítimo segue: duas tasks independentes continuam duas
	if parts := brSplitMulti(`find *.log files and delete them`, m, w); len(parts) < 2 {
		t.Errorf("split legítimo morreu: %v", parts)
	}
}

func TestNLUSetPatternGuards(t *testing.T) {
	m, w := nuTestWorld(t)

	for _, tc := range []struct {
		text, intent string
		check        func(map[string]string) bool
	}{
		{`Find all *conf* files and grep 'timeout' in them`, "text_grep",
			func(s map[string]string) bool {
				return s["namepat"] == "*conf*" && s["pattern"] == "timeout" && s["dir"] == ""
			}},
		{`display a long listing of all the normal files in the current folder (print0 for each)`, "list_files",
			func(s map[string]string) bool { return s["dir"] == "" && s["type"] == "file" }},
		{`grep 'timeout' in all config files`, "text_grep",
			func(s map[string]string) bool { return s["namepat"] == "*config*" && s["dir"] == "" }},
	} {
		in := m.Parse(tc.text, w)
		if in.Name != tc.intent || !tc.check(in.Slots) {
			t.Errorf("%q -> %s slots=%v", tc.text, in.Name, in.Slots)
		}
	}

	// semântica por-item ("de cada palavra/caractere") não tem método
	// determinístico — recusa honesta, nunca contagem fabricada.
	for _, s := range []string{
		`From the list of words in list1.txt, display the number of occurrences of each word in file2.txt`,
		`List the number of occurrences of each unique character in "The quick brown fox"`,
	} {
		if in := m.Parse(s, w); in.Understood() {
			t.Errorf("%q -> %s (falso positivo de per-item)", s, in.Name)
		}
	}
}

func TestNLUSrcOwnerGuards(t *testing.T) {
	m, w := nuTestWorld(t)

	for _, tc := range []struct {
		text, intent string
		check        func(map[string]string) bool
	}{
		{`change the owner of all regular files which belong to root user to admin`, "perm_set",
			func(s map[string]string) bool {
				return s["owner"] == "admin" && s["src_owner"] == "root"
			}},
		{`chmod 700 on files owned by root`, "perm_set",
			func(s map[string]string) bool { return s["mode"] == "700" && s["src_owner"] == "root" }},
		{`change owner of all files to admin`, "perm_set",
			func(s map[string]string) bool { return s["owner"] == "admin" && s["src_owner"] == "" }},
		{`files belonging to group www`, "perm_find",
			func(s map[string]string) bool { return s["group"] == "www" }},
		{`Search for files owned by user www`, "perm_find",
			func(s map[string]string) bool { return s["owner"] == "www" && s["src_owner"] == "" }},
	} {
		in := m.Parse(tc.text, w)
		if in.Name != tc.intent || !tc.check(in.Slots) {
			t.Errorf("%q -> %s slots=%v", tc.text, in.Name, in.Slots)
		}
	}
}

// replace/slice/perm: palavras de contagem não são texto-a-substituir,
// cue-words de slice não viram unfed, segundo arquivo não cabe em método
// de arquivo único, e "group owner X" é filtro de origem (nunca alvo).
func TestNLUSlicePermGuards(t *testing.T) {
	m, w := nuTestWorld(t)

	for _, tc := range []struct {
		text, intent string
		check        func(map[string]string) bool
	}{
		{`Replace all occurrences of foo (case insensitive) with bar in all *.css files`, "text_replace",
			func(s map[string]string) bool {
				return s["from"] == "foo" && s["to"] == "bar" && s["ext"] == "css"
			}},
		{`Replace every instance of "bad" with "good" in notes.txt`, "text_replace",
			func(s map[string]string) bool { return s["from"] == "bad" && s["to"] == "good" }},
		{`Find out all *.sh owned by user vivek`, "perm_find",
			func(s map[string]string) bool { return s["owner"] == "vivek" && s["ext"] == "sh" }},
		{`change owner and group of all files with group owner vboxusers into home directory`, "perm_set",
			func(s map[string]string) bool {
				return s["src_group"] == "vboxusers" && s["group"] == ""
			}},
		{`tail the last 50 lines of report.txt`, "line_slice",
			func(s map[string]string) bool { return s["tail"] == "50" && s["file"] == "report.txt" }},
		{`show first 10 lines of log.txt`, "line_slice",
			func(s map[string]string) bool { return s["start"] == "1" && s["end"] == "10" }},
	} {
		in := m.Parse(tc.text, w)
		if in.Name != tc.intent || !in.Understood() || !tc.check(in.Slots) {
			t.Errorf("%q -> %s conf=%.2f slots=%v", tc.text, in.Name, in.Confidence, in.Slots)
		}
	}

	// segundo operando-arquivo órfão: o método roda UM arquivo — rodar só
	// o primeiro seria falso sucesso; cai pra clarify.
	if in := m.Parse(`Print unique lines in "file_a" and "file_b"`, w); in.Understood() {
		t.Errorf("multi-file slice -> %s %.2f (falso positivo)", in.Name, in.Confidence)
	}
}

// guards do lote: nome de dir não paga shape-penalty, '*dir' não é typo
// de mkdir, filler não alimenta slot por entity-hit, 'spaces in names'
// vira glob '* *', e glob nunca entra no slot file (required-backfill).
func TestNLUMiscGuards(t *testing.T) {
	m, w := nuTestWorld(t)

	for _, tc := range []struct {
		text, intent string
		check        func(map[string]string) bool
	}{
		{`mkdir data`, "create",
			func(s map[string]string) bool { return s["target"] == "data" && s["type"] == "dir" }},
		{`create a directory called mydir`, "create",
			func(s map[string]string) bool { return s["target"] == "mydir" && s["type"] == "dir" }},
		{`list files with spaces in their names`, "list_files",
			func(s map[string]string) bool { return s["namepat"] == "* *" }},
		{`find files whose names contain spaces`, "file_find",
			func(s map[string]string) bool { return s["name"] == "* *" }},
	} {
		in := m.Parse(tc.text, w)
		if in.Name != tc.intent || !in.Understood() || !tc.check(in.Slots) {
			t.Errorf("%q -> %s conf=%.2f slots=%v", tc.text, in.Name, in.Confidence, in.Slots)
		}
	}

	// glob nunca preenche slot file de arquivo único → miss honesto
	in := m.Parse(`Counts the number of lines in each *.hpp or *.cpp file`, w)
	if in.Slots["file"] == "*.cpp" || in.Slots["file"] == "*.hpp" {
		t.Errorf("glob vazou pro slot file: %v", in.Slots)
	}

	// 'mydir' não é typo de 'mkdir'; 'separated' não é typo de 'separator'
	for _, s := range []string{
		`Print out the names of all directories in mydir recursively`,
		`Print working directory separated by newlines`,
	} {
		if in := m.Parse(s, w); in.Name == "create" {
			t.Errorf("%q -> create (typo-repair engoliu palavra real)", s)
		}
	}
}

// O "quê" do move é o substantivo depois do verbo — "files in sub
// directories" move ARQUIVOS (dir é lugar), e uma palavra-dir depois de
// cue de escopo nunca é o tipo. Sem isso o for_each moveria pastas.
func TestNLUMoveTypeGuards(t *testing.T) {
	m, w := nuTestWorld(t)

	for _, tc := range []struct {
		text, intent string
		check        func(map[string]string) bool
	}{
		{`Move all regular files under current directory to ./newdir`, "file_move",
			func(s map[string]string) bool {
				return s["dest"] == "./newdir" && s["type"] == "file"
			}},
		{`move the files in sub directories to /backup`, "file_move",
			func(s map[string]string) bool { return s["type"] == "file" }},
		{`move files in the current directory non-recursively to ./flat`, "file_move",
			func(s map[string]string) bool { return s["type"] == "file" }},
		{`move directories to dir`, "file_move",
			func(s map[string]string) bool { return s["type"] == "dir" }},
		{`move the directory data to /bak`, "file_move",
			func(s map[string]string) bool { return s["type"] == "dir" }},
	} {
		in := m.Parse(tc.text, w)
		if in.Name != tc.intent || !tc.check(in.Slots) {
			t.Errorf("%q -> %s slots=%v", tc.text, in.Name, in.Slots)
		}
	}
}

// Linguagem de contagem nunca é conteúdo do pattern ("count occurrences
// of foo" conta foo), e "X of foo" depois de palavra de contagem abre o
// pattern. O arquivo citado vai pro file, nunca pro pattern.
func TestNLUCountPatternGuards(t *testing.T) {
	m, w := nuTestWorld(t)

	for _, tc := range []struct {
		text, intent string
		check        func(map[string]string) bool
	}{
		{`count occurrences of foo in report.txt`, "text_count",
			func(s map[string]string) bool {
				return s["file"] == "report.txt" && s["pattern"] == "foo"
			}},
		{`Search for the regex '^aaa$' in all *.txt files under current directory and count the number of matches`, "text_count",
			func(s map[string]string) bool {
				return s["ext"] == "txt" && s["mass"] == "1" &&
					s["pattern"] == "^aaa$" && s["rx"] == "1" && s["file"] == ""
			}},
		{`how many times does 'error' appear in all *.log files`, "text_count",
			func(s map[string]string) bool {
				return s["ext"] == "log" && s["mass"] == "1" && s["pattern"] == "error"
			}},
		{`Print a count of all unique entries in "ips.txt"`, "text_count",
			func(s map[string]string) bool { return s["pattern"] != "ips.txt" }},
		{`search files containing foo`, "text_grep",
			func(s map[string]string) bool {
				return s["mass"] == "1" && s["pattern"] == "foo"
			}},
	} {
		in := m.Parse(tc.text, w)
		if in.Name != tc.intent || !tc.check(in.Slots) {
			t.Errorf("%q -> %s slots=%v", tc.text, in.Name, in.Slots)
		}
	}
}

// Predicado de nome sem aspas — "named test", "names contain log",
// "have foo in their names", "begin/end with X" — e uma palavra-ext
// depois de âncora de nome é literal de prefixo/sufixo, nunca filtro.
func TestNLUNamePredicateGuards(t *testing.T) {
	m, w := nuTestWorld(t)

	for _, tc := range []struct {
		text, intent string
		check        func(map[string]string) bool
	}{
		{`find files named test`, "file_find",
			func(s map[string]string) bool { return s["name"] == "test" }},
		{`find files whose names contain log`, "file_find",
			func(s map[string]string) bool {
				return s["namepat"] == "*log*" && s["ext"] == ""
			}},
		{`list files that have foo in their names`, "file_find",
			func(s map[string]string) bool { return s["namepat"] == "*foo*" }},
		{`find files whose names begin with log`, "file_find",
			func(s map[string]string) bool {
				return s["name"] == "log*" && s["ext"] == ""
			}},
		{`find files whose names end with bak`, "file_find",
			func(s map[string]string) bool { return s["name"] == "*bak" }},
		{`find files matching ERROR`, "find",
			func(s map[string]string) bool { return s["query"] == "ERROR" }},
	} {
		in := m.Parse(tc.text, w)
		if in.Name != tc.intent || !tc.check(in.Slots) {
			t.Errorf("%q -> %s slots=%v", tc.text, in.Name, in.Slots)
		}
	}
}

// "writable by both owner and group" é AND-mask 220 (donO+grupO), nunca
// world-writable (o+w) — um ww aqui devolveria arquivos errados. O `ww`
// só dispara com escopo mundo explícito.
func TestNLUPermBitsGuards(t *testing.T) {
	m, w := nuTestWorld(t)

	for _, tc := range []struct {
		text, intent string
		check        func(map[string]string) bool
	}{
		{`Search for files/directories which are writable by both their owner and their group`, "perm_find",
			func(s map[string]string) bool {
				return s["bits"] == "220" && s["ww"] == ""
			}},
		{`find files writable by the owner`, "perm_find",
			func(s map[string]string) bool {
				return s["bits"] == "200" && s["ww"] == ""
			}},
		{`world-writable files`, "perm_find",
			func(s map[string]string) bool { return s["ww"] == "true" }},
		{`find files readable by everyone`, "perm_find",
			func(s map[string]string) bool {
				return s["bits"] == "444" || s["aread"] == "true"
			}},
		{`find files missing g+w and o+w`, "perm_find",
			func(s map[string]string) bool {
				return s["not_bits"] == "022" && s["bits"] == "" && s["name"] == ""
			}},
		{`Search the /path directory tree for files missing g+w and o+w bits`, "perm_find",
			func(s map[string]string) bool {
				return s["not_bits"] == "022" && s["name"] == ""
			}},
		{`find files missing read permission`, "perm_find",
			func(s map[string]string) bool {
				return s["aread"] == "" && s["bits"] == ""
			}},
		{`files not world-writable`, "perm_find",
			func(s map[string]string) bool { return s["nww"] == "true" }},
		{`Find all files owned by "shadow" group`, "perm_find",
			func(s map[string]string) bool {
				return s["group"] == "shadow" && s["owner"] == ""
			}},
		{`find files belonging to the admin group`, "perm_find",
			func(s map[string]string) bool { return s["group"] == "admin" }},
	} {
		in := m.Parse(tc.text, w)
		if in.Name != tc.intent || !tc.check(in.Slots) {
			t.Errorf("%q -> %s slots=%v", tc.text, in.Name, in.Slots)
		}
	}

	// "and their group" é posse dentro da mesma oração — nunca split
	if parts := brSplitMulti(`find files writable by both their owner and their group`, m, w); len(parts) != 0 {
		t.Errorf("conjunção de classe virou split: %v", parts)
	}
}

// Colunas de saída pedidas junto ao find ("with their timestamp and
// paths", "have been modified") descrevem o LISTING — não viram dir/name.
func TestNLUFormatWordGuards(t *testing.T) {
	m, w := nuTestWorld(t)

	for _, tc := range []struct {
		text, intent string
		check        func(map[string]string) bool
	}{
		{`Find all files under current directory with their timestamp and paths and print a sorted list`, "file_find",
			func(s map[string]string) bool {
				return s["dir"] == "" && s["name"] == "" && s["type"] == "file"
			}},
		{`find all the files in home folder which have been modified in the last 24 hours`, "file_find",
			func(s map[string]string) bool { return s["name"] == "" && s["newer"] != "" }},
	} {
		in := m.Parse(tc.text, w)
		if in.Name != tc.intent || !tc.check(in.Slots) {
			t.Errorf("%q -> %s slots=%v", tc.text, in.Name, in.Slots)
		}
	}
}

// mass é o alvo (sem pergunta), name exato ≠ substring, files/directories
// é union (sem type), e rename por extensão vira extswap — nunca list_files.
func TestNLUMassRenameGuards(t *testing.T) {
	m, w := nuTestWorld(t)

	for _, tc := range []struct {
		text, intent string
		check        func(map[string]string) bool
	}{
		{`Delete everything in the current directory`, "delete_file",
			func(s map[string]string) bool { return s["mass"] != "" }},
		{`Remove all .php files in the /var/www/ directory`, "delete_file",
			func(s map[string]string) bool { return s["dir"] == "/var/www/" && s["ext"] == "php" }},
		{`Delete all files/directories named 'file' under current directory tree`, "delete_file",
			func(s map[string]string) bool {
				return s["name"] == "file" && s["type"] == "" && s["mass"] != ""
			}},
		{`rename all the png files to jpg files in the current folder`, "rename",
			func(s map[string]string) bool {
				return s["ext"] == "png" && s["to_ext"] == "jpg" && s["from"] == "*.png"
			}},
		{`rename "blah1" to "blah1-new"`, "rename",
			func(s map[string]string) bool {
				return s["from"] == "blah1" && s["to"] == "blah1-new" && s["ext"] == ""
			}},
		{`find files newer than report.txt`, "file_find",
			func(s map[string]string) bool {
				return s["newer_file"] == "report.txt" && s["name"] == ""
			}},
		{`find files older than report.txt`, "file_find",
			func(s map[string]string) bool {
				return s["older_file"] == "report.txt" && s["name"] == ""
			}},
		{`find files newer than 3 days`, "file_find",
			func(s map[string]string) bool {
				return s["newer"] != "" && s["newer_file"] == ""
			}},
		{`move them to backup`, "file_move",
			func(s map[string]string) bool {
				return s["dest"] == "backup"
			}},
		{`find files with size greater than 100 bytes`, "file_find",
			func(s map[string]string) bool {
				return s["min_size"] == "101" && s["max_size"] == "" && s["name"] == ""
			}},
		{`find files smaller than 50 bytes`, "file_find",
			func(s map[string]string) bool {
				return s["max_size"] == "49" && s["min_size"] == ""
			}},
		{`find files at least 100 bytes`, "file_find",
			func(s map[string]string) bool {
				return s["min_size"] == "100"
			}},
		{`find files at most 200 bytes`, "file_find",
			func(s map[string]string) bool {
				return s["max_size"] == "200"
			}},
		{`find all file name in curent directory -exec will file all file output awk used here for print only file name`, "file_find",
			func(s map[string]string) bool {
				return s["dir"] != "-exec" && !strings.Contains(s["name"], "awk") &&
					!strings.Contains(s["name"], "output")
			}},
	} {
		in := m.Parse(tc.text, w)
		if in.Name != tc.intent || !tc.check(in.Slots) || len(in.Missing) > 0 {
			t.Errorf("%q -> %s slots=%v miss=%v", tc.text, in.Name, in.Slots, in.Missing)
		}
	}
}
