package main

import (
	"encoding/csv"
	"fmt"
	"os"
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
			if shown < 40 {
				cx.say("%s:%d: %s", filepath.ToSlash(rel), i+1, oneLineKeep(l, 240))
				shown++
			}
		}
		total += hits
		cx.task.AddEvidence("file", fmt.Sprintf("%s: %d linha(s) de erro", filepath.ToSlash(rel), hits))
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
	}
	if used == 0 {
		return brStep{obs: obs, why: fmt.Sprintf("a coluna %s de %s não tem número nenhum", rows[0][col], rel)}
	}
	cx.say("soma de %s em %s: %s (%d linha(s) somada(s), %d sem número)", rows[0][col], filepath.ToSlash(rel),
		strconv.FormatFloat(sum, 'f', -1, 64), used, skipped)
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
	if check == "gofmt -l ." {
		return strings.TrimSpace(out) != ""
	}
	return exit != 0
}

// brFormat formats the project: check, rewrite, check again. The seal stands
// on the second check, not on the formatter having run.
func brFormat(cx *brCtx, check string) brStep {
	fix := fpFmtFix(check)
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
	if check == "gofmt -l ." {
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
var fpRealWords = map[string]bool{"limpa": true, "limpe": true, "limpar": true, "limpo": true, "clean": true,
	"pinta": true, "tinta": true, "quinta": true, "lista": true, "liste": true, "listar": true, "list": true,
	"line": true, "lines": true, "link": true, "print": true, "hint": true, "mint": true, "resta": true, "festa": true,
	"texto": true, "text": true, "build": true, "guild": true, "roda": true, "moda": true, "toda": true, "todo": true}

var brWeekdayPT = [...]string{"domingo", "segunda-feira", "terça-feira", "quarta-feira", "quinta-feira", "sexta-feira", "sábado"}
