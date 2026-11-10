package main

import (
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// brain_real.go — capacidades e fatos que a suíte real (`jazz eval real`,
// evalreal.go) mostrou faltarem: o pedido era entendido, mas não havia método
// que fizesse o que foi pedido, ou o método respondia outra coisa.

// brRealFacts adds the facts the methods below gate on. Cheap, and only for
// the intent that needs each one.
func brRealFacts(cx *brCtx, f map[string]string) {
	root := cx.env.Root
	switch cx.in.Name {
	case "env_get":
		// a name that is not in the process environment but is a key of the
		// project's .env: "qual o valor de PORT" is about the project
		if name := cx.task.Slots["name"]; name != "" && os.Getenv(name) == "" {
			if _, _, ok := brDotenvLookup(root, name); ok {
				f["dotenv.key"] = "1"
			}
		}
	case "log_errors_recent":
		if cx.task.Slots["file"] == "" {
			var logs []string
			for _, p := range niListWalk(root).files {
				if strings.EqualFold(filepath.Ext(p), ".log") {
					logs = append(logs, p)
				}
			}
			if len(logs) > 0 && len(logs) <= 8 {
				f["project.logs"] = strings.Join(logs, ",")
			}
		}
	case "shell_cmd":
		if c := cx.task.Slots["cmd"]; IsReadOnly(c) || cbReadCmd(c) {
			f["shell.read"] = "1"
		}
	case "format":
		if t := cx.task.Slots["file"]; t != "" {
			if rel, ok := brProjectFile(root, t); ok {
				f["format.file"] = rel
			}
		}
	case "explain":
		if t := cx.task.Slots["target"]; t != "" {
			if rel, ok := brProjectFile(root, t); ok {
				f["explain.file"] = rel
			}
		}
	}
}

// brProjectFile: t names a regular file of the project — the path as given,
// or the one file with that base name.
func brProjectFile(root, t string) (string, bool) {
	if _, rel, err := brSafePath(root, t); err == nil {
		if fi, e := os.Stat(filepath.Join(root, rel)); e == nil && fi.Mode().IsRegular() {
			return filepath.ToSlash(rel), true
		}
	}
	return brBareFile(root, t)
}

// ---------- .env ----------

// brSecretName: the variable holds a credential by its name.
var brSecretName = lazyRx(`(?i)(TOKEN|SECRET|PASSWORD|PASSWD|PASS|PWD|CREDENTIAL|PRIVATE|APIKEY|API_KEY|_KEY$|^KEY$|AUTH|DSN|COOKIE|SESSION)`)

// brDotenvLookup finds name in the project's .env files.
func brDotenvLookup(root, name string) (val, file string, ok bool) {
	for _, fn := range []string{".env", ".env.local", ".env.development"} {
		b, err := os.ReadFile(filepath.Join(root, fn))
		if err != nil || len(b) > 1<<20 {
			continue
		}
		for _, l := range strings.Split(string(b), "\n") {
			l = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "export "))
			if l == "" || strings.HasPrefix(l, "#") {
				continue
			}
			k, v, found := strings.Cut(l, "=")
			if !found || strings.TrimSpace(k) != name {
				continue
			}
			v = strings.TrimSpace(v)
			if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
				v = v[1 : len(v)-1]
			}
			return v, fn, true
		}
	}
	return "", "", false
}

// brDotenvGet answers "qual o valor de X" from the project's .env. A
// credential is confirmed, never printed: the answer goes to the terminal,
// the session log and (online) the model.
func brDotenvGet(cx *brCtx, in map[string]string) brStep {
	name := cx.str(in, "name")
	obs := rvNoCmd()
	val, file, ok := brDotenvLookup(cx.env.Root, name)
	if !ok {
		return brStep{obs: obs, why: name + " não está no ambiente nem no .env do projeto"}
	}
	secret := brSecretName.MatchString(name) || strings.Contains(val, "://") && strings.Contains(val, "@")
	switch {
	case val == "":
		cx.say("%s está em %s, vazio (não está no ambiente do processo)", name, file)
	case secret:
		cx.say("%s está definido em %s: %d caracteres, valor oculto porque é credencial (abra o %s pra ver)", name, file, len(val), file)
	default:
		cx.say("%s=%s (de %s; não está no ambiente do processo)", name, val, file)
	}
	cx.task.AddEvidence("file", fmt.Sprintf("%s: chave %s presente", file, name))
	return brStep{val: name, obs: obs, ok: true}
}

// ---------- erros em arquivo de log ----------

var brLogErrRe = lazyRx(`(?i)\b(error|erro|err|panic|fatal|exception|traceback|fail|failed|failure|falha|falhou|crit|critical)\b`)

// brLogErrorsFile shows the error lines of the project's log files (the one
// named in the request, or the *.log files found).
func brLogErrorsFile(cx *brCtx, in map[string]string) brStep {
	obs := rvNoCmd()
	var files []string
	if f := cx.str(in, "file"); f != "" {
		files = []string{f}
	} else if l := cx.facts["project.logs"]; l != "" {
		files = strings.Split(l, ",")
	}
	if len(files) == 0 {
		return brStep{obs: obs, why: "nenhum arquivo de log no projeto"}
	}
	which := cx.str(in, "which") // primeiro|first|ultimo|last: only that one
	var only string
	total, shown := 0, 0
	for _, rel := range files {
		abs, rel, err := brSafePath(cx.env.Root, rel)
		if err != nil {
			return brStep{obs: obs, why: err.Error()}
		}
		fi, err := os.Stat(abs)
		if err != nil || !fi.Mode().IsRegular() {
			return brStep{obs: obs, why: "não consegui abrir " + rel}
		}
		f, err := os.Open(abs)
		if err != nil {
			return brStep{obs: obs, why: err.Error()}
		}
		buf := make([]byte, min(fi.Size(), brMaxRead))
		n, _ := f.Read(buf)
		f.Close()
		hits := 0
		for i, l := range strings.Split(string(buf[:n]), "\n") {
			if !brLogErrRe.MatchString(l) {
				continue
			}
			hits++
			if which != "" {
				if line := fmt.Sprintf("%s:%d: %s", filepath.ToSlash(rel), i+1, oneLineKeep(l, 240)); only == "" || which == "ultimo" || which == "last" {
					only = line
				}
				continue
			}
			if shown < 40 {
				cx.say("%s:%d: %s", filepath.ToSlash(rel), i+1, oneLineKeep(l, 240))
				shown++
			}
		}
		total += hits
		cx.task.AddEvidence("file", fmt.Sprintf("%s: %d linha(s) de erro", filepath.ToSlash(rel), hits))
	}
	if which != "" {
		if only == "" {
			cx.say("nenhuma linha de erro em %s", strings.Join(files, ", "))
		} else {
			cx.say("%s\n(%s de %d linha(s) de erro em %s)", only, map[bool]string{true: "a última", false: "a primeira"}[which == "ultimo" || which == "last"], total, strings.Join(files, ", "))
		}
		return brStep{val: strconv.Itoa(total), obs: obs, ok: true}
	}
	if total > shown {
		cx.say("… +%d linha(s)", total-shown)
	}
	cx.say("%d linha(s) de erro em %s (logs do projeto; o journal do sistema é outro pedido: \"erros do sistema\")", total, strings.Join(files, ", "))
	return brStep{val: strconv.Itoa(total), obs: obs, ok: true}
}

// ---------- soma de coluna CSV ----------

// brCSVSum sums one column of a CSV file (by header name or 1-based index).
func brCSVSum(cx *brCtx, in map[string]string) brStep {
	obs := rvNoCmd()
	abs, rel, err := brSafePath(cx.env.Root, cx.str(in, "file"))
	if err != nil {
		return brStep{obs: obs, why: err.Error()}
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return brStep{obs: obs, why: "não consegui abrir " + rel + ": " + err.Error()}
	}
	if len(b) > 64<<20 {
		return brStep{obs: obs, why: rel + " é grande demais (> 64 MB)"}
	}
	first, _, _ := strings.Cut(string(b), "\n")
	delim := ','
	for _, d := range []rune{';', '\t', '|'} {
		if strings.Count(first, string(d)) > strings.Count(first, string(delim)) {
			delim = d
		}
	}
	r := csv.NewReader(strings.NewReader(string(b)))
	r.Comma, r.FieldsPerRecord, r.LazyQuotes = delim, -1, true
	rows, err := r.ReadAll()
	if err != nil || len(rows) < 2 {
		return brStep{obs: obs, why: rel + ": não li como CSV com cabeçalho e linhas"}
	}
	op := cx.str(in, "op")
	if op == "count" {
		n := 0
		for _, row := range rows[1:] {
			if len(row) > 1 || len(row) == 1 && strings.TrimSpace(row[0]) != "" {
				n++
			}
		}
		cx.say("%s tem %d linha(s) de dados (sem contar o cabeçalho: %s)", filepath.ToSlash(rel), n, strings.Join(rows[0], ", "))
		cx.task.AddEvidence("calc", fmt.Sprintf("%s: %d linhas de dados", filepath.ToSlash(rel), n))
		return brStep{val: strconv.Itoa(n), obs: obs, ok: true}
	}
	col, want := -1, strings.TrimSpace(cx.str(in, "col"))
	for i, h := range rows[0] {
		if strings.EqualFold(strings.TrimSpace(h), want) {
			col = i
		}
	}
	if n, e := strconv.Atoi(want); col < 0 && e == nil && n >= 1 && n <= len(rows[0]) {
		col = n - 1
	}
	if col < 0 {
		return brStep{obs: obs, why: fmt.Sprintf("%s não tem a coluna %q (colunas: %s)", rel, want, strings.Join(rows[0], ", "))}
	}
	sum, used, skipped := 0.0, 0, 0
	best, bestRow := 0.0, []string(nil)
	for _, row := range rows[1:] {
		if col >= len(row) || strings.TrimSpace(row[col]) == "" {
			skipped++
			continue
		}
		v := strings.TrimSpace(row[col])
		if strings.Contains(v, ",") && !strings.Contains(v, ".") {
			v = strings.Replace(v, ",", ".", 1)
		}
		x, e := strconv.ParseFloat(v, 64)
		if e != nil {
			skipped++
			continue
		}
		sum += x
		used++
		if bestRow == nil || op == "max" && x > best || op == "min" && x < best {
			best, bestRow = x, row
		}
	}
	if used == 0 {
		return brStep{obs: obs, why: fmt.Sprintf("a coluna %s de %s não tem número nenhum", rows[0][col], rel)}
	}
	num := func(x float64) string { return strconv.FormatFloat(math.Round(x*1e6)/1e6, 'f', -1, 64) }
	switch op {
	case "avg":
		cx.say("média de %s em %s: %s (soma %s em %d linha(s), %d sem número)", rows[0][col], filepath.ToSlash(rel), num(sum/float64(used)), num(sum), used, skipped)
		cx.task.AddEvidence("calc", fmt.Sprintf("%s.%s: %d valores", filepath.ToSlash(rel), rows[0][col], used))
		return brStep{val: num(sum / float64(used)), obs: obs, ok: true}
	case "max", "min":
		word := map[string]string{"max": "maior", "min": "menor"}[op]
		var cells []string
		for i, h := range rows[0] {
			if i < len(bestRow) {
				cells = append(cells, strings.TrimSpace(h)+"="+strings.TrimSpace(bestRow[i]))
			}
		}
		cx.say("%s %s em %s: %s — linha: %s (%d linha(s) comparada(s), %d sem número)", word, rows[0][col], filepath.ToSlash(rel), num(best), strings.Join(cells, ", "), used, skipped)
		cx.task.AddEvidence("calc", fmt.Sprintf("%s.%s: %d valores", filepath.ToSlash(rel), rows[0][col], used))
		return brStep{val: num(best), obs: obs, ok: true}
	}
	cx.say("soma de %s em %s: %s (%d linha(s) somada(s), %d sem número)", rows[0][col], filepath.ToSlash(rel),
		num(sum), used, skipped)
	cx.task.AddEvidence("calc", fmt.Sprintf("%s.%s: %d valores", filepath.ToSlash(rel), rows[0][col], used))
	return brStep{val: strconv.FormatFloat(sum, 'f', -1, 64), obs: obs, ok: true}
}

// ---------- listagem com nomes ----------

// brListNames prints the files by directory: "lista os arquivos" wants names,
// and the tree summary only counts them.
func brListNames(cx *brCtx, in map[string]string) brStep {
	obs := rvNoCmd()
	dir := strings.Trim(filepath.ToSlash(cx.str(in, "path")), "/")
	if dir == "." {
		dir = ""
	}
	byDir := map[string][]string{}
	n := 0
	for _, f := range niListWalk(cx.env.Root).files {
		f = filepath.ToSlash(f)
		if dir != "" && !strings.HasPrefix(f, dir+"/") {
			continue
		}
		d := filepath.ToSlash(filepath.Dir(f))
		byDir[d] = append(byDir[d], filepath.Base(f))
		n++
	}
	if n == 0 {
		return brStep{val: "", obs: obs, ok: true}
	}
	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	shown := 0
	for _, d := range dirs {
		names := byDir[d]
		sort.Strings(names)
		if shown+len(names) > 300 {
			cx.say("… +%d arquivo(s) em outras pastas", n-shown)
			break
		}
		label := d + "/"
		if d == "." {
			label = "./"
		}
		cx.say("%s  %s", label, strings.Join(names, "  "))
		shown += len(names)
	}
	cx.task.AddEvidence("file", fmt.Sprintf("%d arquivo(s) listados", n))
	return brStep{val: strconv.Itoa(n), obs: obs, ok: true}
}

// ---------- resumo estrutural de um arquivo ----------

var (
	brExImportBlock = lazyRx(`(?s)\bimport\s*\((.*?)\)`)
	brExImportLine  = lazyRx(`(?m)^\s*(?:import\s+(?:[\w.]+\s+)?"([^"]+)"|import\s+([\w.]+)|from\s+([\w.]+)\s+import|(?:const|let|var|import)\s+.*?(?:require\(|from\s+)['"]([^'"]+)['"]|#include\s+[<"]([^>"]+)[>"]|use\s+([\w:]+))`)
	brExQuoted      = lazyRx(`"([^"]+)"`)
	brExPackage     = lazyRx(`(?m)^\s*(?:package|module|namespace)\s+([\w./-]+)`)
	brExRoute       = lazyRx(`(?:HandleFunc|Handle|\.(?:GET|POST|PUT|PATCH|DELETE|get|post|put|patch|delete|route|Route))\(\s*["']([^"']+)["']`)
	brExMarker      = lazyRx(`\b(TODO|FIXME|HACK|XXX|BUG)\b[:\s]*(.*)`)
)

// brExplainFile gives the structural summary of one file: what it declares,
// imports, serves and leaves marked. It is not an explanation of behaviour —
// that needs a model — and it says so.
func brExplainFile(cx *brCtx, in map[string]string) brStep {
	obs := rvNoCmd()
	rel := cx.facts["explain.file"]
	if rel == "" {
		return brStep{obs: obs, why: "não achei o arquivo pra resumir"}
	}
	abs := filepath.Join(cx.env.Root, filepath.FromSlash(rel))
	fi, err := os.Stat(abs)
	if err != nil {
		return brStep{obs: obs, why: "não consegui abrir " + rel}
	}
	f, err := os.Open(abs)
	if err != nil {
		return brStep{obs: obs, why: err.Error()}
	}
	buf := make([]byte, min(fi.Size(), brMaxRead))
	n, _ := f.Read(buf)
	f.Close()
	src := string(buf[:n])
	lines := strings.Split(src, "\n")
	head := fmt.Sprintf("%s · %d linhas · %d bytes", rel, len(lines), fi.Size())
	if m := brExPackage.FindStringSubmatch(src); m != nil {
		head += " · " + strings.Fields(strings.TrimSpace(m[0]))[0] + " " + m[1]
	}
	cx.say("%s", head)
	seen := map[string]bool{}
	var imports []string
	add := func(s string) {
		if s != "" && !seen[s] && len(imports) < 30 {
			seen[s] = true
			imports = append(imports, s)
		}
	}
	for _, blk := range brExImportBlock.FindAllStringSubmatch(src, 4) {
		for _, q := range brExQuoted.FindAllStringSubmatch(blk[1], -1) {
			add(q[1])
		}
	}
	for _, m := range brExImportLine.FindAllStringSubmatch(src, 60) {
		for _, g := range m[1:] {
			add(g)
		}
	}
	if len(imports) > 0 {
		cx.say("importa: %s", strings.Join(imports, ", "))
	}
	if syms := brSymbols(rel, buf[:n]); len(syms) > 0 {
		if len(syms) > 40 {
			syms = append(syms[:40], fmt.Sprintf("… +%d", len(syms)-40))
		}
		cx.say("declara: %s", strings.Join(syms, ", "))
	}
	var routes []string
	for _, m := range brExRoute.FindAllStringSubmatch(src, 40) {
		if !seen["r:"+m[1]] {
			seen["r:"+m[1]] = true
			routes = append(routes, m[1])
		}
	}
	if len(routes) > 0 {
		cx.say("rotas: %s", strings.Join(routes, ", "))
	}
	marks := 0
	for i, l := range lines {
		if m := brExMarker.FindStringSubmatch(l); m != nil && marks < 8 {
			cx.say("%s na linha %d: %s", m[1], i+1, oneLineKeep(strings.TrimSpace(m[2]), 120))
			marks++
		}
	}
	cx.say("(resumo estrutural, sem modelo: o que o arquivo declara, importa e serve. explicar o comportamento precisa de um modelo — rode sem --offline)")
	cx.task.AddEvidence("file", fmt.Sprintf("%s lido: %d linhas", rel, len(lines)))
	return brStep{val: rel, obs: obs, ok: true}
}

// ---------- formatar ----------

// fpFmtFix is the command that rewrites the files for a detected format
// CHECK command; "" when the check command is all we know.
func fpFmtFix(check string) string {
	switch check {
	case "gofmt -l .":
		return "gofmt -w ."
	case "cargo fmt --check":
		return "cargo fmt"
	case "ruff format --check .":
		return "ruff format ."
	}
	return ""
}

// brFmtCheckOnly: the request asks to check the formatting, not to format.
var brFmtCheckOnly = lazyRx(`(?i)\b(checa|checar|cheque|verifica|verificar|verifique|confere|conferir|confira|check|est[aá] formatad|t[aá] formatad|is formatted|needs? format)`)

// brFmtDirty: the check command's result says files are out of format.
func brFmtDirty(check, out string, exit int) bool {
	if strings.HasPrefix(check, "gofmt -l ") {
		return strings.TrimSpace(out) != ""
	}
	return exit != 0
}

// brFormat formats the project: check, rewrite, check again. The seal stands
// on the second check, not on the formatter having run.
func brFormat(cx *brCtx, check string) brStep {
	fix := fpFmtFix(check)
	if one := cx.facts["format.file"]; one != "" {
		// "formata só o X": only that file, and only for a formatter that
		// takes a path
		if check != "gofmt -l ." {
			return brStep{obs: rvNoCmd(), why: "formatar um arquivo só eu sei fazer com gofmt; o formatador deste projeto é `" + check + "`"}
		}
		q := brShQ(one)
		check, fix = "gofmt -l "+q, "gofmt -w "+q
		cx.say("só %s (o resto do projeto não é tocado)", one)
		return brFormatRun(cx, check, fix, true)
	}
	return brFormatRun(cx, check, fix, check == "gofmt -l .")
}

func brFormatRun(cx *brCtx, check, fix string, gofmtList bool) brStep {
	run := func(cmd string) (string, int, rvObs) {
		cx.say("$ %s", cmd)
		out, exit, obs := cx.runCmd(cmd, 10*time.Minute)
		if s := strings.TrimRight(truncateHeadTail(out, 2000), "\n"); s != "" {
			cx.say("%s", s)
		}
		return out, exit, obs
	}
	out, exit, obs := run(check)
	if obs.Denied || obs.BudgetLeft == 0 || obs.TimedOut {
		return brStep{obs: obs, why: obs.Output}
	}
	if !brFmtDirty(check, out, exit) {
		cx.say("já estava formatado: nada a mudar")
		cx.task.AddEvidence("cmd", "format: já limpo")
		return brStep{val: brCmdRes{check, 0, out}, obs: obs, ok: true}
	}
	if fix == "" || brFmtCheckOnly.MatchString(cx.task.Goal) {
		cx.say("só conferi (`%s`): há arquivo fora do formato e nada foi reescrito", check)
		cx.task.AddEvidence("cmd", "format: conferido, não reescrito")
		if fix == "" {
			return brStep{val: brCmdRes{check, 1, out}, obs: obs, why: "há arquivo fora do formato e eu só conheço o comando de conferência deste projeto (" + check + ")"}
		}
		return brStep{val: brCmdRes{check, 0, out}, obs: obs, ok: true}
	}
	before := len(strings.Fields(out))
	if _, fexit, fobs := run(fix); fobs.Denied || fobs.BudgetLeft == 0 || fobs.TimedOut {
		return brStep{obs: fobs, why: fobs.Output}
	} else if fexit != 0 {
		return brStep{val: brCmdRes{fix, fexit, ""}, obs: fobs, why: fix + " falhou"}
	}
	out2, exit2, obs2 := run(check)
	if brFmtDirty(check, out2, exit2) {
		return brStep{val: brCmdRes{check, 1, out2}, obs: obs2, why: "rodei `" + fix + "` e ainda há arquivo fora do formato"}
	}
	if gofmtList {
		cx.say("formatado: %d arquivo(s) reescrito(s); conferência depois: limpo (/undo ou git diff mostram o que mudou)", before)
	} else {
		cx.say("formatado; conferência depois: limpo")
	}
	if cx.env.AfterWrite != nil {
		cx.env.AfterWrite(fix)
	}
	cx.task.AddEvidence("cmd", "format: reescrito e conferido limpo")
	return brStep{val: brCmdRes{check, 0, out2}, obs: obs2, ok: true}
}

// ---------- alvo de teste que é um pacote ----------

// brTestDir resolves a test target that names a directory with tests
// ("roda os testes do parser" → ./internal/parser). One match only.
func brTestDir(root, target string) (string, bool) {
	if target == "" || strings.ContainsAny(target, " /\\") {
		return "", false
	}
	found := map[string]bool{}
	for _, f := range niListWalk(root).files {
		f = filepath.ToSlash(f)
		base := filepath.Base(f)
		isTest := strings.HasSuffix(base, "_test.go") || strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py") ||
			strings.HasSuffix(base, "_test.py") || strings.Contains(base, ".test.") || strings.Contains(base, ".spec.")
		d := filepath.ToSlash(filepath.Dir(f))
		if isTest && strings.EqualFold(filepath.Base(d), target) {
			found[d] = true
		}
	}
	if len(found) != 1 {
		return "", false
	}
	for d := range found {
		return d, true
	}
	return "", false
}

func brIsDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// fpRealWords are real words one typo away from a fast-path word. They are
// requests of their own ("limpa o projeto" is not "linta o projeto"), so the
// typo repair leaves them alone.
func fpRealWord(s string) bool {
	switch s {
	case "limpa", "limpe", "limpar", "limpo", "clean", "pinta", "tinta", "quinta", "lista", "liste", "listar", "list", "line", "lines", "link", "print", "hint", "mint":
		return true
	}
	return false
}

var brWeekdayPT = [...]string{"domingo", "segunda-feira", "terça-feira", "quarta-feira", "quinta-feira", "sexta-feira", "sábado"}

// brDotenvKeys lists the variable NAMES of a .env file — never the values.
func brDotenvKeys(cx *brCtx, in map[string]string) brStep {
	obs := rvNoCmd()
	fn := cx.str(in, "file")
	if fn == "" {
		fn = ".env"
	}
	if !strings.HasPrefix(filepath.Base(fn), ".env") {
		return brStep{obs: obs, why: fn + " não é um arquivo .env"}
	}
	abs, rel, err := brSafePath(cx.env.Root, fn)
	if err != nil {
		return brStep{obs: obs, why: err.Error()}
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return brStep{obs: obs, why: "não achei " + rel + " no projeto"}
	}
	var keys []string
	empty := 0
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "export "))
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		if k, v, ok := strings.Cut(l, "="); ok && strings.TrimSpace(k) != "" {
			keys = append(keys, strings.TrimSpace(k))
			if strings.TrimSpace(v) == "" {
				empty++
			}
		}
	}
	cx.say("%s tem %d variável(is): %s", rel, len(keys), strings.Join(keys, ", "))
	cx.say("(só os nomes; %d sem valor. os valores não são mostrados: podem ser credenciais)", empty)
	cx.task.AddEvidence("file", fmt.Sprintf("%s: %d chaves", rel, len(keys)))
	return brStep{val: strings.Join(keys, ","), obs: obs, ok: true}
}

// ---------- atalho dentro do cérebro ----------

// brFastMatch: the read-only fast path (intents.json) the text resolves to.
// The offline door tries the fast paths before the brain for a whole request;
// this gives the same reading to one PART of a multi-step request ("show the
// current branch and the last commit" read "the last commit" as git_commit).
func brFastMatch(root, text string) (fpMatch, bool) {
	_, rgErr := exec.LookPath("rg")
	m, ok := fpIntents(root).Match(text, fpDetect(root), rgErr == nil)
	if !ok || !IsReadOnly(m.Cmd) && !cbReadCmd(m.Cmd) {
		return fpMatch{}, false
	}
	return m, true
}

// brFastCmd runs the fast path of the task's own text. The command is
// re-derived here: the slot is only compared, never executed as given.
func brFastCmd(cx *brCtx, in map[string]string) brStep {
	obs := rvNoCmd()
	m, ok := brFastMatch(cx.env.Root, cx.task.Goal)
	if !ok || m.Cmd != cx.str(in, "cmd") {
		return brStep{obs: obs, why: "o atalho não corresponde mais ao pedido"}
	}
	out, exit, o := cx.runCmdAs(m.Cmd, time.Minute, true)
	if o.Exit < 0 || o.Denied {
		return brStep{obs: o, why: o.Output}
	}
	text, done := olFastText(m, redactSecrets(out), exit, false)
	cx.say("$ %s\n%s", m.Cmd, strings.TrimRight(truncateHeadTail(text, 4000), "\n"))
	if !done {
		return brStep{val: brCmdRes{m.Cmd, exit, out}, obs: o, why: m.Cmd + " falhou"}
	}
	return brStep{val: brCmdRes{m.Cmd, 0, out}, obs: o, ok: true}
}

// brThereRe: "pra lá" / "there" pointing at the folder the previous step made.
var brThereRe = lazyRx(`(?i)\b(?:pra|para) (?:l[aá]|ela|ele|essa pasta|esta pasta)\b|\bl[aá] dentro\b|\b(?:in|into|to) (?:it|there|that folder)\b|\bthere\b`)

// brResolveThere rewrites the anaphora of part with the directory the previous
// part creates ("cria a pasta backup e copia o config.json pra lá").
func brResolveThere(part string, prev Intent) string {
	if prev.Name != "create" || prev.Slots["type"] != "dir" || prev.Slots["target"] == "" || !brThereRe.MatchString(part) {
		return part
	}
	return brThereRe.ReplaceAllString(part, "pra pasta "+prev.Slots["target"])
}

func brHasFlag(cmd string) bool {
	for _, w := range strings.Fields(cmd)[1:] {
		if strings.HasPrefix(w, "-") {
			return true
		}
	}
	return false
}

// brGitWriteSub: git subcommands that change the repository keep going
// through their own intents (message checks, postconditions, confirmation).
func brGitWriteSub(cmd string) bool {
	f := strings.Fields(cmd)
	if len(f) < 2 || f[0] != "git" {
		return false
	}
	switch f[1] {
	case "commit", "push", "pull", "stash", "checkout", "switch", "reset", "rebase", "merge", "clean", "rm", "branch", "restore", "add", "tag", "fetch", "cherry-pick", "revert", "init", "clone", "config":
		return true
	}
	return false
}

// brLiteralRun runs the command line the user typed (brLiteralCmd), re-derived
// from the request itself.
func brLiteralRun(cx *brCtx, in map[string]string) brStep {
	obs := rvNoCmd()
	cmd, ok := brLiteralCmd(cx.task.Goal)
	if !ok || cmd != cx.str(in, "cmd") {
		return brStep{obs: obs, why: "a linha de comando não corresponde mais ao pedido"}
	}
	read := IsReadOnly(cmd) || cbReadCmd(cmd)
	out, exit, o := cx.runCmdAs(cmd, 10*time.Minute, read)
	if o.Exit < 0 || o.Denied || o.BudgetLeft == 0 {
		return brStep{obs: o, why: o.Output}
	}
	shown := strings.TrimRight(truncateHeadTail(redactSecrets(out), 6000), "\n")
	if shown == "" {
		shown = fmt.Sprintf("(sem saída, exit %d)", exit)
	}
	cx.say("$ %s\n%s", cmd, shown)
	cx.task.AddEvidence("cmd", fmt.Sprintf("%s: exit %d", cmd, exit))
	if exit == 126 || exit == 127 {
		return brStep{val: brCmdRes{cmd, exit, out}, obs: o, why: strings.Fields(cmd)[0] + " não rodou (exit " + strconv.Itoa(exit) + ")"}
	}
	return brStep{val: brCmdRes{cmd, exit, out}, obs: o, ok: true}
}

// brPartialMulti: the request has two clauses, the split was not accepted
// (one part is not understood well enough to run), and the whole-request
// reading answers only one of them. Doing half and sealing it is a false
// success ("conta as linhas do X e mostra os erros dele" counted the lines and
// stopped). "" = not that case ("procura por TODO e FIXME" is one request).
func brPartialMulti(parts []string, whole Intent, w *nuWorld) string {
	if whole.Phrase || !whole.Understood() {
		return ""
	}
	other := 0
	for _, p := range parts {
		if len(strings.Fields(p)) < 2 {
			return "" // a one-word tail is an operand, not a clause
		}
		pi := nuGet().Parse(p, w)
		if pi.Name != "none" && pi.Name != whole.Name && pi.Raw >= 0.4 {
			other++
		}
	}
	if other == 0 {
		return ""
	}
	return fmt.Sprintf("o pedido tem %d partes e eu só entendi bem uma delas (%s). fazer metade e dizer que fiz seria errado: não fiz nada. peça uma parte de cada vez: %s", len(parts), whole.Name, "\""+strings.Join(parts, "\" · \"")+"\"")
}

// brGitDiffFile shows the uncommitted diff of ONE file (the request named
// it; the general diff answered with the whole repository's summary).
func brGitDiffFile(cx *brCtx, in map[string]string) brStep {
	obs := rvNoCmd()
	rel, ok := brProjectFile(cx.env.Root, cx.str(in, "path"))
	if !ok {
		return brStep{obs: obs, why: "não achei o arquivo " + cx.str(in, "path") + " no projeto"}
	}
	cmd := "git diff HEAD -- " + brShQ(rel)
	out, exit, o := cx.runCmdAs(cmd, time.Minute, true)
	if o.Exit < 0 || o.Denied {
		return brStep{obs: o, why: o.Output}
	}
	shown := strings.TrimRight(truncateHeadTail(out, 6000), "\n")
	if shown == "" {
		shown = rel + " não tem mudança em relação ao último commit"
	}
	cx.say("$ %s\n%s", cmd, shown)
	if exit != 0 {
		return brStep{val: brCmdRes{cmd, exit, out}, obs: o, why: cmd + " falhou"}
	}
	cx.task.AddEvidence("cmd", "git diff de "+rel)
	return brStep{val: brCmdRes{cmd, 0, out}, obs: o, ok: true}
}

var brPassLineRe = lazyRx(`(?m)^\s*--- PASS: (\S+)|^(\S+::\S+) PASSED`)

// brPassedTests names the tests a verbose run reports as passed ("roda só os
// testes que têm Price no nome": the answer should say WHICH test ran).
func brPassedTests(out string) []string {
	var names []string
	for _, m := range brPassLineRe.FindAllStringSubmatch(out, 40) {
		if m[1] != "" {
			names = append(names, m[1])
		} else if m[2] != "" {
			names = append(names, m[2])
		}
	}
	return names
}
