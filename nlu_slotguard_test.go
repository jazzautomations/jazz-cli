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
		"find files inside $DIR":                "$DIR",
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
				return s["name"] == "anaconda.*" && s["dir"] == "var/log" && s["icase"] == "1"
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
