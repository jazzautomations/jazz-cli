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
		"search files in megabytes":             "megabytes",
		"find files in kilobytes":               "kilobytes",
		"list files in bytes":                   "bytes",
		"find files modified in the last week":  "last",
		"show files from in the past month":     "past",
		"files modified in the previous 2 days": "previous",
		"find files in the future releases":     "future",
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
		"files owned by user 'takuya'":                {"owner": "takuya"},
		"files that belong to the group adm":          {"group": "adm"},
		"files which belong to id":                    {"owner": "id"},
		"files with no owner or group":                {"orphan": "true"},
		"files not owned by 'daniel' in the logs dir": {"not_owner": "daniel"},
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
		"change owner to alice and group to staff on app.log":                    {"file": "app.log", "owner": "alice", "group": "staff"},
		"remove write permission for group on data.csv":                          {"file": "data.csv", "symnw": "g-w"},
		"remove write permission for the user on run.sh":                         {"file": "run.sh", "symnw": "u-w"},
		"make script.sh executable":                                              {"file": "script.sh", "symexec": "a+x"},
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
		check        func(map[string]string) bool
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

// Cláusula de EXCEÇÃO ("except/without/excluding…"): o operando só
// alimenta o slot exclude — nunca vira name/dir/pattern de outro slot,
// nunca evapora em silêncio. Lista vira "a|b"; intent sem o slot
// devolve o operando em Dropped (unverified); slots de negação/limite
// (not_*, min_*, max_*) são a semântica do cue e ficam intactos.
func TestNLUExceptClauseGuards(t *testing.T) {
	m, w := nuTestWorld(t)

	// operando pousa em exclude — inclusive o que outro extrator roubaria
	for _, tc := range []struct {
		text, intent, exclude string
	}{
		{"find files excluding hidden files", "file_find", ".*"},
		{"find files except build/external", "file_find", "build/external"},
		{"search files except in tmp", "file_find", "tmp"},
		{"find all files except those named foo", "file_find", "foo"},
		{"find files except alpha and beta", "file_find", "alpha|beta"},
		{"find files except alpha, beta and gamma", "file_find", "alpha|beta|gamma"},
		{"find files except *.log", "file_find", "*.log"},
		{"delete all files except the .md ones", "delete_file", ".md"},
		{"find files without the extension .tmp", "file_find", ".tmp"},
		{"delete everything except backups", "delete_file", "backups"},
	} {
		in := m.Parse(tc.text, w)
		if in.Name != tc.intent || in.Slots["exclude"] != tc.exclude {
			t.Errorf("%q -> %s slots=%v, queria %s exclude=%q", tc.text, in.Name, in.Slots, tc.intent, tc.exclude)
		}
	}

	// o escopo/destino fora do span da cláusula não é varrido
	for _, tc := range []struct {
		text, slot, want string
	}{
		{"find files under docs except path docs/x", "dir", "docs"},
		{"find files except a.txt in dir", "dir", "dir"},
		{"find regular files except in the cache dir", "exclude", "cache"},
	} {
		in := m.Parse(tc.text, w)
		if in.Slots[tc.slot] != tc.want {
			t.Errorf("%q -> slots=%v, queria %s=%q", tc.text, in.Slots, tc.slot, tc.want)
		}
	}

	// slots de negação/limite sobrevivem ao lado do cue — são a
	// semântica do pedido, não conjunto excluído
	for _, tc := range []struct {
		text, slot, want string
	}{
		{"files not owned by wnj", "not_owner", "wnj"},
		{"find files without 777 permission", "not_mode", "777"},
		{"arquivos com mais de 5 linhas exceto x.txt", "min_lines", "6"},
		{"arquivos com mais de 5 linhas exceto x.txt", "exclude", "x.txt"},
	} {
		in := m.Parse(tc.text, w)
		if in.Slots[tc.slot] != tc.want || len(in.Dropped) > 0 {
			t.Errorf("%q -> slots=%v dropped=%v, queria %s=%q sem dropped", tc.text, in.Slots, in.Dropped, tc.slot, tc.want)
		}
	}

	// operando que não cabe (intent sem slot exclude) vira Dropped —
	// unverified honesto, nunca exclusão ignorada verde
	for _, tc := range []struct {
		text  string
		drops []string
	}{
		{"list files except alpha and beta", []string{"alpha", "beta"}},
		{"show files excluding .git", []string{".git"}},
		{"find files excluding paths containing 'a'", []string{"a"}},
	} {
		in := m.Parse(tc.text, w)
		if in.Slots["exclude"] != "" {
			t.Errorf("%q: exclude=%q num intent sem o slot", tc.text, in.Slots["exclude"])
		}
		for _, d := range tc.drops {
			found := false
			for _, got := range in.Dropped {
				if got == d {
					found = true
				}
			}
			if !found {
				t.Errorf("%q: %q devia estar em dropped=%v", tc.text, d, in.Dropped)
			}
		}
	}

	// o operando da cláusula nunca vira nome-alvo (o oposto do pedido)
	for _, text := range []string{
		"find all files except those named foo",
		"find files except alpha, beta and gamma",
	} {
		if in := m.Parse(text, w); in.Slots["name"] != "" {
			t.Errorf("%q: name=%q — operando de exclusão virou alvo", text, in.Slots["name"])
		}
	}
}

// Copy é um file_move com op=copy — a fonte fica preservada (cp -r em
// vez de rename). O verbo nunca pode entrar no target ("target:copy"
// rodaria rename no literal), nem pronomes/ext-size ("them", "jpgs",
// "1MB"): alvo só vale com evidência de nome, e o resto vira filtro.
func TestNLUCopyGuards(t *testing.T) {
	m, w := nuTestWorld(t)

	for _, tc := range []struct {
		text, intent string
		check        func(map[string]string) bool
	}{
		{`copy all png files into assets`, "file_move",
			func(s map[string]string) bool {
				return s["op"] == "copy" && s["pat"] == "*.png" &&
					s["dest"] == "assets" && s["target"] == ""
			}},
		{`copia os arquivos txt para a pasta docs`, "file_move",
			func(s map[string]string) bool {
				return s["op"] == "copy" && s["pat"] == "*.txt" && s["dest"] == "docs"
			}},
		{`copy file a.txt to b.txt`, "file_move",
			func(s map[string]string) bool {
				return s["op"] == "copy" && s["target"] == "a.txt" && s["dest"] == "b.txt"
			}},
		{`copy dir_b to dir_a`, "file_move",
			func(s map[string]string) bool {
				return s["op"] == "copy" && s["target"] == "dir_b" && s["dest"] == "dir_a"
			}},
		{`copy all .txt files except secret.txt to backup`, "file_move",
			func(s map[string]string) bool {
				return s["op"] == "copy" && s["exclude"] == "secret.txt" &&
					s["pat"] == "*.txt"
			}},
		{`copy the folder src to backup/src`, "file_move",
			func(s map[string]string) bool {
				return s["op"] == "copy" && s["target"] == "src" && s["type"] == "dir"
			}},
		{`copy files larger than 1MB to big`, "file_move",
			func(s map[string]string) bool {
				return s["target"] == "" && s["op"] == "copy"
			}},
		{`move todos os jpgs pra pasta images`, "file_move",
			func(s map[string]string) bool {
				return s["op"] == "rename" && s["pat"] == "*.jpg" && s["target"] == ""
			}},
		{`move file report.pdf to documents`, "file_move",
			func(s map[string]string) bool {
				return s["op"] == "rename" && s["target"] == "report.pdf" &&
					s["dest"] == "documents"
			}},
		{`Move all files excluding hidden files to the subdirectory test`, "file_move",
			func(s map[string]string) bool {
				return s["exclude"] == ".*" && s["dest"] == "test" && s["op"] == "rename"
			}},
		{`find and copy all log files in the current directory tree to /tmp/x`, "file_move",
			func(s map[string]string) bool {
				return s["op"] == "copy" && s["pat"] == "*.log" && s["target"] == ""
			}},
	} {
		in := m.Parse(tc.text, w)
		if in.Name != tc.intent || !tc.check(in.Slots) {
			t.Errorf("%q -> %s slots=%v", tc.text, in.Name, in.Slots)
		}
	}
}

// "(case insensitive)" é flag de match, nunca operando: "case" não pode
// colar em name/dir (delete_file name:case deletaria o literal), e o
// valor flui pro slot icase dos scripts. "less than N days" é
// comparativo — o slang folda pra "menos" e não pode abrir cláusula de
// exceção (exclude:days roubava a unidade).
func TestNLUIcaseGuards(t *testing.T) {
	m, w := nuTestWorld(t)

	for _, tc := range []struct {
		text, intent string
		check        func(map[string]string) bool
	}{
		{`Search for 'string' case insensitively in all files under current directory tree`, "text_grep",
			func(s map[string]string) bool {
				return s["icase"] == "1" && s["pattern"] == "string"
			}},
		{`Delete all 1US* (case insensitive) files under current directory`, "delete_file",
			func(s map[string]string) bool {
				return s["name"] == "1US*" && s["icase"] == "1"
			}},
		{`Delete all files under '/home/backups' directory tree with '.tgz' or '.gz' extension (case insensitive)`, "delete_file",
			func(s map[string]string) bool {
				return s["name"] == "" && s["ext"] == "tgz,gz" && s["icase"] == "1"
			}},
		{`Find all *.jpg (case-insensitive) files under maximum 2 levels down the temp paths`, "file_find",
			func(s map[string]string) bool {
				return s["icase"] == "1" && s["max_depth"] == "2" &&
					s["name"] != "maximum levels temp"
			}},
		{`Find files containing 'blah' in their names modified less than 2 days ago, case insensitive`, "file_find",
			func(s map[string]string) bool {
				return s["exclude"] == "" && s["newer"] == "2880" &&
					s["icase"] == "1" && s["namepat"] == "*blah*"
			}},
		{`Search for files/directories with the case insensitive pattern anaconda.* in var/log directory`, "file_find",
			func(s map[string]string) bool {
				return s["name_re"] == "anaconda.*" && s["dir"] == "var/log" && s["icase"] == "1"
			}},
		{`Replace all occurrences of foo (case insensitive) with bar in all *.css files`, "text_replace",
			func(s map[string]string) bool {
				return s["icase"] == "true" && s["from"] == "foo" && s["to"] == "bar"
			}},
		{`find files matching '*.TXT' case sensitive`, "file_find",
			func(s map[string]string) bool {
				return s["icase"] == "sensitive"
			}},
		{`find all *.txt files`, "file_find",
			func(s map[string]string) bool { return s["icase"] == "" }},
	} {
		in := m.Parse(tc.text, w)
		if in.Name != tc.intent || !tc.check(in.Slots) {
			t.Errorf("%q -> %s slots=%v", tc.text, in.Name, in.Slots)
		}
	}
}

// Janelas temporais: a polaridade e o bound certo, nunca o inverso e
// nunca a frase virando operando de nome/dir.
func TestNLUDateWindowGuards(t *testing.T) {
	m, w := nuTestWorld(t)
	for _, c := range []struct {
		text string
		want map[string]string
		miss []string
	}{
		// "not modified in the last X" é o complemento — older, nunca
		// newer, e sem exclusão inventada
		{"find all files not modified in the last 24 hours",
			map[string]string{"older": "1440"}, []string{"newer", "exclude"}},
		{"find files modified within the past 24 hours",
			map[string]string{"newer": "1440"}, []string{"older"}},
		{"delete all files modified more than 7 days ago",
			map[string]string{"older": "10080"}, []string{"newer"}},
		{"find files modified yesterday",
			map[string]string{"newer": "2880", "older": "1440"}, nil},
		// "except those modified today" = fora da banda de hoje
		{"find files except those modified today",
			map[string]string{"older": "1440"}, []string{"exclude", "newer"}},
		{"find files accessed in the last hour",
			map[string]string{"newer": "60", "tfield": "atime"}, nil},
		{"find files changed in the last 5 minutes",
			map[string]string{"newer": "5", "tfield": "ctime"}, nil},
		{"search for files modified more than an hour ago",
			map[string]string{"older": "60"}, []string{"name"}},
		{"find files that were modified in the last fortnight",
			map[string]string{"newer": "20160"}, []string{"name"}},
		{"find files modified in March 2007",
			map[string]string{"newer_date": "2007-03-01",
				"older_date": "2007-04-01"}, []string{"dir", "name"}},
	} {
		in := m.Parse(c.text, w)
		for k, v := range c.want {
			if in.Slots[k] != v {
				t.Errorf("%q slots %s=%q want %q (%v)", c.text, k,
					in.Slots[k], v, in.Slots)
			}
		}
		for _, k := range c.miss {
			if in.Slots[k] != "" {
				t.Errorf("%q slots %s=%q devia estar vazio (%v)",
					c.text, k, in.Slots[k], in.Slots)
			}
		}
		if len(in.Dropped) > 0 {
			t.Errorf("%q dropped=%v — operando temporal evaporou",
				c.text, in.Dropped)
		}
	}
}

// Datas absolutas: ISO, nomes de mês com sufixo ordinal e o bound
// inclusivo do "between A and B" (~ no teto).
func TestNLUDateBoundGuards(t *testing.T) {
	m, w := nuTestWorld(t)
	for _, c := range []struct {
		text       string
		newer, old string
	}{
		{"find files modified between 2014-08-25 and 2014-08-26",
			"2014-08-25", "2014-08-26~"},
		{"find files modified between August 10th and August 17th",
			"cur-08-10", "cur-08-17~"},
		{"find files modified after February 1 of the current year",
			"cur-02-01", ""},
	} {
		in := m.Parse(c.text, w)
		if in.Slots["newer_date"] != c.newer {
			t.Errorf("%q newer_date=%q want %q", c.text,
				in.Slots["newer_date"], c.newer)
		}
		if in.Slots["older_date"] != c.old {
			t.Errorf("%q older_date=%q want %q", c.text,
				in.Slots["older_date"], c.old)
		}
		if in.Slots["name"] != "" || in.Slots["dir"] != "" {
			t.Errorf("%q frase de data virou operando: %v", c.text, in.Slots)
		}
	}
}

// date_show: nome de dia, epoch e offset com sinal.
func TestNLUDateShowGuards(t *testing.T) {
	m, w := nuTestWorld(t)
	for _, c := range []struct {
		text string
		want map[string]string
	}{
		{"print yesterday's date", map[string]string{"when": "yesterday"}},
		{"show the date for tomorrow + 2 days + 10 minutes",
			map[string]string{"when": "tomorrow", "offset": "2890"}},
		{"print the date 3 days ago", map[string]string{"offset": "-4320"}},
		{"convert epoch second 1267619929 to date",
			map[string]string{"epoch": "1267619929"}},
	} {
		in := m.Parse(c.text, w)
		if in.Name != "date_show" {
			t.Errorf("%q intent=%q want date_show", c.text, in.Name)
			continue
		}
		for k, v := range c.want {
			if in.Slots[k] != v {
				t.Errorf("%q slots %s=%q want %q (%v)", c.text, k,
					in.Slots[k], v, in.Slots)
			}
		}
	}
}

// perm cluster: verbo/meta não vira alvo, modo simbólico multi-grupo,
// chown-of, classe depois de 'permission', e mkdir+chmod composto.
func TestNLUPermClusterGuards(t *testing.T) {
	m, w := nuTestWorld(t)
	// verbos de mutação e substantivos de permissão nunca são arquivo
	if in := m.Parse("Find all files under current directory and change their permission to 600", w); in.Slots["file"] == "change" || in.Slots["mode"] != "600" {
		t.Errorf("change-permission mass: %v", in.Slots)
	}
	if in := m.Parse("Change the owner of tmp_file_upload to nobody", w); in.Slots["file"] != "tmp_file_upload" || in.Slots["owner"] != "nobody" {
		t.Errorf("chown-of: %v", in.Slots)
	}
	if in := m.Parse("Set the permissions of all directories inside the current directory tree to ug=rwx,o=", w); in.Name != "perm_set" || in.Slots["symbolic"] != "ug=rwx,o=" {
		t.Errorf("symbolic multi-group: %s %v", in.Name, in.Slots)
	}
	if in := m.Parse("Set permissions to ug=rw,o= for files inside the tree", w); in.Name != "perm_set" || in.Slots["symbolic"] == "" {
		t.Errorf("symbolic set: %s %v", in.Name, in.Slots)
	}
	// "not have read permission to all" = bit 4 ausente nas 3 classes
	if in := m.Parse("find all files that do not have read permission to all", w); in.Slots["not_bits"] != "444" {
		t.Errorf("not-read-all: %v", in.Slots)
	}
	// "create directory X with permissions N" = mkdir+chmod, não archive
	if in := m.Parse("create directory dirname with permissions 777", w); in.Name != "create" || in.Slots["target"] != "dirname" || in.Slots["mode"] != "777" {
		t.Errorf("mkdir+mode: %s %v", in.Name, in.Slots)
	}
	// enumeração de dirs não engole 'permissions' nem o octal
	if in := m.Parse("create directory dirname with permissions 777", w); in.Slots["targets"] != "" {
		t.Errorf("targets engoliu perm/mode: %v", in.Slots)
	}
}

// ext/glob cluster: multi-dir enumeração vira "a|b" (nunca name:), e
// "user's home directory" resolve pra ~ sem inventar owner/dir.
func TestNLUDirEnumGuards(t *testing.T) {
	m, w := nuTestWorld(t)
	in := m.Parse("Find all *.gz files under asia and emea directory and print their names", w)
	if in.Slots["dir"] != "asia|emea" || in.Slots["name"] != "" {
		t.Errorf("asia+emea: %v", in.Slots)
	}
	in = m.Parse("Find all .txt files in the user's home directory and below", w)
	if in.Slots["dir"] != "~" || in.Slots["owner"] == "home" || in.Slots["dir"] == "user" {
		t.Errorf("user home: %v", in.Slots)
	}
	// "under asia and print" — o 'and' que liga outra ação não é enum
	in = m.Parse("find files under asia and print them", w)
	if in.Slots["dir"] == "asia|print" || in.Slots["name"] == "print" {
		t.Errorf("and-ação: %v", in.Slots)
	}
}

// ext/glob cluster 2: padrões de letra (first/last letter) viram glob
// ancorado, meta-words de domínio não viram name, glob≠regex.
func TestNLUExtGlobGuards(t *testing.T) {
	m, w := nuTestWorld(t)
	// "first letter e or f and last one x" → [ef]*x — o 'e' nu é
	// filler PT mas aqui é operando; o ultimo não vira sort:mtime.
	in := m.Parse("find all files with the first letter e or f and last one x", w)
	if in.Name != "file_find" || in.Slots["name"] != "[ef]*x" || in.Slots["sort"] != "" {
		t.Errorf("first/last letter: %s %v", in.Name, in.Slots)
	}
	in = m.Parse("find all files with the first letter 'e' or 'f' and last one 'x'", w)
	if in.Slots["name"] != "[ef]*x" {
		t.Errorf("quoted letters: %v", in.Slots)
	}
	// meta-word de domínio não é nome literal
	in = m.Parse("find files matching regex '^test.*\\.go$'", w)
	if in.Slots["name_re"] == "" || in.Slots["name"] != "" {
		t.Errorf("regex cue: %v", in.Slots)
	}
	in = m.Parse("find files with names matching the regular expression 'a+'", w)
	if in.Slots["name_re"] != "a+" || in.Slots["name"] != "" {
		t.Errorf("regular expression: %v", in.Slots)
	}
	// glob nunca é regex: '*.php~' e '*.rb' são ext/glob
	in = m.Parse("find all files with extension '*.php~'", w)
	if in.Slots["ext"] != "php~" || in.Slots["name_re"] != "" {
		t.Errorf("php-tilde ext: %v", in.Slots)
	}
	// "spaces in names" → glob com espaço, não file_move
	in = m.Parse("find all files that contain spaces in their names", w)
	if in.Name != "file_find" || in.Slots["name"] != "* *" {
		t.Errorf("spaces in names: %s %v", in.Name, in.Slots)
	}
	// "regular files" = type f, não regex
	in = m.Parse("find all regular files in the folder pathfolder which are 2 levels deep", w)
	if in.Slots["type"] != "file" || in.Slots["max_depth"] != "2" || in.Slots["name_re"] != "" {
		t.Errorf("regular files: %v", in.Slots)
	}
	// lista de exts C/CPP/Header → c,cpp,h
	in = m.Parse("display all the C, CPP, Header files in the kat folder", w)
	if in.Slots["ext"] != "c,cpp,h" || in.Slots["dir"] != "kat" {
		t.Errorf("c,cpp,h: %v", in.Slots)
	}
	// regressão: "named letter" ainda é nome literal; "last 3" ainda é limit
	in = m.Parse("find files named letter", w)
	if in.Slots["name"] != "letter" {
		t.Errorf("named letter: %v", in.Slots)
	}
	in = m.Parse("list the last 3 files", w)
	if in.Slots["limit"] != "3" || in.Slots["name"] != "" {
		t.Errorf("last 3: %v", in.Slots)
	}
}

// depth/dir+sort cluster: particípio de ordenação e escopo de
// filesystem nunca são path; quoted operand vence sponge-compound.
func TestNLUSortDirGuards(t *testing.T) {
	m, w := nuTestWorld(t)
	// "sorted by X" é cláusula de sort — nunca dir:sorted/dir:number
	in := m.Parse("find all files in current directory sorted by size", w)
	if in.Slots["dir"] != "" || in.Slots["sort"] != "size" {
		t.Errorf("sorted by size: %v", in.Slots)
	}
	in = m.Parse("find files under docs sorted by name", w)
	if in.Slots["dir"] != "docs" || in.Slots["sort"] != "name" {
		t.Errorf("sorted by name: %v", in.Slots)
	}
	in = m.Parse("Prints list of top-level subfolders in a current folder, sorted by number of contained files", w)
	if in.Slots["dir"] == "sorted" || in.Slots["dir"] == "number" ||
		in.Slots["name"] != "" {
		t.Errorf("top-level subfolders: %v", in.Slots)
	}
	// "disk partition" é escopo de filesystem, não dir/name
	in = m.Parse("find all the files which have the size 40 bytes in the current disk partition", w)
	if in.Slots["dir"] == "disk" || in.Slots["name"] == "disk partition" ||
		in.Slots["min_size"] != "40" {
		t.Errorf("disk partition: %v", in.Slots)
	}
	// quoted operand vence sponge-compound de palavra-atributo
	in = m.Parse("List all files with name \"someFile\" and their modification time under the current directory tree, sorted by name", w)
	if in.Slots["name"] != "someFile" || len(in.Dropped) > 0 {
		t.Errorf("someFile: %v dropped=%v", in.Slots, in.Dropped)
	}
	// cued path ainda extrai palavras que o sponge bloqueia
	for _, word := range []string{"list", "time", "count", "level"} {
		in = m.Parse("find files named "+word, w)
		if in.Slots["name"] != word {
			t.Errorf("named %s: %v", word, in.Slots)
		}
	}
}

// line/count cluster: aspa após prep de escopo é alvo (nunca padrão),
// "of X" alimenta o file do slice, ordinal de campo não é path.
func TestNLULineCountGuards(t *testing.T) {
	m, w := nuTestWorld(t)
	// "count 'x' chars in 'filename'" — o padrão é x, o alvo é o file;
	// operandos nunca trocam de papel
	in := m.Parse("Count the number of \"x\" characters in \"filename\"", w)
	if in.Slots["pattern"] != "x" || in.Slots["file"] != "filename" ||
		in.Slots["namepat"] != "" {
		t.Errorf("count chars: %v", in.Slots)
	}
	// "search 'foo' in 'bar.txt'" — 'bar' é escopo, não forquoted
	in = m.Parse("search files for \"foo\" in \"bar.txt\"", w)
	if in.Slots["pattern"] != "foo" || in.Slots["file"] != "bar.txt" {
		t.Errorf("grep scope: %v", in.Slots)
	}
	// "of syslog" alimenta o file do slice — syslog é nome de log
	in = m.Parse("tail the last 50 lines of syslog", w)
	if in.Slots["file"] != "syslog" || in.Slots["tail"] != "50" {
		t.Errorf("tail of: %v", in.Slots)
	}
	in = m.Parse("show lines 5 to 20 of data.txt", w)
	if in.Slots["start"] != "5" || in.Slots["end"] != "20" || in.Slots["file"] != "data.txt" {
		t.Errorf("slice range: %v", in.Slots)
	}
	// ordinal de campo não é path — "the first" no merge não é dir
	in = m.Parse("Merge files 'text.txt' and 'codes.txt' by outputting any lines whose second field in the first matches the third field in the second", w)
	if in.Slots["dir"] == "first" || in.Slots["dir"] == "second" {
		t.Errorf("field ordinal: %v", in.Slots)
	}
	// path com $VAR e barra é dir, não file
	in = m.Parse("search for the word \"slrn\" in all the files in the folder $HOME/html/andrews-corner", w)
	if in.Slots["dir"] != "$HOME/html/andrews-corner" || in.Slots["file"] != "" {
		t.Errorf("$HOME dir: %v", in.Slots)
	}
}

func TestNLUArchiveGuards(t *testing.T) {
	m, w := nuTestWorld(t)
	// "with 'bzip2'" é formato/ferramenta, nunca fonte nem out; a aspa de
	// destino (outtgt) vence o cue de ferramenta
	in := m.Parse("Compress the file 'file' with 'bzip2' and append all output to the file 'logfile' and stdout", w)
	if in.Slots["out"] != "logfile" || in.Slots["srcs"] == "bzip2" ||
		(in.Slots["srcs"] != "" && in.Slots["srcs"] != "file") {
		t.Errorf("bzip2 roles: %v", in.Slots)
	}
	// pattern cue consome a cue-word — 'filepattern' é o valor do pat,
	// nunca uma fonte
	in = m.Parse("Update the archive '2009.tar' with the files from the data/ directory tree that match pattern 'filepattern'", w)
	if in.Slots["pat"] != "filepattern" || in.Slots["out"] != "2009.tar" {
		t.Errorf("arc pat: %v", in.Slots)
	}
	for _, bad := range []string{"filepattern", "pattern", "tree"} {
		if in.Slots["srcs"] == bad {
			t.Errorf("srcs roubou %q: %v", bad, in.Slots)
		}
	}
	// "from X" introduz a fonte direto
	in = m.Parse("create archive backup.tar.gz from src", w)
	if in.Slots["out"] != "backup.tar.gz" || in.Slots["srcs"] != "src" {
		t.Errorf("from src: %v", in.Slots)
	}
	// "user's home" é escopo ~ — nunca owner nem dir literal; ".tar-gz"
	// composta é ext/nome, nunca dir
	in = m.Parse("Search the current user's home directory for any file that ends in .tar-gz", w)
	if in.Name != "file_find" || in.Slots["dir"] != "~" ||
		in.Slots["owner"] != "" || in.Slots["dir"] == ".tar-gz" {
		t.Errorf("user home tar-gz: %s %v", in.Name, in.Slots)
	}
	// "ends in tar-gz" sem ponto também vira filtro de nome/ext
	in = m.Parse("find files that end in tar-gz", w)
	if in.Slots["dir"] == "tar-gz" {
		t.Errorf("tar-gz dir: %v", in.Slots)
	}
}

func TestNLUHiddenDotGuards(t *testing.T) {
	m, w := nuTestWorld(t)
	// "hidden files"/"arquivos ocultos" — o conjunto dos dotfiles vira
	// glob ".*" no nome (PT põe o adjetivo depois do substantivo)
	for _, s := range []string{"find hidden files", "lista arquivos ocultos"} {
		in := m.Parse(s, w)
		if in.Slots["name"] != ".*" && in.Slots["namepat"] != ".*" {
			t.Errorf("%q: %s %v", s, in.Name, in.Slots)
		}
	}
	// mass-delete de hidden exige o filtro ".*" + dir do escopo — sem o
	// nome o each deletaria o diretório inteiro (catastrófico); o $VAR é
	// scope, nunca target
	in := m.Parse("Delete all hidden files under $some_directory", w)
	if in.Name != "delete_file" || in.Slots["name"] != ".*" ||
		in.Slots["dir"] != "$some_directory" || in.Slots["target"] != "" {
		t.Errorf("delete hidden: %s %v", in.Name, in.Slots)
	}
	// "excluding all hidden files" — a exclusão é o glob ".*", nunca o
	// name do conjunto (polaridade); 'all' é ponte dentro da cláusula
	in = m.Parse("find all the normal/regular files in the folder \"pathfolder\" which are 2 levels deep, excluding all hidden files and directories", w)
	if in.Slots["exclude"] != ".*" || in.Slots["name"] != "" ||
		in.Slots["dir"] != "pathfolder" {
		t.Errorf("excluding hidden: %v", in.Slots)
	}
	// phrasing de filtro/formato nunca vira dir/name: "including hidden
	// ones"/"long listing"/"normal-regular" são descrição, não operandos
	in = m.Parse("Filters only directories including hidden ones from long file listing of a current directory, and prints their names.", w)
	if in.Slots["dir"] == "including" || in.Slots["name"] == "long" ||
		in.Slots["name"] == "*normal/regular*" {
		t.Errorf("filter phrasing: %v", in.Slots)
	}
}

func TestNLUSizeGuards(t *testing.T) {
	m, w := nuTestWorld(t)
	// "empty X" é predicado (empty:true), nunca name "empty"
	in := m.Parse("Find all empty directories under /tmp", w)
	if in.Slots["empty"] != "true" || in.Slots["name"] == "empty" ||
		in.Slots["dir"] != "/tmp" {
		t.Errorf("empty dirs: %v", in.Slots)
	}
	// "greater than 1MB and move them" — o filtro de tamanho sobrevive
	// ao move; sem ele o each moveria TUDO (silent-wrong catastrófico)
	in = m.Parse("Find all files in the current directory tree whose size is greater than 1MB, and move them to the \"files\" folder", w)
	if in.Name != "file_move" || in.Slots["min_size"] == "" ||
		in.Slots["dest"] != "files" {
		t.Errorf("move size filter: %s %v", in.Name, in.Slots)
	}
	// "search X folder for files larger than N" — 'for' é escopo, não
	// grep; 'Movies' é nome real (typo-repair não pode virar 'moves');
	// user's home + subpasta vira "~/Movies"
	in = m.Parse("Search the Movies folder in the user's home directory for files larger than 1024 megabytes in size", w)
	if in.Name != "file_find" || in.Slots["dir"] != "~/Movies" ||
		in.Slots["min_size"] == "" || in.Slots["sort"] != "" {
		t.Errorf("movies home: %s %v", in.Name, in.Slots)
	}
	// comparativo com 'than' é predicado de tamanho, não sort
	in = m.Parse("display all the files in the home folder which are smaller than 500 bytes", w)
	if in.Slots["sort"] == "size" || in.Slots["max_size"] == "" {
		t.Errorf("smaller than: %v", in.Slots)
	}
	// superlativo PT sem comparação é sort=size
	in = m.Parse("lista os maiores arquivos", w)
	if in.Slots["sort"] != "size" {
		t.Errorf("maiores: %v", in.Slots)
	}
}

func TestNLUNameExactGuards(t *testing.T) {
	m, w := nuTestWorld(t)
	// "named X under PATH" — o path depois de under é dir, nunca target;
	// delete precisa de name+dir+mass com confiança executável (them é
	// anáfora/filler, não palavra solta)
	in := m.Parse("Find directories named 'work' under '/usr/ports/' directory tree and remove them", w)
	if in.Name != "delete_file" || in.Slots["dir"] != "/usr/ports/" ||
		in.Slots["name"] != "work" || in.Slots["target"] != "" ||
		in.Slots["mass"] == "" || in.Confidence < 0.6 {
		t.Errorf("named under remove: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	in = m.Parse("Find files named 'core' in or below the directory /tmp and delete them", w)
	if in.Name != "delete_file" || in.Slots["dir"] != "/tmp" ||
		in.Slots["name"] != "core" || in.Confidence < 0.6 {
		t.Errorf("named delete: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// "doesn't contain a file named 'bin'" — contenção estrutural NÃO é
	// busca de conteúdo nem filtro de nome; 'bin' é o item contido e o
	// pedido fica honestamente sem suporte (conf baixa → recusa)
	in = m.Parse("Find all directories under /home that doesn't contain a file/directory named 'bin'", w)
	if in.Slots["name"] == "bin" || in.Slots["pattern"] == "bin" ||
		(in.Name == "text_grep" && in.Confidence >= 0.6) {
		t.Errorf("contain named: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// "entrie filesystem" (typo) — o escopo é a raiz; a palavra defeituosa
	// antes de filesystem é phrasing, nunca dir
	in = m.Parse("Find all files/directories named 'apt' in the entrie filesystem", w)
	if in.Slots["dir"] != "/" || in.Slots["name"] != "apt" {
		t.Errorf("entrie filesystem: %v", in.Slots)
	}
	// "specfied directory" — adjetivo de escopo vago é phrasing; o glob
	// não absorve o typo ("*blast* specfied" seria nome inexistente)
	in = m.Parse("Find file names *blast* in specfied directory", w)
	if in.Slots["name"] != "*blast*" || in.Slots["dir"] == "specfied" {
		t.Errorf("specfied: %v", in.Slots)
	}
	// "under 'Books' ... located in user's home" — dir relativo +
	// cláusula de home compõe "~/Books"
	in = m.Parse("Find all files/directories named 'Waldo' under 'Books' directory tree that is located in user's home directory", w)
	if in.Slots["dir"] != "~/Books" || in.Slots["name"] != "Waldo" {
		t.Errorf("waldo home: %v", in.Slots)
	}
	// "under / directory" — a raiz citada é dir, não target
	in = m.Parse("Find all directories named postgis-2.0.0 under / directory", w)
	if in.Slots["dir"] != "/" || in.Slots["name"] != "postgis-2.0.0" {
		t.Errorf("postgis: %v", in.Slots)
	}
}

func TestNLUSymlinkGuards(t *testing.T) {
	m, w := nuTestWorld(t)
	// criação básica: "named X to Y" — X é o link NOVO, Y o alvo
	in := m.Parse(`Create a symbolic link named "mylink" to "target.txt"`, w)
	if in.Name != "link_create" || in.Slots["link"] != "mylink" ||
		in.Slots["target"] != "target.txt" || in.Confidence < 0.6 {
		t.Errorf("link create: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// "to file X" — 'file' é substantivo-ponte, não parte do alvo
	in = m.Parse(`create a symbolic link named "www" to file "www1"`, w)
	if in.Name != "link_create" || in.Slots["link"] != "www" ||
		in.Slots["target"] != "www1" || in.Confidence < 0.6 {
		t.Errorf("to file: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// "in directory D named X to Y" — compõe link=D/X
	in = m.Parse(`create a symbolic link in directory "dest" named "lk" to "src/f"`, w)
	if in.Name != "link_create" || in.Slots["link"] != "dest/lk" ||
		in.Slots["target"] != "src/f" || in.Confidence < 0.6 {
		t.Errorf("dest compose: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// hard link separado: "hard" + "link" → hard=true (os.Link, não symlink)
	in = m.Parse(`create a hard link named "h" to "orig"`, w)
	if in.Name != "link_create" || in.Slots["hard"] != "true" ||
		in.Slots["link"] != "h" || in.Slots["target"] != "orig" ||
		in.Confidence < 0.6 {
		t.Errorf("hard link: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// "broken symlinks" = type:link AND orphan:true — o substantivo-link
	// é os dois predicados; sem type:link o find retornaria qq órfão
	in = m.Parse(`Find all broken symlinks under /tmp`, w)
	if in.Name != "file_find" || in.Slots["orphan"] != "true" ||
		in.Slots["type"] != "link" || in.Slots["dir"] != "/tmp" ||
		in.Confidence < 0.6 {
		t.Errorf("broken find: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// "broken and cyclic" — segundo qualificador não vaza pra name
	in = m.Parse(`List all broken symlinks including cyclic links under current directory`, w)
	if in.Name != "file_find" || in.Slots["orphan"] != "true" ||
		in.Slots["type"] != "link" || in.Slots["name"] != "" {
		t.Errorf("cyclic: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// "X directory tree" é phrasing de escopo — o type do delete é o
	// link (orphan), nunca dir; type:dir apagaria diretórios inteiros
	in = m.Parse(`Remove all broken symlinks from the /usr/ports/packages directory tree`, w)
	if in.Name != "delete_file" || in.Slots["type"] != "link" ||
		in.Slots["orphan"] != "true" ||
		in.Slots["dir"] != "/usr/ports/packages" {
		t.Errorf("broken delete: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// "following symlinks" → follow=true; 'if needed' é phrasing
	in = m.Parse(`Find all files/directories under /myfiles following symlinks if needed`, w)
	if in.Name != "file_find" || in.Slots["follow"] != "true" ||
		in.Slots["dir"] != "/myfiles" || in.Slots["name"] != "" {
		t.Errorf("follow: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// "exactly 2 links" — contagem de hardlinks, não tipo
	in = m.Parse(`search in root ( / ) directory downwards all files which have exactly 2 links`, w)
	if in.Name != "file_find" || in.Slots["links"] != "2" ||
		in.Slots["type"] == "link" {
		t.Errorf("links count: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// "pointing to anywhere within X" — restrição por alvo do link não
	// é predicado suportado: o operando cai no Dropped e derruba a
	// confiança (superset de todos os links seria falso sucesso)
	in = m.Parse(`Search for symlinks pointing to anywhere within /mnt/oldname/`, w)
	if len(in.Dropped) == 0 || in.Confidence > 0.6 {
		t.Errorf("pointing: %s %.2f %v dropped=%v",
			in.Name, in.Confidence, in.Slots, in.Dropped)
	}
	// "same name" não é cue de nome — mass-link sem path/target fica
	// honestamente missing, nunca inventa link:directory
	in = m.Parse(`Create symlinks to all /home/folder1/*.txt files with the same name in current directory`, w)
	if in.Slots["link"] == "directory" || in.Slots["link"] == "name" {
		t.Errorf("same name: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
}

func TestNLUExecGuards(t *testing.T) {
	m, w := nuTestWorld(t)
	// "count duplicate lines" — wordcount_stats não tem dup; contar
	// TODAS as linhas seria sucesso falso. O qualificador vai pro
	// Dropped e derruba a confiança (recusa honesta).
	in := m.Parse(`Print a count of duplicate lines in "filename"`, w)
	if nuPredict(in) != "none" || len(in.Dropped) == 0 {
		t.Errorf("duplicate: %s %.2f %v dropped=%v",
			nuPredict(in), in.Confidence, in.Slots, in.Dropped)
	}
	// "no later than one minute ago" ≈ -mmin -1 — palavra-número e
	// o comparativo 'later than' alimentam o slot newer; "file
	// system" (par colado pelo tokenizer) é a raiz
	in = m.Parse(`Print a list of all the files in entire file system whose inode has been modified no later than one minute ago`, w)
	if in.Name != "file_find" || in.Slots["dir"] != "/" ||
		in.Slots["newer"] != "1" || in.Slots["name"] != "" {
		t.Errorf("no later than: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// "user $1 belongs to group $2" — membership de usuário não é
	// consulta de permissão de arquivo: recusa, nunca perm_find
	in = m.Parse(`Returns 0 if user $1 belongs to group $2`, w)
	if nuPredict(in) == "perm_find" || len(in.Dropped) == 0 {
		t.Errorf("membership: %s %.2f %v dropped=%v",
			nuPredict(in), in.Confidence, in.Slots, in.Dropped)
	}
	in = m.Parse(`check whether user bob is a member of group wheel`, w)
	if nuPredict(in) == "perm_find" || len(in.Dropped) == 0 {
		t.Errorf("member of: %s %.2f %v dropped=%v",
			nuPredict(in), in.Confidence, in.Slots, in.Dropped)
	}
	// "loop over find results" — controle de fluxo não suportado; a
	// query colada não é operando de busca
	in = m.Parse(`loop over the results of a find command`, w)
	if nuPredict(in) == "find" || len(in.Dropped) == 0 {
		t.Errorf("loop: %s %.2f %v dropped=%v",
			nuPredict(in), in.Confidence, in.Slots, in.Dropped)
	}
	// "for each ... print an mv command" — geração de comandos não é
	// um move: fica sem dest e cai abaixo do corte, nunca executa
	in = m.Parse(`Find all directories and for each of them, print an mv command to move it to /new/location`, w)
	if nuPredict(in) == "file_move" {
		t.Errorf("for-each mv: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// package-ownership ("which package owns X") não é permissão de
	// arquivo — o nome do pacote cai no Dropped
	in = m.Parse(`Print package information the command "ldconfig" belongs to in rpm`, w)
	if nuPredict(in) == "perm_find" || len(in.Dropped) == 0 {
		t.Errorf("package: %s %.2f %v dropped=%v",
			nuPredict(in), in.Confidence, in.Slots, in.Dropped)
	}
}

func TestNLUSortFirstGuards(t *testing.T) {
	m, w := nuTestWorld(t)
	// "top-level subfolders" — o objeto pedido é dir; "files" em
	// "contained files" é a métrica do sort, não o tipo
	in := m.Parse(`Prints list of top-level subfolders in a current folder, sorted by number of contained files`, w)
	if in.Name != "file_find" || in.Slots["type"] != "dir" {
		t.Errorf("subfolders: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// "all but 5 newest ones" — keep-N-newest não é limit+sort numa
	// busca qualquer: o superlativo cai no Dropped (recusa honesta)
	in = m.Parse(`Removes all files from current folder but 5 newest ones, ignoring folders in a files list`, w)
	if nuPredict(in) != "none" || len(in.Dropped) == 0 ||
		in.Slots["name"] == "Removes" {
		t.Errorf("keep newest: %s %.2f %v dropped=%v",
			nuPredict(in), in.Confidence, in.Slots, in.Dropped)
	}
	// "sorted by oldest modified to newest" — superlativo é campo
	// mtime; o nome citado sobrevive como predicado
	in = m.Parse(`List all files with name "someFile" and their modification time under the current directory sorted by oldest modified to newest`, w)
	if in.Name != "file_find" || in.Slots["name"] != "someFile" ||
		in.Slots["sort"] != "mtime" {
		t.Errorf("oldest sort: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// "365*24 hours" — o produto escrito na frase avalia pra janela;
	// a negação "not modified" flipa pra older
	in = m.Parse(`display all the files in the home folder which have not been modified in the last 365*24 hours`, w)
	if in.Name != "file_find" || in.Slots["dir"] != "~" ||
		in.Slots["older"] != "525600" || in.Slots["name"] != "" {
		t.Errorf("365*24: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// "sort strings by second field" — sort -k não é line_slice;
	// o campo cai no Dropped
	in = m.Parse(`Sort strings of 'test.txt' file by second from the end field`, w)
	if nuPredict(in) == "line_slice" || len(in.Dropped) == 0 {
		t.Errorf("sort -k: %s %.2f %v dropped=%v",
			nuPredict(in), in.Confidence, in.Slots, in.Dropped)
	}
}

func TestNLUContentGrepGuards(t *testing.T) {
	m, w := nuTestWorld(t)
	// "search for X in all files" — palavra nua de conteúdo entre
	// "for" e o escopo é pattern, nunca nome de arquivo
	in := m.Parse(`Search for aaa in all files under current directory and count the number of matches`, w)
	if in.Slots["name"] == "aaa" || in.Slots["pattern"] != "aaa" {
		t.Errorf("unquoted pattern: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// "${var}" — o } de fechamento é parte da var, não pontuação
	in = m.Parse(`Find all files under ${searchpath} that match the regex 'x' in their contents`, w)
	if in.Slots["dir"] == "${searchpath" {
		t.Errorf("brace var: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// "in their contents" — a palavra de domínio colada como name é
	// dropada: nome-regex em file_find seria a busca errada
	in = m.Parse(`Find all files under ${searchpath} that match the regex '"${s1}".*"${s2}"' in their contents`, w)
	if len(in.Dropped) == 0 {
		t.Errorf("contents: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// "under 'dir'" — preposição de escopo exclusiva: a aspa é o
	// diretório de busca, nunca o arquivo único
	in = m.Parse(`Recursively finds strings with "text" in files under 'directory-path'`, w)
	if in.Slots["dir"] != "directory-path" || in.Slots["file"] != "" {
		t.Errorf("under dir: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// "$DIR"/"'/path/'" no slot de arquivo único migram pra dir —
	// a var se declara pasta e o / final é forma de diretório
	in = m.Parse(`Search for $S in all regular files under $DIR directory tree`, w)
	if in.Slots["dir"] != "$DIR" || in.Slots["file"] != "" {
		t.Errorf("$DIR scope: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	in = m.Parse(`Finds recursively all files in '/path/' that contain 'p'`, w)
	if in.Slots["file"] == "/path/" {
		t.Errorf("slash dir: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// "anaconda.*" é regex (.* = qualquer-coisa), não glob — migra
	// pro name_re; "[ef]" charclass segue glob no name
	in = m.Parse(`Search for files/directories with the case insensitive pattern anaconda.* in var/log directory`, w)
	if in.Slots["name_re"] != "anaconda.*" || in.Slots["name"] == "anaconda.*" {
		t.Errorf("regex name: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// "atleast" colado é quantificador de profundidade, nunca nome
	in = m.Parse(`find all the text files in the folder /home/calvin which are atleast below 2 levels`, w)
	if in.Slots["name"] == "atleast" || in.Slots["max_depth"] != "2" {
		t.Errorf("atleast: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
}

// Guards do cluster recent/time: direção correta ("last N"→newer,
// "ago"→older), superlativo de campo não é janela, palavras-número e
// produtos ("365*24") são quantidade — nunca nome.
func TestNLURecentTimeGuards(t *testing.T) {
	m, w := nuTestWorld(t)
	in := m.Parse(`find files that were modified in the last fortnight`, w)
	if in.Slots["newer"] != "20160" {
		t.Errorf("fortnight: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// "not modified in the last 365*24 hours" — produto avalia e a
	// negação da janela inverte a direção (older, não newer)
	in = m.Parse(`display all the files in the home folder which have not been modified in the last 365*24 hours`, w)
	if in.Slots["older"] != "525600" || in.Slots["dir"] != "~" || in.Slots["name"] != "" {
		t.Errorf("prod window: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// "last accessed more than 7 days" — 'last' antes de campo é
	// superlativo (atime), não janela recente
	in = m.Parse(`Find any files that were last accessed more than 7 days and are larger than 100MB`, w)
	if in.Slots["tfield"] != "atime" || in.Slots["older"] != "10080" || in.Slots["newer"] != "" {
		t.Errorf("last accessed: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// palavra-número e 'ago' simples: direção older
	in = m.Parse(`find files in /u/bill directory which were accessed about 2 to 6 minutes ago`, w)
	if in.Slots["tfield"] != "atime" || in.Slots["dir"] != "/u/bill" {
		t.Errorf("accessed ago: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// "less than 60 minutes ago" — 'ago' não inverte a direção
	// já marcada pelo 'less than'
	in = m.Parse(`Find all files under /home/foo/public_html/ that were modified less than 60 minutes ago`, w)
	if in.Slots["newer"] != "60" || in.Slots["older"] != "" {
		t.Errorf("less-ago: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// "no later than one minute ago" — palavra-número, direção newer,
	// 'file system' two-word → raiz
	in = m.Parse(`find in the entire file system inode modified no later than one minute ago`, w)
	if in.Slots["newer"] != "1" || in.Slots["dir"] != "/" || in.Slots["name"] != "" {
		t.Errorf("no-later: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// "exactly 10*24 hours ago" — faixa exata [N,N+unit): older=N
	in = m.Parse(`search for all mp3 files in the folder /home/you which have been accessed exactly 10*24 hours ago`, w)
	if in.Slots["older"] != "14400" || in.Slots["ext"] != "mp3" || in.Slots["name"] != "" {
		t.Errorf("exact ago: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
}

// TestNLUFind2Guards — silent-wrongs do cluster find2 do corpus:
// operandos especiais (uid numérico, fstype, delimitador de saída,
// flags), escopo/destino com 'under', exceções com verbos de locação
// e aspa citada, vars dir-shaped e non-recursive.
func TestNLUFind2Guards(t *testing.T) {
	m, w := nuTestWorld(t)
	// uid numérico é predicado próprio (find -uid), não nome de owner
	in := m.Parse(`find all files in the /var/tmp directory tree with uid=1000`, w)
	if in.Name != "file_find" || in.Slots["uid"] != "1000" || in.Slots["dir"] != "/var/tmp" {
		t.Errorf("uid: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// "read-write access for others" — bit composto → máscara rw
	in = m.Parse(`find all files under /myfiles with read-write access for others`, w)
	if in.Name != "perm_find" || in.Slots["bits"] == "" || in.Slots["name"] != "" {
		t.Errorf("rw-others: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// "do not belong to any user" — órfão sem dono
	in = m.Parse(`find all files in the file system which do not belong to any user`, w)
	if in.Name != "perm_find" || in.Slots["orphan"] == "" || in.Slots["dir"] != "/" {
		t.Errorf("any-user orphan: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// "not currently stored in ./pdf" — 'stored' é ponte; a exclusão
	// é o path depois de 'in'
	in = m.Parse(`find all pdf files in the current directory that are not currently stored in the ./pdf`, w)
	if in.Name != "file_find" || in.Slots["exclude"] != "./pdf" {
		t.Errorf("stored-in exclude: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// 'under' antes do verbo move é ESCOPO; depois pode ser destino
	in = m.Parse(`find all *.csv files under /foot/bar/ and move them to some_dir`, w)
	if in.Name != "file_move" || in.Slots["srcdir"] != "/foot/bar/" || in.Slots["dest"] != "some_dir" {
		t.Errorf("under-scope: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// aspa citada dentro de cláusula de exceção é o operando excluído
	in = m.Parse(`search the current directory tree for regular files omitting directory 'omit-directory'`, w)
	if in.Name != "file_find" || in.Slots["exclude"] != "omit-directory" {
		t.Errorf("exctgt: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// "$dir directory (non-recursive)" — var auto-descritiva + depth 1
	in = m.Parse(`find all files in $dir directory (non-recursive) and count them`, w)
	if in.Slots["dir"] != "$dir" || in.Slots["max_depth"] != "1" {
		t.Errorf("dir-var nonrec: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// glob real vence cola de phrasing; delimitador de saída não é
	// suportado → o glob fica e a cláusula cai pro Dropped
	in = m.Parse(`find all *company* (case-insensitive) files/directories under /basedir with null character as the delimiter`, w)
	if in.Slots["name"] != "*company*" && in.Slots["namepat"] != "*company*" {
		t.Errorf("company glob: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	if in.Slots["name"] == "null character delimiter" {
		t.Errorf("delimiter virou name: %v", in.Slots)
	}
	// "look for directory 'cookbook'" — a aspa é o nome da pasta
	in = m.Parse(`look for directory 'cookbook'`, w)
	if in.Name != "file_find" || in.Slots["name"] != "cookbook" || in.Slots["type"] != "dir" {
		t.Errorf("cookbook: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// valor com cara de flag ('-tcp') não é nome de arquivo
	in = m.Parse(`output two lines of "-tcp"`, w)
	if in.Slots["name"] == "-tcp" {
		t.Errorf("flag-name: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// restrição de fstype ("only in jfs and ext2") não é suportada —
	// não pode vazar como owner nem filtrar silenciosamente
	in = m.Parse(`find all files in the file system which do not belong to any user and search only in jfs and ext2 filesystems`, w)
	if in.Slots["not_owner"] == "jfs" || in.Slots["not_owner"] == "search" {
		t.Errorf("fstype-owner: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// 'under' depois do verbo continua sendo destino
	in = m.Parse(`move all regular files under current directory to ./newdir`, w)
	if in.Name != "file_move" || in.Slots["dest"] != "./newdir" {
		t.Errorf("under-dest: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
}

// Batch2 do find2: exec-por-item, contagem-interrogativa, target de
// escopo no create e predicado temporal no perm_find.
func TestNLUFind2Batch2Guards(t *testing.T) {
	m, w := nuTestWorld(t)
	// fan-out por arquivo é segunda operação — o find não executa
	in := m.Parse(`Find all *.txt files/directories under current directory and execute process for each of them`, w)
	if nuPredict(in) != "none" || len(in.Dropped) == 0 {
		t.Errorf("exec-fanout: %s %.2f %v dropped=%v", in.Name, in.Confidence, in.Slots, in.Dropped)
	}
	// "find every X" sozinho NÃO é fan-out — 'every' sem verbo de
	// execução continua quantificador de escopo
	in = m.Parse(`find every log file over 10MB`, w)
	if in.Name != "file_find" || in.Slots["ext"] != "log" || in.Slots["min_size"] == "" {
		t.Errorf("every-scope: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// pergunta de contagem sobre conjunto — o find lista, não conta
	in = m.Parse(`how many python files are in this repo`, w)
	if nuPredict(in) != "none" || len(in.Dropped) == 0 {
		t.Errorf("how-many: %s %.2f %v dropped=%v", in.Name, in.Confidence, in.Slots, in.Dropped)
	}
	// path depois de cue de escopo não é o objeto criado
	in = m.Parse(`Create thumbnails from the first page of each PDF files under /path/to/dir`, w)
	if in.Slots["target"] == "/path/to/dir" || nuPredict(in) == "create" {
		t.Errorf("scope-target: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// perm_find ganhou predicado temporal — dono negado + janela
	in = m.Parse(`display all the files in the file system which do not belong to the user "wnj" and which are modified more than 30 days ago`, w)
	if in.Name != "perm_find" || in.Slots["not_owner"] != "wnj" ||
		in.Slots["older"] != "43200" || len(in.Dropped) > 0 {
		t.Errorf("perm-time: %s %.2f %v dropped=%v", in.Name, in.Confidence, in.Slots, in.Dropped)
	}
	// superlativo segue sem suporte no perm_find (sem sort+limit)
	in = m.Parse(`find files owned by root modified newest`, w)
	if in.Name == "perm_find" && len(in.Dropped) == 0 {
		t.Errorf("perm-superlative: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// "convert epoch to date" é conversão SUPORTADA pelo date_show
	in = m.Parse(`convert epoch second 1267619929 to date`, w)
	if in.Name != "date_show" || len(in.Dropped) > 0 {
		t.Errorf("epoch-convert: %s %.2f %v dropped=%v", in.Name, in.Confidence, in.Slots, in.Dropped)
	}
	// named-cue reivindica palavra com conceito ("named count")
	in = m.Parse(`find files named count`, w)
	if in.Slots["name"] != "count" {
		t.Errorf("named-count: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	// 'letter' literal depois de named não vira glob
	in = m.Parse(`find files named letter`, w)
	if in.Slots["name"] != "letter" {
		t.Errorf("named-letter: %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
}

// Mutation-routing de permissões (find2 mining): "set their permission"
// é perm_set com cobertura total (mass+mode+depth+recursive+type);
// "permission set for everybody" é particípio — FILTRO (perm_find),
// nunca mutação. E dir-word sob verbo de mutação é o ALVO (type:dir),
// não localização.
func TestNLUPermMutationGuards(t *testing.T) {
	m, w := nuTestWorld(t)

	for _, c := range []struct {
		text string
		want map[string]string
	}{
		{"Find all directories under maximum 1 level down the current directory and set their permission to 700 recursively",
			map[string]string{"mass": "1", "mode": "700", "max_depth": "1", "recursive": "true", "type": "dir"}},
		{"Find all files/directories under minimum 2 level down the current directory and set their permission to 7",
			map[string]string{"mass": "1", "mode": "7", "min_depth": "2"}},
		{"Find all files under current directory and change their permission to 600",
			map[string]string{"mass": "1", "mode": "600"}},
		{"set the permission of report.txt to 644",
			map[string]string{"file": "report.txt", "mode": "644"}},
		{"chmod 755 in all directories under /srv",
			map[string]string{"dir": "/srv", "mass": "1", "mode": "755", "type": "dir"}},
		{"chmod 755 on all directories",
			map[string]string{"mass": "1", "mode": "755", "type": "dir"}},
	} {
		in := m.Parse(c.text, w)
		if in.Name != "perm_set" || nuPredict(in) != "perm_set" {
			t.Errorf("perm_set miss: %s -> %s %.2f %v", c.text, in.Name, in.Confidence, in.Slots)
			continue
		}
		for k, v := range c.want {
			if in.Slots[k] != v {
				t.Errorf("perm_set slot: %s -> %s=%q want %q (%v)", c.text, k, in.Slots[k], v, in.Slots)
			}
		}
		if in.Slots["file"] == "maximum" || in.Slots["file"] == "set" ||
			in.Slots["file"] == "execute" || in.Slots["file"] == "read" {
			t.Errorf("perm_set file-lixo: %s -> %v", c.text, in.Slots)
		}
	}

	// particípio "set for X" é FILTRO de busca, nunca chmod
	for _, c := range []struct {
		text string
		slot string
	}{
		{"Search the current directory tree for directories with the execute permission set for everybody", "exall"},
		{"Search the current directory recursively for regular files with the read permission set for everybody", "aread"},
		{"display all the files which have read permission to the user", "uread"},
		{"Search the current directory tree for directories lacking execute permissions for user, group, or others", "nx"},
	} {
		in := m.Parse(c.text, w)
		if in.Name != "perm_find" || in.Slots[c.slot] == "" {
			t.Errorf("perm_find particípio: %s -> %s %.2f %v", c.text, in.Name, in.Confidence, in.Slots)
		}
		if in.Name == "perm_set" || in.Slots["file"] != "" {
			t.Errorf("busca virou mutação: %s -> %s %v", c.text, in.Name, in.Slots)
		}
	}
}

// Predicados find2 (mining): cue-vocábulo ("the word 'x'") é papel
// sintático — nunca operando nem drop; "named loop" é basename, não
// controle de fluxo; "writable" sem escopo-mundo não vira ww;
// "not broken" link = not_orphan; "not empty" dir = not_empty.
func TestNLUPredicateCueGuards(t *testing.T) {
	m, w := nuTestWorld(t)

	for _, c := range []struct {
		text   string
		intent string
		want   map[string]string
		absent []string
	}{
		{"search for files containing the word 'fuse' in their name in the current directory",
			"file_find", map[string]string{"name": "fuse"}, nil},
		{"find all the files starting with the word 'data'",
			"file_find", map[string]string{"name": "data*"}, nil},
		{"find files named loop",
			"file_find", map[string]string{"name": "loop"}, nil},
		{"find files named count",
			"file_find", map[string]string{"name": "count"}, nil},
		{"find all writable files",
			"perm_find", map[string]string{"writable": "true"}, []string{"ww", "bits"}},
		{"find all world-writable files",
			"perm_find", map[string]string{"ww": "true"}, nil},
		{"find files writable by the group",
			"perm_find", map[string]string{"bits": "020"}, []string{"writable", "ww"}},
		{"find files writable by everybody",
			"perm_find", map[string]string{"bits": "222"}, []string{"writable"}},
		{"find all files in the /path/to/dir directory which are not read-only",
			"perm_find", map[string]string{"writable": "true"}, []string{"ww"}},
		{"find all files which are read-only",
			"perm_find", map[string]string{"not_writable": "true"}, nil},
		{"find all symbolic links in the current directory which are not broken",
			"file_find", map[string]string{"type": "link", "not_orphan": "true"}, nil},
		{"Find the directories in the current directory that are not empty",
			"file_find", map[string]string{"type": "dir", "not_empty": "true"}, []string{"empty", "exclude"}},
		{"find all files in the directory 'docs' which do not have write permission for group",
			"perm_find", map[string]string{"dir": "docs", "not_bits": "020"}, nil},
	} {
		in := m.Parse(c.text, w)
		if in.Name != c.intent {
			t.Errorf("intent: %s -> %s %.2f %v (want %s)", c.text, in.Name, in.Confidence, in.Slots, c.intent)
			continue
		}
		for k, v := range c.want {
			if in.Slots[k] != v {
				t.Errorf("slot: %s -> %s=%q want %q (%v)", c.text, k, in.Slots[k], v, in.Slots)
			}
		}
		for _, k := range c.absent {
			if in.Slots[k] != "" {
				t.Errorf("slot extra: %s -> %s=%q (%v)", c.text, k, in.Slots[k], in.Slots)
			}
		}
		for _, d := range in.Dropped {
			// cue-vocábulos puros ("word", "string") nunca são drop —
			// é papel sintático já coberto pelo padrão extraído
			switch d {
			case "word", "string", "term", "name", "the", "a":
				t.Errorf("cue-vocábulo dropado: %s -> dropped=%v", c.text, in.Dropped)
			}
		}
	}
}

// Corpus mining (redo-v20): verbo/meta-vocábulo nunca é operando —
// "set read permission for group" é g+r simbólico; "aluno1's files"
// é src_owner possessivo; "by user X" não tem dir; unidade/meta
// (KiB/Plus/input) nunca é pasta; descriptor computado ("system IP")
// e verbo de aritmética recusam honestos.
func TestNLUCorpusMineGuards(t *testing.T) {
	m, w := nuTestWorld(t)

	for _, c := range []struct {
		text   string
		intent string
		want   map[string]string
		absent []string
	}{
		{"Find all files/directories under $1 which have at least read permission for their owner and set read permission for group for these files/directories",
			"perm_set", map[string]string{"mass": "1", "symread": "g+r"}, []string{"file", "owner"}},
		{"Change the ownership of all aluno1's files in the current directory and below to aluno2",
			"perm_set", map[string]string{"src_owner": "aluno1", "owner": "aluno2"}, []string{"file"}},
		{"Search the system for files and directories owned by user `admin'",
			"perm_find", map[string]string{"owner": "admin"}, []string{"dir"}},
		{"arquivos do usuário matheus",
			"perm_find", map[string]string{"owner": "matheus"}, []string{"dir"}},
		{"search the word NEEDLE and substitute it with REPLACEMENT in all the php files of the current folder",
			"text_replace", map[string]string{"from": "NEEDLE", "to": "REPLACEMENT", "ext": "php"}, []string{"file"}},
		{"Search the Movies folder in the user's home directory for files larger than 1024 megabytes in size",
			"file_find", map[string]string{"dir": "~/Movies"}, []string{"name"}},
		{"find  file which case-insensitive name is too in currect directory",
			"file_find", map[string]string{"name": "too", "icase": "1"}, []string{"dir"}},
		// 'the pattern REGEX' — o vocábulo é phrasing do cue; o
		// operando é só o literal citado
		{"find all java files in the current folder and search for the pattern REGEX",
			"find", map[string]string{"query": "REGEX", "ext": "java"}, []string{"file"}},
	} {
		in := m.Parse(c.text, w)
		if in.Name != c.intent || nuPredict(in) != c.intent {
			t.Errorf("intent: %s -> %s %.2f %v (want %s)", c.text, in.Name, in.Confidence, in.Slots, c.intent)
			continue
		}
		for k, v := range c.want {
			if in.Slots[k] != v {
				t.Errorf("slot: %s -> %s=%q want %q (%v)", c.text, k, in.Slots[k], v, in.Slots)
			}
		}
		for _, k := range c.absent {
			if in.Slots[k] != "" {
				t.Errorf("slot extra: %s -> %s=%q (%v)", c.text, k, in.Slots[k], in.Slots)
			}
		}
	}

	// disk_usage é a rota certa pro df — 'disk' veta list_files e a
	// unidade de exibição ("1 KiB blocks") nunca vira pasta
	for _, text := range []string{
		"Print file system disk space usage in 1 KiB blocks of the current directory's file system",
	} {
		in := m.Parse(text, w)
		if in.Name != "disk_usage" {
			t.Errorf("want disk_usage: %s -> %s %.2f %v", text, in.Name, in.Confidence, in.Slots)
		}
		if in.Slots["path"] == "KiB" {
			t.Errorf("unidade vazou pro path: %v", in.Slots)
		}
	}

	// recusas honestas — o pedido não é executável, nunca um slot
	// inventado a 0.9
	for _, text := range []string{
		`Replace "dummyvalue" with the system IP address in "filename"`,
		"Go into the directory whose name is selected in the X window system, ie. double-clicked in a terminal window.",
		"Creates temporary file in a TMPDIR folder with name like tmp.XXXXXXXXXX.",
		`Mathematically sum each line in "infile"`,
		// var de shell em slot de padrão/nome = literal silencioso;
		// em path-slot o brPath recusa, no padrão é o drop que empurra
		`Search for $S in all regular files under $DIR directory tree`,
		`Search for 'Processed Files' in all $srch* (case insensitive) files under current directory`,
		`Search for 'stuff' in all *,txt files under current directory`,
		`grep from bottom of file`,
		`Count the number of files in the directory trees whose pathnames match pattern '/dev/sd*[a-z]'`,
		`Display the number of sub-directories for all directories under current directory tree, sort them according to the decreasing order of the number and show only the first 10 of them`,
		`Find number of different rows between File1 and File2.`,
		// sentença aspada não é basename nem pasta — é payload de
		// echo/test, e a variável nunca resolve no offline
		`Print "$somedir is empty" if $somedir is empty`,
		`Print 'Empty dir' if $some_dir is empty`,
		// comando entre aspas no slot de nome — exec não roda
		`Find all files in ~/clang+llvm-3.3/bin/ and print 'basename /file/path' for each file`,
		`Reports time consumed by command 'sleep 1' to the file time.txt together with command error output.`,
		// phrasing multi-palavra não-aspado nunca é basename —
		// "name of lines" casaria o literal, vacuoso
		`Count the number of lines in all ".php" files in the current directory tree`,
		`Find all *.c files on the system and feed the output to wc`,
		`Find all files under /path and calculate their md5sum`,
		// quantificador/descritor de papel nunca é operando
		`Updates all software in a system, skipping packages that have a failed dependencies.`,
		`Look for files in the current directory tree to which the group users have full access`,
		`Print all group names known to the system.`,
		`Find regular files in the current directory tree that have executable bits set for the user and group but not for the other`,
		`search for the text file "file.txt" and display its parent directory`,
		`Change directory to the alphabetically last path under the current directory`,
		`Find "file.xml" under the current directory and change directory to its parent`,
		`Move all files that contain "Subject: \[SPAM\]" to "your_file"`,
		`Find all *.sql files in maximum 1 level down the current directory, process it with sed and then send the output to a mysql command`,
	} {
		in := m.Parse(text, w)
		if nuPredict(in) != "none" {
			t.Errorf("should refuse: %s -> %s %.2f %v", text, in.Name, in.Confidence, in.Slots)
		}
	}

	// perm_set sem payload (sem mode/owner/group/sym*) não executa
	// chmod nenhum — o único método aplicável é o 'ask' que reporta
	// needs_input. Cobertura de slot não salva mutação sem alvo.
	lib, err := mhLibrary()
	if err != nil {
		t.Skip("methods: ", err)
	}
	for _, text := range []string{
		"change the permission of all php files in current folder. Plus symbol at the end is used to give multiple files as input to the chmod command",
		"change permissions recursively on build/",
	} {
		in := m.Parse(text, w)
		if in.Name != "perm_set" {
			t.Fatalf("payload-less chmod deve rotear perm_set: %s -> %s", text, in.Name)
		}
		facts := map[string]string{}
		for k, v := range in.Slots {
			facts["slot."+k] = v
		}
		var applicable []string
		for _, mt := range lib["perm_set"] {
			if mhApplicable(mt, facts) {
				applicable = append(applicable, mt.Name)
			}
		}
		if len(applicable) != 1 || applicable[0] != "perm_set.ask" {
			t.Errorf("sem payload só 'ask' pode aplicar: %s -> %v", text, applicable)
		}
	}
}

// owner/group: a forma "user:group" e a enumeração "to user and
// group X" alimentam OS DOIS slots — um slot só é cobertura parcial
// (chown mudaria dono mas não grupo, ou vice-versa). text_replace
// com URL/descrição em from/to não é literal — recusa honesta.
func TestNLUOwnerGroupGuards(t *testing.T) {
	m, w := nuTestWorld(t)

	for _, c := range []struct {
		text   string
		intent string
		want   map[string]string
	}{
		{"Change the user and group of all files and directories under /var/www to www-data:www-data",
			"perm_set", map[string]string{"owner": "www-data", "group": "www-data"}},
		{"change owner and group of the all files with group owner vboxusers into home directory to user and group kent",
			"perm_set", map[string]string{"owner": "kent", "group": "kent", "src_group": "vboxusers"}},
		{"recursively change owner and group of the directory and all files into it to user and group dev_user",
			"perm_set", map[string]string{"owner": "dev_user", "group": "dev_user"}},
		{"recursively change owner of the directory ~/.npm to the current user",
			"perm_set", map[string]string{"dir": "~/.npm"}},
	} {
		in := m.Parse(c.text, w)
		if in.Name != c.intent || nuPredict(in) != c.intent {
			t.Errorf("intent: %s -> %s %.2f %v (want %s)", c.text, in.Name, in.Confidence, in.Slots, c.intent)
			continue
		}
		for k, v := range c.want {
			if in.Slots[k] != v {
				t.Errorf("slot: %s -> %s=%q want %q (%v)", c.text, k, in.Slots[k], v, in.Slots)
			}
		}
	}
	// "current user" resolve pro USER do ambiente, nunca literal
	in := m.Parse("recursively change owner of the directory ~/.npm to the current user", w)
	if in.Slots["owner"] == "" || in.Slots["owner"] == "current" || in.Slots["owner"] == "user" {
		t.Errorf("current user: %v", in.Slots)
	}

	for _, text := range []string{
		`Download "https://s3.amazonaws.com/sampletest/sample.tar.gz", unzip it, and untar it replacing filenames with "old" to "new"`,
		`Print "This is a sentence." by replacing all consecutive space characters with a single newline character`,
	} {
		in := m.Parse(text, w)
		if nuPredict(in) != "none" {
			t.Errorf("should refuse: %s -> %s %.2f %v", text, in.Name, in.Confidence, in.Slots)
		}
	}
}

// su/sudo: impersonação não é suportada — "login as user X" nunca é
// status de serviço nem sessão; o nome-alvo vai pro Dropped e a
// confiança cai abaixo do gate em vez de fingir a troca de conta.
// removeword roteia delete_file com os filtros do file_find;
// namelen comparativo, vocábulo de exibição em owner/group, name
// com forma de caminho e "excluding <classe>" contraditório também
// viram recusa honesta.
func TestNLUSysClusterGuards(t *testing.T) {
	m, w := nuTestWorld(t)

	// executáveis corretos
	for _, c := range []struct {
		text   string
		intent string
		want   map[string]string
	}{
		{"Removes all empty folders with modification time more that 10 minutes ago from $homeDirData folder.",
			"delete_file", map[string]string{"dir": "$homeDirData", "empty": "true", "older": "10", "type": "dir"}},
		{"display the directory name along with the modified time for all the files /var",
			"file_find", map[string]string{"dir": "/var"}},
		{"display all files in the current folder which do not belong to the user john",
			"perm_find", map[string]string{"not_owner": "john", "type": "file"}},
		{"Gets string with MAC address of eth0 network interface.",
			"net_ifaces", map[string]string{"name": "eth0"}},
		{"files named list", "file_find", map[string]string{"name": "list"}},
		// whitespace aspado é literal; aspa de escopo vira file
		{"Replace all ' ' with '-' from standard input",
			"text_replace", map[string]string{"from": " ", "to": "-", "stdin": "1"}},
		{`Replace "," with "\r\n" in "a,b"`,
			"text_replace", map[string]string{"file": "a,b", "from": ",", "to": "\\r\\n"}},
		{"Replace spaces in file names with underscores for all files in the current directory tree",
			"text_replace", map[string]string{"from": "spaces", "to": "underscores", "namefile": "1"}},
		// pasta nua após preposição de escopo; classe+prep = alvo
		{"Print out the names of all directories in mydir recursively",
			"list_files", map[string]string{"dir": "mydir", "type": "dir"}},
		{"display all the directories in the folder master-",
			"list_files", map[string]string{"dir": "master-", "type": "dir"}},
		// "links that point to nothing" = links órfãos
		{"Find links that point to nothing",
			"file_find", map[string]string{"orphan": "true", "type": "link"}},
	} {
		in := m.Parse(c.text, w)
		if in.Name != c.intent || nuPredict(in) != c.intent {
			t.Errorf("intent: %s -> %s %.2f %v (want %s)", c.text, in.Name, in.Confidence, in.Slots, c.intent)
			continue
		}
		for k, v := range c.want {
			if in.Slots[k] != v {
				t.Errorf("slot: %s -> %s=%q want %q (%v)", c.text, k, in.Slots[k], v, in.Slots)
			}
		}
	}

	// recusas honestas
	for _, text := range []string{
		"login as user postgres",
		"switch user to jenkins",
		"run ksh shell as user apache",
		"simulate a full login of user root",
		"get the root user access",
		"login as user root",
		"Search the file system for regular files whose names are shorter than 25 characters",
		"Display permissions, user, group, and full path for each file in the current directory tree as a list",
		`display all the files ending with ".user" in /var/adm/logs/morelogs/ and excluding all regular files`,
		`run command "cd /home/$USERNAME/$PROJECT ; svn update" as user named as value of the variable $USERNAME`,
		`Set variable OS to the name of the operating system, ie. "Linux"`,
		"Finds users with X session in system and puts the result in USERS variable.",
		"display a long ilsting of all the files in the file system which are bigger than 1KB and which have not been modified in the last 30*24 hours",
		`display all the files ending with ".user" or beginning with "admin" or ending with ".user.gz" in /var/adm/logs/morelogs/ and excluding all regular files`,
		// verbo-comando nunca é basename; duração nunca é limit
		"sleep for 5 seconds",
		// faixa enumerada não é fan-out fixa do create
		"make directories a, b, c, ..., z",
		"make directories for each line in folder_list.txt",
		// arquivo como container de nomes (comm/join) não é dir
		"list the names that are present in file foo",
		// predicados/escopo não viram operando de mutação
		"search for text files in the current folder which have write access to others",
		"Remove the last file extension from standard input",
		"Remove all files with the .c extension in the current directory tree",
		`Print file type of the executable file of command "file"`,
		"Change group ownership to `foo' for files with GID=2000",
		"change the ownership of all regular/normal files in the current directory after users confirmation",
		"Find all PDF files in the current directory and its sub-directories and move them to ./pdfs",
		"Remove all files from the current directory tree whose names contain whitespaces",
		"replace a word in all the regular/normal files in the current folder",
	} {
		in := m.Parse(text, w)
		if nuPredict(in) != "none" {
			t.Errorf("should refuse: %s -> %s %.2f %v", text, in.Name, in.Confidence, in.Slots)
		}
	}
}

// Pacote dep/sys/read: phrasing de gerenciador ("npm install X",
// "o pacote X") alimenta dep, não o nome do gerenciador; variável
// SCREAMING junto a cue de var alimenta env_get.name; consulta de
// pacotes instalados não é instalação; glob solto / spec de formato
// / histórico de shell / impersonação / var-name vão pro Dropped.
func TestNLUDepSysGuards(t *testing.T) {
	m, w := nuTestWorld(t)

	for _, c := range []struct {
		text   string
		intent string
		want   map[string]string
	}{
		{"pip install flask", "install_dep", map[string]string{"dep": "flask"}},
		{"npm install requests", "install_dep", map[string]string{"dep": "requests"}},
		{"instala o pacote requests", "install_dep", map[string]string{"dep": "requests"}},
		{"Installs package 'mysql'", "install_dep", map[string]string{"dep": "mysql"}},
		{"Updates 'openssl' package", "update_dep", map[string]string{"dep": "openssl"}},
		{"get the PATH variable", "env_get", map[string]string{"name": "PATH"}},
		{"qual o valor de GOPATH", "env_get", map[string]string{"name": "GOPATH"}},
		{"npm ls", "pkg_installed", map[string]string{}},
		{"quais pacotes estão instalados", "pkg_installed", map[string]string{}},
		{"quais libs tenho instaladas", "pkg_installed", map[string]string{}},
		{"rpm instalados", "pkg_installed", map[string]string{}},
		{"dependências instaladas", "pkg_installed", map[string]string{}},
		{"últimos commits", "git_log", map[string]string{}},
		{"show history", "git_log", map[string]string{}},
		{"histórico de commits", "git_log", map[string]string{}},
		{"pega o campo port do config", "config_get", map[string]string{"target": "config"}},
		{"mostra a chave database.host do config", "config_get", map[string]string{"target": "config"}},
	} {
		in := m.Parse(c.text, w)
		if nuPredict(in) != c.intent {
			t.Errorf("intent: %s -> %s %.2f %v (want %s)", c.text, in.Name, in.Confidence, in.Slots, c.intent)
			continue
		}
		for k, v := range c.want {
			if in.Slots[k] != v {
				t.Errorf("slot: %s -> %s=%q want %q (%v)", c.text, k, in.Slots[k], v, in.Slots)
			}
		}
	}

	// recusas honestas
	for _, text := range []string{
		// histórico de shell ≠ git log
		"Prints top-ten of most used utilities from history",
		"most used commands from history",
		// glob solto sem slot de padrão que o cubra
		"List all *fink* files/directories in entire file system",
		// spec de formato de data não é suportada
		"get year-month-day hour:minute:second from date",
		"convert epoch second timestamp to date",
		// impersonação/switch-user não suportado
		"switch to user username",
		// nome de variável citado não é pacote
		`Create empty files (or update timestamps if they exist) with names matching each word in variable "correctFilePathAndName"`,
		// diagnóstico não é instalação
		"how do i figure out what's wrong with python pip",
	} {
		in := m.Parse(text, w)
		if nuPredict(in) != "none" {
			t.Errorf("should refuse: %s -> %s %.2f %v", text, in.Name, in.Confidence, in.Slots)
		}
	}

	// gerenciador sem pacote: dep é required, o Missing bloqueia a
	// execução honestamente ("instalar o quê?") em vez de rodar vazio
	if in := m.Parse("npm install", w); in.Name != "install_dep" ||
		in.Slots["dep"] != "" || len(in.Missing) == 0 {
		t.Errorf("npm install sem pacote devia dar miss=[dep]: %v miss=%v", in.Slots, in.Missing)
	}
}

// archive/create cluster: ferramenta de compressão não é srcs nem
// nome de out; spec remota, comando-com-flag, var-duração e create
// negado/tamanho/backup-op nunca viram operando executável.
func TestNLUArchiveCreateGuards(t *testing.T) {
	m, w := nuTestWorld(t)

	// rotas e slots certos
	for _, c := range []struct {
		text, intent string
		want         map[string]string
	}{
		{"compacta a pasta logs em tar.gz", "archive_create",
			map[string]string{"srcs": "logs", "out": "tar.gz"}},
		{"Archive the directory structure under .. directory into dirstructure.tar",
			"archive_create", map[string]string{"dir": "..", "out": "dirstructure.tar"}},
		{"Find all *.gz files under asia and emea directory and print their names",
			"file_find", map[string]string{"dir": "asia|emea"}},
		{"View gzip's help documentation", "help", map[string]string{}},
		{"procura ParseArgs", "find",
			map[string]string{"query": "ParseArgs"}},
		{"find files sorted by date", "find",
			map[string]string{"query": "sorted date"}},
		{"encontra a string 'connection refused'", "text_grep",
			map[string]string{"pattern": "connection refused"}},
		// faixa de tamanho posicional — "between X and Y"
		{"Find all .gif files in the /var/www directory tree that are between 5 kB and 10 kB in size",
			"file_find", map[string]string{"ext": "gif",
				"min_size": "5120", "max_size": "10240"}},
		{"Search for files whose size is between 100 kilobytes and 500 kilobytes",
			"file_find", map[string]string{"min_size": "102400",
				"max_size": "512000"}},
		{"Search the current directory recursively for files whose size is between 10 and 50 MB",
			"file_find", map[string]string{"min_size": "10485760",
				"max_size": "52428800"}},
	} {
		in := m.Parse(c.text, w)
		if nuPredict(in) != c.intent {
			t.Errorf("intent: %s -> %s %.2f %v (want %s)", c.text, in.Name, in.Confidence, in.Slots, c.intent)
			continue
		}
		for k, v := range c.want {
			if in.Slots[k] != v {
				t.Errorf("slot: %s -> %s=%q want %q (%v)", c.text, k, in.Slots[k], v, in.Slots)
			}
		}
	}

	// recusas honestas — o operando crítico vai pro Dropped ou o
	// slot required fica Missing; nenhuma executa com slot inventado
	for _, text := range []string{
		// create negado — o pedido é mktemp -u, não criar
		"Prints name of temporary file but doesn`t create nothing.",
		// tamanho não é nome de arquivo
		"create a 100MB test file",
		// backup é operação, não mkdir
		"create a backup of all the files in the home folder on a partition and save the log to a file",
		// enum de phrasing/var não é fan-out
		"display all the directories in specific path ( saved in $x ) excluding those that are present in a path and save the ouput to a file",
		// alternação de ferramenta não é fonte
		`get info about "lbzip2|plzip|pigz" from tar command`,
		// spec remota user@host:path não é dir
		`Archive "<SOURCE_DIR>" to "rsyncuser@192.168.1.110:/srv/www/prj112/myfolder"`,
		// pipe-into-comando não é dir de extract
		`Unzip all files matching "file_*" and pipe into "agrep -dEOE 'grep'"`,
		// var-duração não fabrica janela nem alvo
		"Delete files in $DIR_TO_CLEAN older than $DAYS_TO_SAVE days",
		// escopo "em todos os configs" não é suportado pelo sed
		"substitui localhost por 127.0.0.1 em todos os configs",
		// pedido de man/help não é símbolo do grep de código
		"Show manual page of find",
		"Show the explanation of find's debugging options",
		// número aleatório não é padrão de contagem
		"Print a random number between 1 and 10",
		// jobs/inode/classe não são literal de grep
		"count amount of jobs running",
		"get the job number from the stored PID",
		"Find the file whose inode number is 1316256",
		"count all the html files having a specifc word in a folder.",
		// phrasing de listagem de diretório não é literal
		"List all leaf directories (directories which don't contain any sub-directory) under current directory",
		"display all the files in the current folder which contains form feed (^L) and does not contain NULL",
		// fonte por variável é indireção não resolvida
		`Archive directory specified by variable "myFolder" to current directory.`,
		// diff de diretórios não é grep de símbolo
		"Find .cpp files that differs in subdirectories PATH1 and PATH2.",
		"Find PHP files with abstract classes",
		// função-word/pronome não é operando
		"Find all files you have modified in the last two days",
		"Search for first match of the case insensitive regex 're' in all *.coffee files under current directory",
		"find all files in the file system which belong to no user or which have no user",
		"Find all files & directory which have write permission for group , remove the permission from group",
		"find all the directories in the current folder and change to these directories and run a command",
		// metacaractere não suportado em slot de nome/path
		"Find files/directories named<filetype> under current directory which were accessed less than 5 days ago",
		"Search directories /path/to/directory/folder{1..50} for .txt files",
		"delete all the empty files(files with size 0 bytes) in the current folder",
		"Delete the text matched by the regex '<script>if(window.*<\\/script>' in all index.html files under current directory",
		"Copy directory hierarchy from \"./<SOURCE_DIR>/\" to \"<DEST_DIR>\"",
		"find all the video files in the folder /home/<user>",
		"Rename all *.jpg files to *.jpg$.jpg files under ../<dirname>  directory by appending the parent directory name at the beginning of their names",
		// glob em slot de path/conta
		"find files in the current directory with pattern` '*.[ch]' which are contain ‘thing’ string and print file names",
		"Find all directories under current directory and make them read, write, and executable for owner & group and remove read-write-execute permission for other",
		"Print virtual memory usage of all processes owned by \"jbos[s]\"",
		// path vazado dentro de padrão de extensão
		"Replace all occurrences of edx (case insensitive) with gurukul in all *.css (case insensitive) files under ./cms/djangoapps/contentstore/management/commands/tests directory",
		// quantificador regex em slot glob / aspas tipográficas
		"Search for 'specific string' in all files matching the name pattern '“*.[txt|TXT]”' under current directory tree",
		"Ensure all 5 of UEDP0{1..5}_20120821.csv files exist, creating empty files for any missing ones (updates the file's timestamps)",
	} {
		in := m.Parse(text, w)
		if nuPredict(in) != "none" {
			t.Errorf("should refuse: %s -> %s %.2f %v", text, in.Name, in.Confidence, in.Slots)
		}
	}

	// palavra-ferramenta vira out — o CAP recusa o formato
	for _, text := range []string{
		"create an archive using 'pigz' as a compress program",
		"create an archive using pbzip2 as a compress program",
		"Find all .log files in the /var/log directory tree and compress them using bzip2",
	} {
		in := m.Parse(text, w)
		if in.Name != "archive_create" || !nuArchiveWord(in.Slots["out"]) {
			t.Errorf("tool->out: %s -> %s %v", text, in.Name, in.Slots)
		}
	}
}
