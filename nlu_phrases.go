package main

import (
	"path/filepath"
	"strings"
)

// nlu_phrases.go — frases feitas: pedidos do dia a dia que a gramática de
// conceitos lia errado ou com confiança baixa demais para agir (sonda de 89
// pedidos, docs/research/offline-realworld-probe.md).
//
// Cada frase é um padrão de casamento TOTAL sobre o pedido normalizado
// (fpNormalize: sem acento, sem pontuação final, sem "por favor"), com os
// operandos em grupos nomeados. Não é um segundo classificador: ou a frase
// inteira casa e todo operando resolve no mundo (o arquivo existe, o nome é
// um identificador), ou a frase não se aplica e a gramática decide como
// antes. Uma frase nunca adivinha.
//
// Dois momentos:
//   - nuPhraseParse roda antes da gramática: a frase é a leitura.
//   - nuPhraseRepair roda depois, só quando a gramática não entendeu: conserta
//     o operando (nome de arquivo com erro de digitação) e diz o que leu.

// nuPhraseExt: extensões e nomes de linguagem aceitos como filtro de arquivo.
const nuPhraseExt = `go|golang|py|python|js|javascript|ts|typescript|tsx|jsx|md|markdown|json|yaml|yml|toml|sh|rs|rust|c|h|cpp|java|rb|ruby|php|html|css|sql|txt|csv|log|xml`

var nuPhraseExtCanon = map[string]string{"golang": "go", "python": "py", "javascript": "js", "typescript": "ts",
	"markdown": "md", "rust": "rs", "ruby": "rb"}

const (
	nuPhShow = `(?:(?:mostra|mostre|me mostra|me mostre|exibe|ver|veja|show|show me|print|imprime|da|me da) )?`
	nuPhFile = `(?:arquivo |file )?(?P<file>[\w./-]+)`
	nuPhSym  = `(?P<sym>[A-Za-z_][A-Za-z0-9_]{2,79})`
	nuPhKind = `(?:funcao |metodo |classe |tipo |struct |variavel |constante |function |method |class |type )?`
	nuPhFind = `(?:acha|ache|achar|procura|procure|procurar|busca|busque|buscar|encontra|encontre|encontrar|find|search|grep)`
)

// nuPhraseDef is one phrase: patterns, the intent, fixed slots, and how the
// captured groups become slots (group name → slot name; a group named
// "file" must resolve to a project file, "sym" must be an identifier).
type nuPhraseDef struct {
	id      string
	pats    []string
	intent  string
	fixed   map[string]string
	groups  map[string]string
	prepare func(g map[string]string, w *nuWorld) bool // extra validation/derivation; false = phrase does not apply
	res     []*lazyRe
}

var nuPhrases = []*nuPhraseDef{
	{id: "primeiras-n-linhas", intent: "line_slice", fixed: map[string]string{"start": "1"}, groups: map[string]string{"n": "end", "file": "file"},
		pats: []string{
			nuPhShow + `(?:as |the )?(?:primeiras (?P<n>\d{1,5})|(?P<n2>\d{1,5}) primeiras|first (?P<n3>\d{1,5})) (?:linhas?|lines?) (?:do|da|de|of|in|from) ` + nuPhFile,
			`head (?:-n ?)?-?(?P<n>\d{1,5}) ` + nuPhFile,
		}},
	{id: "ultimas-n-linhas", intent: "line_slice", groups: map[string]string{"n": "tail", "file": "file"},
		pats: []string{
			nuPhShow + `(?:as |the )?(?:ultimas (?P<n>\d{1,5})|(?P<n2>\d{1,5}) ultimas|last (?P<n3>\d{1,5})) (?:linhas?|lines?) (?:do|da|de|of|in|from) ` + nuPhFile,
			`tail (?:-n ?)?-?(?P<n>\d{1,5}) ` + nuPhFile,
		}},
	{id: "csv-pra-json", intent: "csv_to_json", groups: map[string]string{"file": "target"},
		pats: []string{
			`(?:converte|converter|converta|convert|transforma|transformar|transforme) (?:o |a |the )?` + nuPhFile + ` (?:pra|para|em|to|into|in) json`,
		},
		prepare: func(g map[string]string, w *nuWorld) bool {
			return strings.HasSuffix(strings.ToLower(g["file"]), ".csv")
		}},
	{id: "onde-usa", intent: "symbol_refs", groups: map[string]string{"sym": "symbol"},
		pats: []string{
			`onde (?:e que )?(?:a |o )?` + nuPhKind + nuPhSym + ` (?:e|esta sendo|ta sendo|foi|sao) (?:usad[oa]s?|chamad[oa]s?|referenciad[oa]s?|utilizad[oa]s?)`,
			`(?:quem|o que|onde) (?:usa|chama|referencia|utiliza)(?: a| o)? ` + nuPhKind + nuPhSym,
			`(?:` + nuPhFind + `|mostra|lista) onde (?:usa|usam|chama|chamam|se usa|e usad[oa])(?: a| o)? ` + nuPhKind + nuPhSym,
			`where is (?:the )?` + nuPhKind + nuPhSym + ` (?:used|called|referenced)`,
			`(?:who|what) (?:uses|calls|references) (?:the )?` + nuPhKind + nuPhSym,
			`(?:find |show |list )?(?:the )?(?:usages|uses|references|callers) (?:of|to|for) (?:the )?` + nuPhKind + nuPhSym,
			`(?:usos|referencias|chamadas) (?:de|da|do|a|pra) ` + nuPhKind + nuPhSym,
		}},
	{id: "texto-em-arquivo", intent: "text_grep", groups: map[string]string{"pat": "pattern"},
		pats: []string{
			nuPhFind + `(?: por| pela| pelo| for)?(?: a| o| the)?(?: palavra| texto| termo| string| word| text)? (?P<pat>[\w.:-]{2,60}) (?:no|na|nos|nas|em|dentro de|dentro do|dentro da|in|in the|inside) (?:arquivo |file |pasta |dir |folder )?(?P<where>[\w./-]+)`,
		},
		prepare: func(g map[string]string, w *nuWorld) bool {
			where := g["where"]
			switch strings.ToLower(where) {
			case "codigo", "projeto", "repo", "repositorio", "code", "codebase", "project", "fonte", "fontes", "arquivos", "files":
				return false // the whole project: the search fast path and `find` own that
			case "log", "logs":
				logs := nuWorldByExt(w, ".log")
				if len(logs) == 1 {
					g["file"] = logs[0]
					return true
				}
				if d := nuWorldCommonDir(logs); len(logs) > 1 && d != "" {
					g["dir"] = d
					return true
				}
			}
			if f, ok := nuWorldFile(w, where); ok {
				g["file"] = f
				return true
			}
			if nuWorldDir(w, where) {
				g["dir"] = strings.TrimSuffix(where, "/")
				return true
			}
			return false
		}},
	{id: "esquema-json", intent: "json_schema_infer", groups: map[string]string{"file": "target"},
		pats: []string{
			`(?:(?:mostra|mostre|me mostra|infere|inferir|infira|qual|qual e|show|show me|infer|ver) )?(?:o |a |the )?(?:esquema|schema|estrutura|formato|structure|shape) (?:do|da|de|of) ` + nuPhFile,
		},
		prepare: func(g map[string]string, w *nuWorld) bool {
			return strings.HasSuffix(strings.ToLower(g["file"]), ".json")
		}},
	{id: "soma-coluna-csv", intent: "csv_sum", groups: map[string]string{"col": "col", "file": "file"},
		pats: []string{
			`(?:soma|somar|some|sum|totaliza|totalizar)(?: a| o| os valores da| os valores do| the)? (?:coluna|column|campo|field) (?P<col>\w{1,40}) (?:do|da|de|of|in|no|na|from) ` + nuPhFile,
			`(?:qual|what is|whats)(?: e| a| o| the)* (?:soma|total|sum) (?:da|do|de|of)(?: coluna| column| campo| the column)? (?P<col>\w{1,40}) (?:do|da|de|no|na|of|in|from) ` + nuPhFile,
			`(?:total|soma|sum) (?:da|do|de|of)(?: coluna| column)? (?P<col>\w{1,40}) (?:do|da|de|no|na|of|in|from) ` + nuPhFile,
		},
		prepare: func(g map[string]string, w *nuWorld) bool {
			return strings.HasSuffix(strings.ToLower(g["file"]), ".csv")
		}},
	{id: "que-horas", intent: "date_show",
		pats: []string{
			`que horas sao(?: agora)?|que hora e(?: agora)?|que horas e|hora atual|horario atual|que dia e hoje|data e hora|what time is it(?: now)?|current time|whats the time|what day is it(?: today)?`,
		}},
	{id: "arquivos-por-extensao", intent: "list_files", fixed: map[string]string{"type": "file"}, groups: map[string]string{"ext": "ext"},
		pats: []string{
			`(?:(?:lista|liste|listar|mostra|mostre|me mostra|list|show|show me|quais sao|ver) )?(?:todos |all )?(?:os |the )?(?:arquivos|files) (?:\.|de |do tipo |em |in )?(?P<ext>` + nuPhraseExt + `)`,
			`(?:(?:list|show|show me|lista|mostra) )?(?:all )?(?:the )?\.?(?P<ext>` + nuPhraseExt + `) files`,
		}},
	{id: "linhas-por-extensao", intent: "wordcount_stats", fixed: map[string]string{"what": "lines"}, groups: map[string]string{"ext": "ext"},
		pats: []string{
			`(?:conta|contar|conte|count)(?: as| the)? (?:linhas|lines) (?:dos|de|nos|em|of|in)(?: todos os| all| all the| the)? ?(?:arquivos |files )?\.?(?P<ext>` + nuPhraseExt + `)(?: files)?`,
			`quantas linhas (?:tem|ha|existem|tem em) (?:nos|em|os|nos arquivos|os arquivos) (?:arquivos )?\.?(?P<ext>` + nuPhraseExt + `)`,
			`how many lines (?:of |in )(?:the )?\.?(?P<ext>` + nuPhraseExt + `)(?: files| code)?`,
			`(?:lista|liste|list|mostra|show)(?: todos| all)?(?: os| the)? (?:arquivos|files) \.?(?P<ext>` + nuPhraseExt + `) (?:e|and) (?:conta|conte|contar|count)(?: as| the)? (?:linhas|lines)(?: deles| de cada um| de cada| of each| in each)?`,
			`(?:list|show)(?: all)?(?: the)? \.?(?P<ext>` + nuPhraseExt + `) files and count (?:the |their )?lines`,
		}},
	{id: "cria-branch", intent: "git_branch", fixed: map[string]string{"op": "create"}, groups: map[string]string{"name": "name"},
		pats: []string{
			`(?:cria|crie|criar|create|make|faz|abre|abrir)(?: uma| um| a)?(?: nova| new)? (?:branch|ramo)(?: nova| novo)?(?: chamad[oa]| com o nome| de nome| com nome| called| named)?(?: de)? (?P<name>[A-Za-z0-9][\w./-]{0,79})`,
			`(?:nova|new) branch (?:chamada |called |named )?(?P<name>[A-Za-z0-9][\w./-]{0,79})`,
			`git (?:checkout -b|switch -c) (?P<name>[A-Za-z0-9][\w./-]{0,79})`,
		},
		prepare: func(g map[string]string, w *nuWorld) bool {
			switch strings.ToLower(g["name"]) {
			case "nova", "novo", "new", "chamada", "chamado", "called", "named", "pra", "para", "aqui", "branch", "ramo", "isso", "uma", "de":
				return false
			}
			return brGitRefRe.MatchString(g["name"]) && !strings.Contains(g["name"], "..")
		}},
	{id: "stash", intent: "git_stash", fixed: map[string]string{"op": "push"},
		pats: []string{
			`git stash(?: push)?|stash|(?:faz|faca|fazer) (?:um )?stash`,
			`(?:guarda|guarde|guardar|esconde|esconder|salva|salve|joga|coloca|bota|poe) (?:(?:as |minhas |essas |as minhas |todas as )?(?:mudancas|alteracoes|modificacoes)|tudo|isso)(?: ai)? (?:no|num|em um|pro|pra o) stash`,
			`(?:guarda|guarde|guardar|esconde|esconder) (?:as |minhas |essas |as minhas )(?:mudancas|alteracoes|modificacoes)`,
			`stash (?:(?:my |the |all |all my )?changes|everything|it|this|all)`,
			`(?:faz|faca) (?:um )?stash (?:das |de )?(?:minhas |as )?(?:mudancas|alteracoes)`,
		}},
	{id: "descarta-arquivo", intent: "undo", groups: map[string]string{"file": "file"},
		pats: []string{
			`(?:desfaz|desfaca|desfazer|descarta|descarte|descartar|reverte|reverta|reverter|joga fora|undo|discard|revert|restaura|restaure)(?: as| minhas| todas as| the| my| all| all the)? (?:mudancas|alteracoes|modificacoes|changes|edits) (?:do|da|de|no|na|em|in|to|of|from|on) ` + nuPhFile,
			`git (?:restore|checkout --) ` + nuPhFile,
		}},
	{id: "variavel-de-ambiente", intent: "env_get", groups: map[string]string{"name": "name"},
		pats: []string{
			`(?:mostra|mostre|me mostra|qual|qual e|show|show me|print|ver|exibe|echo)(?: o| a| the)?(?: valor| value)?(?: de| do| da| of)? \$?(?P<name>[A-Z][A-Z0-9]*_[A-Z0-9_]+)`,
		},
		prepare: func(g map[string]string, w *nuWorld) bool { _, isFile := nuWorldFile(w, g["name"]); return !isFile }},
	{id: "erros-do-arquivo-de-log", intent: "log_errors_recent", groups: map[string]string{"file": "file"},
		pats: []string{
			`(?:(?:mostra|mostre|me mostra|lista|quais|quais sao|ver|show|show me|list) )?(?:os |the )?(?:erros|errors) (?:do|dos|no|nos|de|in|in the|from|from the) ` + nuPhFile,
		}},
}

// nuPhraseParse returns the phrase reading of text, if a phrase applies.
func nuPhraseParse(text string, w *nuWorld) (Intent, bool) {
	n := fpNormalize(text)
	if n == "" {
		return Intent{}, false
	}
	for _, p := range nuPhrases {
		if p.res == nil {
			for _, pat := range p.pats {
				p.res = append(p.res, lazyRx(`(?i)^(?:`+pat+`)$`))
			}
		}
		for _, re := range p.res {
			m := re.FindStringSubmatch(n)
			if m == nil {
				continue
			}
			g := map[string]string{}
			for i, name := range re.SubexpNames() {
				if name == "" || m[i] == "" {
					continue
				}
				g[strings.TrimRight(name, "0123456789")] = m[i]
			}
			if in, ok := p.build(g, w); ok {
				return in, true
			}
		}
	}
	return Intent{}, false
}

// build validates the captured operands and assembles the intent.
func (p *nuPhraseDef) build(g map[string]string, w *nuWorld) (Intent, bool) {
	if raw, has := g["file"]; has {
		f, ok := nuWorldFile(w, raw)
		if !ok {
			return Intent{}, false
		}
		g["file"] = f
	}
	if s, has := g["sym"]; has {
		switch strings.ToLower(s) {
		case "isso", "isto", "esse", "essa", "este", "esta", "ele", "ela", "aquilo", "this", "that", "these", "those", "there",
			"funcao", "metodo", "classe", "function", "method", "class", "codigo", "code", "arquivo", "file", "projeto", "project":
			return Intent{}, false
		}
	}
	if e, has := g["ext"]; has {
		e = strings.ToLower(e)
		if c := nuPhraseExtCanon[e]; c != "" {
			e = c
		}
		g["ext"] = e
	}
	if p.prepare != nil && !p.prepare(g, w) {
		return Intent{}, false
	}
	in := Intent{Name: p.intent, Slots: map[string]string{}, Confidence: 0.97, Raw: 0.97, Phrase: true,
		Evidence: []string{"frase feita: " + p.id}}
	for k, v := range p.fixed {
		in.Slots[k] = v
	}
	for grp, slot := range p.groups {
		if v := g[grp]; v != "" {
			in.Slots[slot] = v
		}
	}
	// operands derived by prepare (file/dir of "nos logs")
	for _, k := range []string{"file", "dir"} {
		if _, mapped := p.groups[k]; !mapped && g[k] != "" && (p.intent == "text_grep") {
			in.Slots[k] = g[k]
		}
	}
	return in, true
}

// nuPhraseOpenRe: "abre o X" with one loose word.
var nuPhraseOpenRe = lazyRx(`(?i)^(?:abre|abra|abrir|mostra|mostre|me mostra|open|show|show me|cat|ver|le|leia|read|exibe) (?:o |a |the )?(?:arquivo |file )?(?P<f>[\w.-]{4,60})$`)

// nuPhraseRepair runs when the grammar did not understand: a request to open
// a file whose name is one typo away from exactly one project file becomes
// open_file on that file, with the repair stated in the evidence. Below
// brLevel0Min on purpose: -p acts on it (read-only, 0.70) and says what it
// read; nothing that mutates ever stands on this.
func nuPhraseRepair(text string, w *nuWorld) (Intent, bool) {
	n := fpNormalize(text)
	if n == "" || w == nil {
		return Intent{}, false
	}
	m := nuPhraseOpenRe.FindStringSubmatch(n)
	if m == nil {
		return Intent{}, false
	}
	typed := fzFold(m[1])
	best, hits := "", 0
	seen := map[string]bool{}
	for _, e := range w.ents {
		if e.Kind != "file" && e.Kind != "test" || seen[e.Name] {
			continue
		}
		if len([]rune(e.Key)) < 4 {
			continue
		}
		if _, ok := fzNearWord(typed, e.Key); ok && typed != e.Key {
			seen[e.Name] = true
			best = e.Name
			hits++
		}
	}
	if hits != 1 {
		return Intent{}, false
	}
	return Intent{Name: "open_file", Slots: map[string]string{"target": best}, Confidence: 0.8, Raw: 0.8, Phrase: true,
		Evidence: []string{"'" + m[1] + "' lido como " + best + " (nome de arquivo parecido, único no projeto)"}}, true
}

// nuWorldFile resolves a typed file name to a project file: the exact
// relative path, or the ONE file with that base name.
func nuWorldFile(w *nuWorld, name string) (string, bool) {
	if w == nil || name == "" {
		return "", false
	}
	name = strings.TrimPrefix(filepath.ToSlash(name), "./")
	found, n := "", 0
	seen := map[string]bool{}
	for _, e := range w.ents {
		if e.Kind != "file" && e.Kind != "test" || seen[e.Name] {
			continue
		}
		if e.Name == name {
			return e.Name, true
		}
		if !strings.Contains(name, "/") && filepath.Base(e.Name) == name {
			seen[e.Name] = true
			found = e.Name
			n++
		}
	}
	return found, n == 1
}

func nuWorldDir(w *nuWorld, name string) bool {
	if w == nil {
		return false
	}
	name = strings.TrimSuffix(strings.TrimPrefix(filepath.ToSlash(name), "./"), "/")
	for _, e := range w.ents {
		if e.Kind == "dir" && e.Name == name {
			return true
		}
	}
	return false
}

// nuWorldByExt lists the project files with the extension (".log").
func nuWorldByExt(w *nuWorld, ext string) []string {
	var out []string
	if w == nil {
		return out
	}
	seen := map[string]bool{}
	for _, e := range w.ents {
		if (e.Kind == "file" || e.Kind == "test") && !seen[e.Name] && strings.EqualFold(filepath.Ext(e.Name), ext) {
			seen[e.Name] = true
			out = append(out, e.Name)
		}
	}
	return out
}

// nuWorldCommonDir: the one directory all files live in ("" when they differ
// or sit at the root).
func nuWorldCommonDir(files []string) string {
	d := ""
	for i, f := range files {
		fd := filepath.ToSlash(filepath.Dir(f))
		if i > 0 && fd != d {
			return ""
		}
		d = fd
	}
	if d == "." {
		return ""
	}
	return d
}
