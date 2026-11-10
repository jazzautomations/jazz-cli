package main

import (
	"path/filepath"
	"strings"
	"sync"
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

// nuPhInProj: an optional "no projeto"/"in this repo" tail.
const nuPhInProj = `(?: (?:tem|existem|ha))?(?: (?:no|neste|nesse|aqui no|do|in the|in this|in) (?:projeto|repo|repositorio|project|codigo|codebase))?(?: (?:tem|existem))?`

// nuPhCSVIn: an optional "do <arquivo>.csv" / "no csv" tail; nuPhCSVFile
// resolves it (the named file, or the project's only CSV).
const nuPhCSVIn = `(?: (?:do|da|de|no|na|of|in|from|em)(?: the)? (?:csv|` + `(?:arquivo |file )?(?P<file>[\w./-]+\.csv)))?`

func nuPhCSVFile(g map[string]string, w *nuWorld) bool {
	if g["file"] != "" {
		return true
	}
	if all := nuWorldByExt(w, ".csv"); len(all) == 1 {
		g["file"] = all[0]
		return true
	}
	return false
}

const (
	nuPhShow = `(?:(?:mostra|mostre|me mostra|me mostre|exibe|ver|veja|show|show me|print|imprime|da|me da) )?`
	nuPhFile = `(?:arquivo |file )?(?P<file>[\w./-]+)`
	nuPhSym  = `(?:[A-Za-z_][A-Za-z0-9_]*\.)?(?P<sym>[A-Za-z_][A-Za-z0-9_]{2,79})`
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

var (
	nuPhrasesOnce sync.Once
	nuPhrasesList []*nuPhraseDef
)

// nuPhraseAll builds the phrase table on first use (package init stays light).
func nuPhraseAll() []*nuPhraseDef {
	nuPhrasesOnce.Do(func() { nuPhrasesList = nuPhraseBuild() })
	return nuPhrasesList
}

func nuPhraseBuild() []*nuPhraseDef {
	return []*nuPhraseDef{
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
				`(?:(?:list|show|show me|lista|mostra) )?(?:all )?(?:the )?\.?(?P<ext>` + nuPhraseExt + `) files` + nuPhInProj,
				`(?:quais|que) arquivos \.?(?P<ext>` + nuPhraseExt + `)(?: tem| existem| ha| que tem)?` + nuPhInProj,
				`(?:quais|que) (?:sao )?os arquivos \.?(?P<ext>` + nuPhraseExt + `)` + nuPhInProj,
				`(?:which|what) \.?(?P<ext>` + nuPhraseExt + `) files (?:are there|exist|do we have|are)` + nuPhInProj,
			}},
		{id: "maior-arquivo", intent: "file_find", fixed: map[string]string{"sort": "size", "type": "file"},
			pats: []string{
				`(?:qual|quais)(?: e| sao)? (?:o|os) (?:maior|maiores|mais pesado|mais pesados) arquivos?(?: daqui| aqui| do projeto| do repo| da pasta)?|maior arquivo(?: daqui| do projeto)?`,
				`(?:what is|whats|what's|which is|show|show me|find) the (?:biggest|largest) file(?: here| in the project| in this project| in the repo)?|(?:biggest|largest) files?`,
			}},
		{id: "cade-o-arquivo", intent: "file_find", fixed: map[string]string{"type": "file"}, groups: map[string]string{"base": "name"},
			pats: []string{
				`(?:cade|onde (?:esta|fica|ta)|aonde (?:esta|fica|ta)|where is|where's|wheres|localiza|locate|em que pasta (?:esta|fica|ta)) (?:o |a |the )?` + nuPhFile,
			},
			prepare: func(g map[string]string, w *nuWorld) bool { g["base"] = filepath.Base(g["file"]); return true }},
		{id: "conteudo-do-arquivo", intent: "open_file", groups: map[string]string{"file": "target"},
			pats: []string{
				`(?:o que|oque|oq) (?:ta|esta|tem|ha) (?:escrito )?(?:no|na|em|dentro do|dentro da) ` + nuPhFile,
				`(?:what(?:'s| is)|whats) (?:in|inside|written in) (?:the )?` + nuPhFile,
				`(?:` + nuPhShow + `)?(?:o |the )?(?:conteudo|contents?) (?:do|da|de|of) (?:the )?` + nuPhFile,
				`(?:le|leia|ler|read|cat|print) (?:o |a |the )?` + nuPhFile,
			}},
		{id: "titulos-do-markdown", intent: "md_outline", groups: map[string]string{"file": "target"},
			pats: []string{
				`(?:(?:quais|mostra|mostre|lista|me mostra|show|show me|list)(?: sao)?(?: os| as| the)? )?(?:titulos|secoes|cabecalhos|topicos|headings|headers|sections|outline|sumario|indice|toc)(?: do| da| de| of| in| from)? ` + nuPhFile,
			},
			prepare: func(g map[string]string, w *nuWorld) bool {
				l := strings.ToLower(g["file"])
				return strings.HasSuffix(l, ".md") || strings.HasSuffix(l, ".markdown")
			}},
		{id: "onde-define", intent: "find", groups: map[string]string{"sym": "query"},
			pats: []string{
				`(?:onde|aonde) (?:e que )?(?:esta|ta|fica|foi|e) (?:definid[oa]|declarad[oa]|implementad[oa]) (?:a |o )?` + nuPhKind + nuPhSym,
				`(?:onde|aonde) (?:fica|esta|ta) (?:a |o )?(?:funcao |metodo |classe |tipo |struct |teste |function |method |class |type |test )` + nuPhSym,
				`(?:onde|aonde) (?:fica|esta|ta) (?:a |o )?(?P<sym>[A-Za-z_][a-z0-9_]*[A-Z_][A-Za-z0-9_]*)`,
				`(?:definicao|definition|declaracao|declaration) (?:de|da|do|of) ` + nuPhKind + nuPhSym,
			}},
		{id: "funcoes-do-arquivo", intent: "explain", groups: map[string]string{"file": "target"},
			pats: []string{
				`(?:(?:quais|que|lista|liste|list|mostra|mostre|show|show me)(?: sao)?(?: as| os| the)? )?(?:funcoes|metodos|functions|methods|simbolos|symbols|declaracoes)(?: que)?(?: tem| existem| are| ha| estao| defined| definidas)? (?:no|na|em|do|da|in|of|inside) (?:the )?` + nuPhFile,
				`o que (?:o |a )?` + nuPhFile + ` (?:define|declara|exporta)`,
			}},
		{id: "roda-um-teste", intent: "run_tests", groups: map[string]string{"t": "target"},
			pats: []string{
				`(?:roda|rode|rodar|executa|execute|executar|run|testa)(?: so| somente| apenas| only| just)?(?: o| the)?(?: teste| test)? (?P<t>Test[A-Za-z0-9_]+|test_[a-z0-9_]+)(?: so| only| apenas| somente)?`,
				`(?:roda|rode|rodar|executa|execute|run)(?: o| the)?(?: teste| test) (?P<t>Test[A-Za-z0-9_]+|test_[a-z0-9_]+)(?: so| only| apenas)?`,
				`go test -run (?P<t>Test[A-Za-z0-9_]+)`,
			}},
		{id: "extrai-padrao", intent: "text_extract", groups: map[string]string{"file": "file", "kind": "pattern"},
			pats: []string{
				`(?:any|are there(?: any)?|list|show|show me|find|extract|get|tem|ha|quais|quais sao|lista|mostra|extrai|extraia|pega)(?: os| as| the| all| all the| algum| alguns)? ?(?P<kind>ips?|ip addresses|enderecos ip|enderecos de ip|emails?|e-mails?|email addresses|enderecos de email|urls?|links) (?:in|no|na|do|da|from|em|que aparecem no|que aparecem em|dentro do) (?:the )?` + nuPhFile,
			},
			prepare: func(g map[string]string, w *nuWorld) bool {
				k := strings.ToLower(g["kind"])
				switch {
				case strings.Contains(k, "ip"):
					g["kind"] = "ipv4"
				case strings.Contains(k, "mail"):
					g["kind"] = "email"
				default:
					g["kind"] = "url"
				}
				return true
			}},
		{id: "csv-como-json", intent: "csv_to_json", groups: map[string]string{"file": "target"},
			pats: []string{
				nuPhShow + `(?:o |a |the )?` + nuPhFile + ` (?:em|no|como|in|as)(?: formato| format)? json(?: format)?`,
			},
			prepare: func(g map[string]string, w *nuWorld) bool {
				return strings.HasSuffix(strings.ToLower(g["file"]), ".csv")
			}},
		{id: "media-coluna-csv", intent: "csv_sum", fixed: map[string]string{"op": "avg"}, groups: map[string]string{"col": "col", "file": "file"},
			pats: []string{
				`(?:(?:qual|what is|whats|what's)(?: e)? )?(?:a |o |the )?(?:media|average|mean|avg) (?:da|do|de|of)(?: a| the)?(?: coluna| column| campo)? (?P<col>\w{1,40})(?: column| coluna)?` + nuPhCSVIn,
				`(?:calcula|calcule|calculate|compute)(?: a| the)? (?:media|average|mean) (?:da|do|de|of)(?: the)?(?: coluna| column)? (?P<col>\w{1,40})(?: column)?` + nuPhCSVIn,
			}, prepare: nuPhCSVFile},
		{id: "extremo-coluna-csv", intent: "csv_sum", groups: map[string]string{"col": "col", "file": "file", "op": "op"},
			pats: []string{
				`(?:qual|quais|que) (?:\w+ )?(?:tem|possui|esta com) (?:o |a )?(?P<op>maior|menor) (?P<col>\w{1,40})` + nuPhCSVIn,
				`(?:qual|what is|whats|what's)(?: e)? (?:o |a |the )?(?P<op>maior|menor|maximo|minimo|max|min|highest|lowest|largest|smallest|biggest) (?P<col>\w{1,40})` + nuPhCSVIn,
				`(?:which|what) (?:\w+ )?has the (?P<op>highest|lowest|largest|smallest|biggest) (?P<col>\w{1,40})` + nuPhCSVIn,
			},
			prepare: func(g map[string]string, w *nuWorld) bool {
				switch strings.ToLower(g["op"]) {
				case "maior", "maximo", "max", "highest", "largest", "biggest":
					g["op"] = "max"
				default:
					g["op"] = "min"
				}
				return nuPhCSVFile(g, w)
			}},
		{id: "conta-linhas-csv", intent: "csv_sum", fixed: map[string]string{"op": "count"}, groups: map[string]string{"file": "file"},
			pats: []string{
				`quant[oa]s (?P<noun>\w+) (?:tem|ha|existem|sao|tem ai)(?: no| na| em| dentro do| dentro da)? ` + nuPhFile,
				`how many (?P<noun>\w+) (?:are (?:there )?)?(?:in|inside) (?:the )?` + nuPhFile,
				`(?:conta|conte|count)(?: os| as| the)? (?P<noun>registros|pedidos|itens|entradas|rows|records|entries) (?:do|da|de|no|na|in|of) ` + nuPhFile,
			},
			prepare: func(g map[string]string, w *nuWorld) bool {
				switch strings.ToLower(g["noun"]) {
				case "linhas", "lines", "palavras", "words", "caracteres", "characters", "bytes", "colunas", "columns":
					return false // counting text units is wordcount's job, not rows of data
				}
				return strings.HasSuffix(strings.ToLower(g["file"]), ".csv")
			}},
		{id: "chaves-do-env", intent: "env_get", fixed: map[string]string{"op": "keys"}, groups: map[string]string{"envfile": "file"},
			pats: []string{
				`(?:quais|que|lista|liste|list|mostra|mostre|show|show me)(?: sao)?(?: as| os| the)? ?(?:variaveis|chaves|keys|vars|variables|nomes)(?: que)?(?: tem| existem| estao| are| ha)?(?: definidas| defined| set)? (?:no|na|em|do|in|of|from) (?:the )?(?P<envfile>\.env[\w.]*)`,
				`(?:what(?:'s| is)|o que (?:tem|ha)) (?:in|no|na|em) (?:the )?(?P<envfile>\.env[\w.]*)`,
				`(?:which|what) (?:variables|keys|vars) does (?:the )?(?P<envfile>\.env[\w.]*) (?:expect|need|define|have|contain|list|declare|use)`,
				`(?:quais|que) (?:variaveis|chaves) (?:o |a )?(?P<envfile>\.env[\w.]*) (?:espera|precisa|define|tem|declara|usa)`,
			}},
		{id: "variavel-no-env", intent: "env_get", groups: map[string]string{"name": "name"},
			pats: []string{
				`(?:qual|que|what)(?: e)?(?: o valor d[eo]| o| a| the value of)? \$?(?P<name>[A-Z][A-Z0-9_]{1,60}) (?:ta|esta|que ta|que esta|definid[oa]|configurad[oa]|is|is set) (?:no|na|em|in) (?:the )?\.env[\w.]*`,
				`(?:valor|value) (?:de|do|da|of) \$?(?P<name>[A-Z][A-Z0-9_]{1,60}) (?:no|na|em|in) (?:the )?\.env[\w.]*`,
			}},
		{id: "chave-de-config", intent: "config_get", groups: map[string]string{"file": "target"},
			pats: []string{
				`(?:qual|que|what is|whats|what's)(?: e)? (?:a |o |the )?(?P<key>[\w.]{2,60}) (?:configurad[oa]|definid[oa]|setad[oa]|que (?:ta|esta)|set|configured|defined) (?:no|na|em|in) (?:the )?` + nuPhFile,
			},
			prepare: func(g map[string]string, w *nuWorld) bool {
				switch strings.ToLower(filepath.Ext(g["file"])) {
				case ".json", ".yaml", ".yml", ".toml", ".ini":
					return true
				}
				return false
			}},
		{id: "move-pra-pasta", intent: "file_move", fixed: map[string]string{"op": "rename", "type": "file"}, groups: map[string]string{"file": "target", "dest": "dest"},
			pats: []string{
				`(?:move|mova|mover|mv|joga|jogue|coloca|coloque|bota|bote|poe|leva|leve) (?:o |a |the )?` + nuPhFile + ` (?:pra|para|pro|to|into|em|no|na)(?: a| o| the)?(?: pasta| dir| diretorio| folder| dentro de| dentro da| dentro do)* (?P<dest>[\w./-]+)`,
			},
			prepare: func(g map[string]string, w *nuWorld) bool { return nuWorldDir(w, g["dest"]) }},
		{id: "copia-pra-pasta", intent: "file_move", fixed: map[string]string{"op": "copy", "type": "file"}, groups: map[string]string{"file": "target", "dest": "dest"},
			pats: []string{
				`(?:copia|copie|copiar|copy|cp) (?:o |a |the )?` + nuPhFile + ` (?:pra|para|pro|to|into|em|no|na)(?: a| o| the)?(?: pasta| dir| diretorio| folder| dentro de| dentro da| dentro do)+ (?P<dest>[\w./-]+)`,
			},
			prepare: func(g map[string]string, w *nuWorld) bool { return nuWorldDir(w, g["dest"]) }},
		{id: "cria-arquivo-vazio", intent: "create", fixed: map[string]string{"type": "file"}, groups: map[string]string{"newfile": "target"},
			pats: []string{
				`(?:cria|crie|criar|create|make|touch)(?: um| uma| an?| o| the)?(?: novo| new)?(?: arquivo| file)?(?: vazio| empty)?(?: arquivo| file)?(?: chamado| com o nome| de nome| called| named)? (?P<newfile>[\w][\w./-]{0,120}\.[A-Za-z0-9]{1,8})`,
			},
			prepare: func(g map[string]string, w *nuWorld) bool { _, exists := nuWorldFile(w, g["newfile"]); return !exists }},
		{id: "linha-n-do-arquivo", intent: "line_slice", groups: map[string]string{"n": "start", "m": "end", "file": "file"},
			pats: []string{
				nuPhShow + `(?:a |the )?(?:linha|line) (?P<n>\d{1,6}) (?:do|da|de|of|in|from) ` + nuPhFile,
				nuPhShow + `(?:as |the )?(?:linhas|lines) (?P<n>\d{1,6}) (?:a|ate|to|through|-) (?P<m>\d{1,6}) (?:do|da|de|of|in|from) ` + nuPhFile,
			},
			prepare: func(g map[string]string, w *nuWorld) bool {
				if g["m"] == "" {
					g["m"] = g["n"]
				}
				return true
			}},
		{id: "colunas-do-csv", intent: "csv_sum", fixed: map[string]string{"op": "count"}, groups: map[string]string{"file": "file"},
			pats: []string{
				`(?:quais|que)(?: sao as)? colunas (?:tem|ha|existem|do|da|de)(?: o| a| no| na)? ?` + nuPhFile,
				`(?:what|which) columns (?:are in|does|is in)(?: the)? ` + nuPhFile + `(?: have| contain)?`,
				`(?:colunas|columns|cabecalho|header) (?:do|da|de|of) ` + nuPhFile,
			},
			prepare: func(g map[string]string, w *nuWorld) bool {
				return strings.HasSuffix(strings.ToLower(g["file"]), ".csv")
			}},
		{id: "primeiro-erro-do-log", intent: "log_errors_recent", groups: map[string]string{"file": "file", "which": "which"},
			pats: []string{
				`(?:qual|quais|what|which)(?: e| foi| foram| was| is| were)?(?: o| os| the)? ?(?P<which>primeiro|ultimo|first|last)? ?(?:erros?|errors?)(?: que)?(?: apareceu| apareceram| aconteceu| rolou| deu| happened| showed up)? (?:no|na|em|do|in|of|from) (?:the )?` + nuPhFile,
			}},
		{id: "formata-um-arquivo", intent: "format", groups: map[string]string{"file": "file"},
			pats: []string{
				`(?:formata|formate|formatar|format|gofmt)(?: so| somente| apenas| only| just)?(?: o| a| the)? ` + nuPhFile + `(?:[, ]+(?:o resto|e so|so ele|only that|nothing else|leave the rest).*)?`,
			},
			prepare: func(g map[string]string, w *nuWorld) bool { return strings.HasSuffix(g["file"], ".go") }},
		{id: "cria-pasta-nomeada", intent: "create", fixed: map[string]string{"type": "dir"}, groups: map[string]string{"newdir": "target"},
			pats: []string{
				`(?:cria|crie|criar|create|make|mkdir|faz)?(?: uma| a| an?| the)?(?: nova| new)? ?(?:pasta|diretorio|folder|directory|dir)(?: nova| novo)?(?: chamad[oa]| com o nome| de nome| called| named)? (?P<newdir>[\w][\w./-]{0,120})`,
			},
			prepare: func(g map[string]string, w *nuWorld) bool {
				return !brCreateStopWord(strings.ToLower(g["newdir"])) && !nuWorldDir(w, g["newdir"]) && !strings.Contains(g["newdir"], "..")
			}},
		{id: "variavel-do-env-en", intent: "env_get", groups: map[string]string{"lname": "name"},
			pats: []string{
				`(?:what|which) (?P<lname>[a-z][a-z0-9_]{1,40}) (?:does|is)(?: the)? \.env[\w.]* (?:set|setting|use|using|define|have)`,
				`(?:qual|que) (?P<lname>[a-z][a-z0-9_]{1,40}) (?:o |a )?\.env[\w.]* (?:define|usa|seta|tem)`,
			},
			prepare: func(g map[string]string, w *nuWorld) bool { g["lname"] = strings.ToUpper(g["lname"]); return true }},
		{id: "cria-e-muda-branch", intent: "git_branch", fixed: map[string]string{"op": "create"}, groups: map[string]string{"name": "name"},
			pats: []string{
				`(?:cria|crie|criar|create) (?:e |and )(?:ja )?(?:muda|mude|mudar|troca|trocar|vai|ir|switch|checkout|go)(?: to)? (?:pra|para|pro|to)?(?: a| the)? ?(?:branch |ramo )?(?P<name>[A-Za-z0-9][\w./-]{0,79})`,
				`(?:cria|crie|criar|create)(?: a| uma| the| a new)? (?:branch|ramo) (?P<name>[A-Za-z0-9][\w./-]{0,79}) (?:e |and )(?:ja )?(?:muda|troca|vai|switch|checkout|go)(?: to it| pra ela| pra la)?`,
			},
			prepare: func(g map[string]string, w *nuWorld) bool {
				return brGitRefRe.MatchString(g["name"]) && !strings.Contains(g["name"], "..") && strings.ToLower(g["name"]) != "branch"
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
				return len(g["name"]) >= 2 && brGitRefRe.MatchString(g["name"]) && !strings.Contains(g["name"], "..")
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
}

// nuPhraseParse returns the phrase reading of text, if a phrase applies.
func nuPhraseParse(text string, w *nuWorld) (Intent, bool) {
	n := fpNormalize(text)
	if n == "" {
		return Intent{}, false
	}
	if in, ok := nuPhraseMatch(n, w); ok {
		return in, true
	}
	// typos in the phrase's own words ("ultmas 2 linhas", "ond fica"): repair
	// against the phrase vocabulary and try once more. The operands must be
	// in the request exactly as typed, and a repaired reading never carries a
	// write.
	fixed, changed := fzRepairLine(nuPhraseSlang(n), nuPhraseVocab())
	if !changed && fixed == n {
		return Intent{}, false
	}
	in, ok := nuPhraseMatch(fixed, w)
	if !ok || nuPhraseWrites[in.Name] {
		return Intent{}, false
	}
	for _, v := range in.Slots {
		if strings.ContainsAny(v, "./_") || v != strings.ToLower(v) {
			if !strings.Contains(n, filepath.Base(v)) && !strings.Contains(n, v) {
				return Intent{}, false // the repair touched an operand
			}
		}
	}
	in.Confidence, in.Raw = 0.9, 0.9
	in.Evidence = append(in.Evidence, "pedido lido com correção de digitação: \""+fixed+"\"")
	return in, true
}

// nuPhraseWrites: phrase intents that change things.
var nuPhraseWrites = map[string]bool{"git_branch": true, "git_stash": true, "undo": true, "file_move": true, "create": true}

func nuPhraseSlang(n string) string {
	w := strings.Fields(n)
	for i, x := range w {
		switch strings.ToLower(x) {
		case "ond", "aond":
			w[i] = "onde"
		case "oq", "oque":
			w[i] = "o que"
		}
	}
	return strings.Join(w, " ")
}

var (
	nuPhraseVocabOnce sync.Once
	nuPhraseVocabMap  map[string]bool
)

// nuPhraseVocab: the literal words (4+ letters) of the phrase patterns.
func nuPhraseVocab() map[string]bool {
	nuPhraseVocabOnce.Do(func() {
		nuPhraseVocabMap = map[string]bool{}
		for _, p := range nuPhraseAll() {
			for _, pat := range p.pats {
				for _, wd := range fpWordRE.FindAllString(strings.ToLower(pat), -1) {
					nuPhraseVocabMap[wd] = true
				}
			}
		}
	})
	return nuPhraseVocabMap
}

func nuPhraseMatch(n string, w *nuWorld) (Intent, bool) {
	for _, p := range nuPhraseAll() {
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
