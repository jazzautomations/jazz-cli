package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// brain_honest.go — o selo cobre o VERBO do pedido, não só os operandos.
//
// A sonda de 89 pedidos reais (docs/research/offline-realworld-probe.md,
// 2026-11-09) achou sete "feito e verificado" que não cumpriam o pedido. Três
// causas ficam fechadas aqui, antes de qualquer método rodar:
//
//  1. Pergunta nunca dispara intenção que muda coisa. "qual foi o último
//     commit" era lido como git_commit (a regra é a palavra "commit") e
//     commitava tudo com a mensagem "último".
//  2. Pedido de escrita cuja única resposta offline é um comando de leitura
//     não roda a leitura pra depois selar verde. "guarda minhas mudanças no
//     stash" rodava `git stash list`; "cria uma branch chamada teste" rodava
//     `git branch`.
//  3. Trabalho que precisa de modelo não vira `create` de um arquivo vazio.
//     "escreve um teste pra função Soma" criava um arquivo chamado Soma.
//
// Nos três casos a resposta é um miss honesto, sem efeito colateral: diz o que
// foi lido, por que não agiu e o que dá pra fazer agora.

// brQuestionRe: a frase começa com palavra interrogativa. Um "?" no fim não
// basta ("commita isso pra mim?" é pedido educado, não pergunta).
var brQuestionRe = lazyRx(`(?i)^\s*(?:(?:e|ent[aã]o|mas|a[ií]|uai|mano|cara|p[oô]|bah|opa|oxe|eita|v[eé]i|tipo|bom|olha|hey|so|well|hmm|ok)[,\s]+)*(?:(?:queria\s+saber|preciso\s+saber|quero\s+ver|d[aá]\s+pra\s+(?:saber|ver)|consegue\s+me\s+dizer|could\s+you\s+tell\s+me|i\s+(?:need|want)\s+to\s+know|i\s+wonder|wondering|me\s+(?:diz|diga|fala|fale|conta|conte|explica)|sabe(?:\s+me\s+dizer)?|pode\s+me\s+dizer|quero\s+saber|tell\s+me|do\s+you\s+know|can\s+you\s+tell\s+me)\s+)?(?:qual|quais|quem|quando|onde|cad[eê]|quant[oa]s?|por\s*qu[eê]|porqu[eê]|pq|como|o\s+que|oq|what|which|who|whose|when|where|why|how|is|are|was|were|does|do|did|tem|existe|h[aá])(?:[\s?,.!:]|$)`) // RE2: \b é ASCII e falha depois de "cadê"

func brIsQuestion(text string) bool { return brQuestionRe.MatchString(text) }

// brWriteVerbRe: verbo de escrita no imperativo/infinitivo (pt e en). Formas
// de particípio ("criadas", "changed") ficam de fora de propósito: "lista as
// branches criadas hoje" é leitura.
var brWriteVerbRe = lazyRx(`(?i)(?:^|[\s,;:])(cria|crie|criar|create|make|guarda|guarde|guardar|salva|salve|salvar|save|esconde|esconder|aplica|aplicar|apply|pop|adiciona|adicionar|add|muda|mudar|troca|trocar|switch|altera|alterar|change|apaga|apagar|deleta|deletar|delete|remove|remover|renomeia|renomear|rename|restaura|restaurar|restore|descarta|descartar|drop|discard)\b`)

// brListWordRe: o pedido diz explicitamente que quer ver, não fazer.
var brListWordRe = lazyRx(`(?i)\b(lista|listar|liste|list|mostra|mostrar|mostre|show|ver|veja|quais|qual|tem|what|which)\b`)

// brBareWrite são intenções cujo nome já é o verbo de escrita: "git stash" ou
// "stash" sozinho quer dizer guardar, e o único método offline só lista.
var brBareWrite = map[string]bool{"git_stash": true}

// brFirstMethod is the method brExecute would pick first for in (the same
// applicability walk as brIntentRisk).
func brFirstMethod(env *brEnv, in Intent) (mhMethod, bool) {
	lib, _ := mhLibrary()
	methods := lib[in.Name]
	if len(methods) == 0 {
		return mhMethod{}, false
	}
	p := env.Profile
	if p == nil {
		p = epGet(false)
	}
	probe := &brCtx{env: env, in: in, task: &TaskState{Slots: in.Slots, Facts: map[string]string{}}}
	probe.proj = fpDetect(env.Root)
	facts := brFacts(probe, p)
	for _, m := range methods {
		if mhApplicable(m, facts) {
			return m, true
		}
	}
	return mhMethod{}, false
}

// brReadOnlyMethod: every step is a plain read command.
func brReadOnlyMethod(m mhMethod) bool {
	if len(m.Steps) == 0 {
		return false
	}
	for _, s := range m.Steps {
		if s.Cap != "read_cmd" {
			return false
		}
	}
	return true
}

// brMutates: the reading would change the project or the machine.
func brMutates(in Intent, risk cbRisk) bool { return risk >= cbHigh || in.Name == "create" }

// brVerbGuard returns the honest-miss text (and its MissWhy) when acting on
// in would not be what text asked for; "" = go ahead.
func brVerbGuard(env *brEnv, text string, in Intent, risk cbRisk) (why, kind string) {
	desc := nuGet().descOf(in.Name)
	if brIsQuestion(text) && brMutates(in, risk) {
		return fmt.Sprintf("isso é uma pergunta, e a leitura que eu tenho (%s — %s) muda coisas. não vou agir numa pergunta: nada foi alterado. se era pra fazer, peça no imperativo; se era pra saber, reformule (ex.: \"mostra o git log\")", in.Name, desc), "question_mutation"
	}
	if in.Name == "create" {
		if w, ok := brNeedsModel(text); ok {
			return w, "needs_model"
		}
	}
	if in.Name != "fix" && in.Name != "fastpath" {
		if w, ok := brNeedsModelLead(text); ok {
			return w, "needs_model"
		}
	}
	if why, kind := brSemanticGuard(text, in); why != "" {
		return why, kind
	}
	if f := brFileIgnored(env, text, in); f != "" {
		return fmt.Sprintf("o pedido cita %s, e a leitura que eu tenho (%s — %s) não usa esse arquivo: responder com ela seria responder outra pergunta. não fiz nada. reformule dizendo o que quer do arquivo (ex.: \"abre o %s\", \"procura X no %s\")", f, in.Name, desc, f, f), "file_ignored"
	}
	if m, ok := brFirstMethod(env, in); ok && brReadOnlyMethod(m) {
		verb := strings.TrimSpace(brWriteVerbRe.FindString(text))
		verb = strings.TrimLeft(verb, ",;: ")
		bare := brBareWrite[in.Name] && !brListWordRe.MatchString(text)
		if verb != "" || bare {
			if verb == "" {
				verb = in.Name
			}
			show := ""
			if len(m.Cmds) > 0 {
				show = fmt.Sprintf(" o que eu sei mostrar agora: `%s` (peça \"lista\"/\"mostra\").", m.Cmds[0])
			}
			return fmt.Sprintf("o pedido é de escrita (%q) e, sem modelo, de %s eu só faço a parte de leitura. não fiz nada e não vou fingir que fiz.%s pra fazer de verdade, use um modelo (rode sem --offline, ou /online)", verb, in.Name, show), "write_unsupported"
		}
	}
	return "", ""
}

// brNoTestsRan: the runner said it ran nothing. Go prints "[no tests to run]"
// on every "ok" line when -run matches no test; pytest prints "no tests ran".
func brNoTestsRan(out string) bool {
	if strings.Contains(out, "no tests ran") || strings.Contains(out, "collected 0 items") {
		return true
	}
	ok, empty := 0, 0
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "ok ") || strings.HasPrefix(l, "ok\t") {
			ok++
			if strings.Contains(l, "[no tests to run]") {
				empty++
			}
		}
	}
	return ok > 0 && ok == empty
}

// brBareFile resolves a bare file name (no directory) that does not exist at
// root to the ONE project file with that base name. Zero or several matches:
// ok=false, and the caller keeps the name as given (the step fails honestly).
func brBareFile(root, name string) (rel string, ok bool) {
	if name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return "", false
	}
	if _, err := os.Lstat(filepath.Join(root, name)); err == nil {
		return "", false
	}
	found := ""
	for _, f := range niListWalk(root).files {
		if filepath.Base(f) == name {
			if found != "" {
				return "", false
			}
			found = f
		}
	}
	return found, found != ""
}

// brFileTokRe: a token that looks like a file path (has an extension or a slash).
var brFileTokRe = lazyRx(`[\w.-]*[\w-]+(?:/[\w.-]+)+|[\w-]*\.?[\w-]+\.[A-Za-z0-9]{1,6}\b|\.env[\w.]*`)

// brFileSelf are intents whose method reads a fixed project file whatever the
// slots say, so naming that file in the request is consumed by definition.
var brFileSelf = map[string]string{"dotenv_check": ".env", "env_get": ".env", "project_summary": "", "git_status": "", "git_diff": "", "run_tests": "", "build": "", "lint": ""}

// brFileIgnored returns the project file the request names that the reading
// does not use at all ("" = none). "any IP addresses in logs/app.log?" was read
// as net_ifaces and answered with the machine's interfaces; "qual a porta
// configurada no config.json?" listed the listening sockets. The operand
// guard covered quoted operands (Dropped); this covers a named file.
func brFileIgnored(env *brEnv, text string, in Intent) string {
	if in.Phrase || in.Name == "fastpath" || in.Name == "smalltalk" || in.Name == "none" {
		return ""
	}
	w := env.World
	if w == nil {
		w = nuRuntimeWorld(env.Root, env.Profile)
	}
	var blob strings.Builder
	for _, v := range in.Slots {
		blob.WriteString(v)
		blob.WriteByte('\n')
	}
	slots := blob.String()
	for _, tok := range brFileTokRe.FindAllString(text, 8) {
		tok = strings.Trim(tok, ".,;:?!")
		rel := tok
		if f, ok := nuWorldFile(w, tok); ok {
			rel = f
		} else if fi, err := os.Stat(filepath.Join(env.Root, filepath.FromSlash(tok))); err != nil || fi.IsDir() || strings.Contains(tok, "..") || filepath.IsAbs(tok) {
			continue // not a file of this project: nothing to say
		}
		base := filepath.Base(rel)
		if strings.Contains(slots, base) || strings.Contains(slots, strings.TrimSuffix(base, filepath.Ext(base))) && len(base) > 6 {
			continue
		}
		if pre, self := brFileSelf[in.Name]; self && (pre == "" || strings.HasPrefix(base, pre)) {
			continue
		}
		return rel
	}
	return ""
}

// brCommitVerbRe: the request orders a commit (a verb form, or "commit" used
// as a command). The noun alone — "a mensagem do commit mais recente" — is
// somebody asking about a commit.
var brCommitVerbRe = lazyRx(`(?i)\b(?:comm?it[ae]\w*|comm?itar|comm?itando|fa(?:z|zer|ca|ça) (?:um |o |outro )?commit|manda (?:um |o )?commit|salva (?:um |o )?commit|git commit)\b|^\s*(?:(?:por favor|please|pode|bora|vamos)[, ]+)*commit(?:$|[: ]\s*(?:-|"|\x27|with|everything|all|tudo|isso|the|my|these|this|com|as|os|so|only|just))`)

// brGitNounRe / brGitOK: a request that talks about git objects is answered
// by a git reading (or a fast path), never by a listing of files or a
// project summary that happened to share a word with it.
var (
	brGitNounRe = lazyRx(`(?i)\b(commits?|branch(?:es|s)?|stash|merge|rebase|untracked|rastread[oa]s?|rastreando|versionad[oa]s?|staged|git)\b`)
	brGitOK     = map[string]bool{"fastpath": true, "undo": true, "tool_info": true, "tool_status": true, "pkg_installed": true, "help": true,
		"smalltalk": true, "multi": true, "self_info": true, "shell_cmd": true, "fix": true, "update_jazz": true, "env_status": true, "explain": true}
	// brTodoWordRe: "todo" as the Portuguese word for "all" ("no projeto todo",
	// "o dia todo", "todo o código"), not the TODO marker.
	brTodoWordRe = lazyRx(`(?i)\b(?:projeto|repo|repositorio|repositório|codigo|código|dia|mundo|tempo|arquivo|log|sistema) todo\b|\btodo (?:o|a|os|as|mundo|dia)\b`)
)

var (
	brCommitNounRe = lazyRx(`\b(?:ultimo|último|last|latest|primeiro|first|penultimo|penúltimo|previous|anterior) commit\b|\bno commit\b|\bin the (?:last |latest )?commit\b|\bcommit (?:anterior|passado|mais recente)\b`)
	brSummaryAskRe = lazyRx(`\b(resumo|resume|resuma|sumario|sumário|summary|summari[sz]e|overview|visao geral|visão geral|sobre o que|que projeto|what is this|what's this|describe|descreve|descreva|detecta|detect|stack|tecnologias|linguagens?|languages?|como (?:roda|builda|testa)|how (?:do i|to) (?:run|build|test))\b`)
	brSearchLeadRe = lazyRx(`^\s*(?:(?:por favor|please|pode|bora|me)[, ]+)*(?:busca|busque|buscar|procura|procure|procurar|acha|ache|achar|encontra|encontre|find|search|grep|seach|serach|procrua|procurra)\b`)
	brEnvFileRe    = lazyRx(`(?:^|[\s/"'])\.env[\w.]*\b`)
)

func brNotSearchIntent(n string) bool {
	switch n {
	case "env_status", "project_summary", "net_status", "uptime_load", "mem_usage", "disk_usage", "list_ports", "self_info", "tool_info",
		"tool_status", "host_status", "net_ifaces", "date_show", "help", "list_hosts", "net_route", "logged_in", "pkg_installed":
		return true
	}
	return false
}

// brSemanticGuard catches readings that picked one word of the request and
// answered something else (second held-out set, 13 false successes).
func brSemanticGuard(text string, in Intent) (why, kind string) {
	if in.Phrase {
		return "", ""
	}
	desc := nuGet().descOf(in.Name)
	switch in.Name {
	case "git_commit":
		if !brCommitVerbRe.MatchString(text) {
			return "o pedido fala de commit mas não manda commitar, e a leitura que eu tenho (git_commit) muda o repositório. não commitei nada. pra ver: \"último commit\" ou \"mostra o git log\"; pra commitar: \"commita com a mensagem ...\"", "question_mutation"
		}
	case "todo_scan":
		if brTodoWordRe.MatchString(text) && !strings.Contains(text, "TODO") && !strings.Contains(strings.ToLower(text), "fixme") {
			return "li \"todo\" como a palavra (tudo), não como o marcador TODO, então não vou responder com a lista de TODOs. diga o que procurar e onde (ex.: \"procura localhost no código\")", "wrong_reading"
		}
	case "create":
		t := in.Slots["target"]
		if in.Slots["type"] != "dir" && (brCreateStopWord(strings.ToLower(t)) || !strings.Contains(t, ".") && brFileTokRe.MatchString(strings.ReplaceAll(text, t, ""))) {
			return fmt.Sprintf("eu criaria um arquivo chamado %q, e isso não parece ser o nome que você pediu. não criei nada. diga o nome exato (ex.: \"cria a pasta build/artefatos\", \"cria o arquivo notas.md\")", t), "wrong_reading"
		}
	}
	low := strings.ToLower(text)
	switch {
	case in.Name == "git_diff" && brCommitNounRe.MatchString(low):
		// "quais arquivos mudaram no último commit" is about a commit; git_diff
		// answers with the uncommitted changes
		return "o pedido é sobre um commit, e a leitura que eu tenho (git_diff) mostra o que ainda NÃO foi commitado: seria responder outra pergunta. não fiz nada. peça \"último commit\" (mostra os arquivos dele) ou \"mostra o git log\"", "wrong_reading"
	case in.Name == "project_summary" && !brSummaryAskRe.MatchString(low):
		return "a leitura que eu tenho é o resumo do projeto, e o pedido não pede um resumo: responder com ele seria responder outra pergunta. não fiz nada. diga o que quer ver (ex.: \"lista os arquivos\", \"lista os arquivos json\", \"resumo do projeto\")", "wrong_reading"
	case brSearchLeadRe.MatchString(low) && brNotSearchIntent(in.Name):
		return fmt.Sprintf("o pedido é uma busca e a leitura que eu tenho (%s — %s) não procura nada: seria responder outra pergunta. não fiz nada. diga o que procurar e onde (ex.: \"procura X no código\", \"procura X no arquivo.go\")", in.Name, desc), "wrong_reading"
	case in.Name == "config_get" && brEnvFileRe.MatchString(text):
		// the config reader prints every value of the file, credentials included
		return "ler um .env pelo leitor de config imprimiria todos os valores, inclusive credenciais. não fiz. peça \"quais variáveis tem no .env\" (só os nomes) ou \"qual o valor de NOME\" (credencial fica oculta)", "secret_guard"
	}
	if brGitNounRe.MatchString(text) && !strings.HasPrefix(in.Name, "git_") && !brGitOK[in.Name] {
		return fmt.Sprintf("o pedido é sobre git e a leitura que eu tenho (%s — %s) não é: responder com ela seria responder outra pergunta. não fiz nada. sem modelo eu sei: status, diff, log, último commit, branches, stash, quem mexeu num arquivo", in.Name, desc), "wrong_reading"
	}
	return "", ""
}

// ---------- comando digitado cru ----------

// brLiteralBins: programs a developer types as a command line, not as prose.
func brLiteralBin(s string) bool {
	switch s {
	case "git", "go", "gofmt", "make", "cat", "ls", "wc", "grep", "rg", "head", "tail", "find", "tree", "du", "pwd", "npm", "pnpm", "yarn", "cargo", "pytest", "python3", "node", "sort", "uniq", "diff", "stat", "file":
		return true
	}
	return false
}

// brLiteralProse: words that make the line a sentence ("git status do repo").
func brLiteralProseWord(s string) bool {
	switch s {
	case "o", "a", "os", "as", "do", "da", "de", "pra", "para", "the", "of", "my", "me", "e", "and", "que", "no", "na", "em", "um", "uma", "por", "com", "to", "for", "with", "please", "ai", "so", "only", "all", "tudo":
		return true
	}
	return false
}

// brLiteralCmd: text IS a command line (first word a known program, the rest
// flags, paths and words that are not prose), with no shell operators. It
// runs as typed, through the sandbox and the permission check — the same
// thing `!cmd` does, without the user having to know about `!`.
func brLiteralCmd(text string) (string, bool) {
	t := strings.TrimSpace(text)
	if t == "" || len(t) > 200 || strings.ContainsAny(t, ";|&<>`$\n(){}\\") {
		return "", false
	}
	f := strings.Fields(t)
	if len(f) < 2 || len(f) > 12 || !brLiteralBin(f[0]) {
		return "", false
	}
	shellish := false
	for _, w := range f[1:] {
		if brLiteralProseWord(strings.ToLower(strings.Trim(w, ",.?!"))) || strings.HasSuffix(w, "?") {
			return "", false
		}
		if strings.HasPrefix(w, "-") || strings.ContainsAny(w, "./") {
			shellish = true
		}
	}
	if !shellish && f[0] != "git" && f[0] != "go" && f[0] != "make" && f[0] != "cat" && f[0] != "ls" && f[0] != "npm" && f[0] != "cargo" {
		return "", false // "find usages", "file handling": prose that starts with a program name
	}
	return t, true
}

func brCreateStopWord(s string) bool {
	switch s {
	case "nova", "novo", "new", "pasta", "arquivo", "file", "folder", "dir", "vazio", "vazia", "empty", "chamada", "chamado", "called", "named", "um", "uma", "teste", "test", "isso", "aqui", "linha", "line":
		return true
	}
	return false
}
