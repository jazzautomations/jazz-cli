package main

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
)

func nuTestWorld(t *testing.T) (*nuModel, *nuWorld) {
	t.Helper()
	m := nuGet()
	if m.loadErr != nil {
		t.Fatal(m.loadErr)
	}
	return m, nuWorldFromDef(m.data.World)
}

// nuPredict is what the caller acts on: the intent when understood, else none.
func nuPredict(in Intent) string {
	if in.Understood() {
		return in.Name
	}
	return "none"
}

// The dataset is the test: >= 95% of the examples land on their intent.
func TestNLUDatasetAccuracy(t *testing.T) {
	m, w := nuTestWorld(t)
	total, ok := 0, 0
	var bad []string
	for _, it := range m.data.Intents {
		for _, ex := range it.Examples {
			total++
			in := m.Parse(ex, w)
			if nuPredict(in) == it.Name {
				ok++
			} else {
				bad = append(bad, it.Name+" <- "+ex+" => "+in.Name+" "+strings.Join(in.Evidence, " | "))
			}
		}
	}
	acc := float64(ok) / float64(total)
	t.Logf("NLU exemplos: %d/%d = %.1f%%", ok, total, acc*100)
	for _, b := range bad {
		t.Log("  " + b)
	}
	if acc < 0.95 {
		t.Fatalf("acurácia %.1f%% < 95%%", acc*100)
	}
}

// Negatives never land on the intent that lists them.
func TestNLUNegatives(t *testing.T) {
	m, w := nuTestWorld(t)
	for _, it := range m.data.Intents {
		for _, neg := range it.Negatives {
			if in := m.Parse(neg, w); nuPredict(in) == it.Name {
				t.Errorf("%q não deveria ser %s (%v)", neg, it.Name, in.Evidence)
			}
		}
	}
}

// The blind set is measured, never tuned on: the number is reported as is.
func TestNLUBlindSet(t *testing.T) {
	m, w := nuTestWorld(t)
	b, err := os.ReadFile("nlu/blind_test.json")
	if err != nil {
		t.Fatal(err)
	}
	var bs struct {
		Items []struct {
			T      string `json:"t"`
			Intent string `json:"intent"`
		} `json:"items"`
	}
	if err := json.Unmarshal(b, &bs); err != nil {
		t.Fatal(err)
	}
	ok := 0
	var bad []string
	for _, x := range bs.Items {
		in := m.Parse(x.T, w)
		if nuPredict(in) == x.Intent {
			ok++
		} else {
			bad = append(bad, x.Intent+" <- "+x.T+" => "+in.Name)
		}
	}
	acc := float64(ok) / float64(len(bs.Items))
	t.Logf("NLU conjunto cego: %d/%d = %.1f%%", ok, len(bs.Items), acc*100)
	sort.Strings(bad)
	for _, x := range bad {
		t.Log("  " + x)
	}
	if len(bs.Items) != 100 {
		t.Fatalf("o conjunto cego deve ter 100 frases, tem %d", len(bs.Items))
	}
	if acc < 0.70 {
		t.Fatalf("acurácia no conjunto cego %.1f%% caiu abaixo do piso de regressão (70%%)", acc*100)
	}
}

// BenchmarkNLUParse: one request against the dataset world (warm model).
func BenchmarkNLUParse(b *testing.B) {
	m := nuGet()
	w := nuWorldFromDef(m.data.World)
	m.Parse("roda os testes", w) // trains the classifier
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		m.Parse("abre o teste do parsr pra mim", w)
	}
}

// Entity-first routing: a generic status verb is scoped to the tool it names.
func TestNLUToolStatusEntityFirst(t *testing.T) {
	m, w := nuTestWorld(t)
	cases := map[string]string{
		"talscle status": "tool_status", "status do tailscale": "tool_status", "tailscale ta rodando?": "tool_status",
		"docker ta de pé?": "tool_status", "como ta o podman": "tool_status", "is docker running": "tool_status",
		"status": "git_status", "git status": "git_status", "status do git": "git_status",
		"status do nginx": "service_status", "status do labbox": "host_status", "status do ambiente": "env_status",
		"lista os containers": "container_list", "qual a versão do docker": "tool_info",
	}
	for text, want := range cases {
		if in := m.Parse(text, w); nuPredict(in) != want {
			t.Errorf("%q => %s (%v), want %s", text, nuPredict(in), in.Evidence, want)
		}
	}
	if in := m.Parse("talscle status", w); in.Slots["tool"] != "tailscale" {
		t.Errorf("slot tool = %q", in.Slots["tool"])
	}
}

// 'responde/respondendo' is host_status only with a host; without one it must
// not land on net_status (the old network false positive of "responde só: ok").
func TestNLURespondeIsNotNetStatus(t *testing.T) {
	m, w := nuTestWorld(t)
	// No host: never net_status; ideally 'none' (honest miss / the model takes it).
	if got := nuPredict(m.Parse("responde só: ok", w)); got == "net_status" {
		t.Errorf("%q caiu em net_status (%v)", "responde só: ok", m.Parse("responde só: ok", w).Evidence)
	}
	// Positives: a real network question and a host that responds.
	cases := map[string]string{
		"a internet caiu?":      "net_status",
		"o macbook responde?":   "host_status",
		"o vps tá respondendo?": "host_status",
	}
	for text, want := range cases {
		if got := nuPredict(m.Parse(text, w)); got != want {
			t.Errorf("%q => %s (%v), want %s", text, got, m.Parse(text, w).Evidence, want)
		}
	}
}

// Single-letter suffix strips need a >= 4 stem: short everyday words used to
// collide into concept stems they never meant ("fila"→fil reached files' stem
// and answered list_files, "toda"→tod reached list, "peso"→pes reached
// uptime). The PTY queue prompt "fila tarefa" must reach the model instead.
func TestNLUShortStemIsNotFiles(t *testing.T) {
	m, w := nuTestWorld(t)
	for _, text := range []string{
		"fila", "fila tarefa", "fila de tarefa", "toda", "peso", "mesa", "casa", "dedo",
	} {
		if in := m.Parse(text, w); in.Confidence >= brReadActMin {
			t.Errorf("%q foi comido por %s (%.2f, %v) — palavra curta não é prova de intent", text, in.Name, in.Confidence, in.Evidence)
		}
	}
	// Positives: real listing requests still parse — the stems that matter
	// ("files"→fil via "es", "arquivo"→arquiv via "o") are unchanged.
	cases := map[string]string{
		"lista os arquivos":       "list_files",
		"mostra as pastas":        "list_files",
		"quais arquivos tem aqui": "list_files",
		"files":                   "list_files",
		"arquivos do projeto":     "list_files",
		"lista a pasta src":       "list_files",
	}
	for text, want := range cases {
		if got := nuPredict(m.Parse(text, w)); got != want {
			t.Errorf("%q => %s (%v), want %s", text, got, m.Parse(text, w).Evidence, want)
		}
	}
}

// A tag de entidade num token fraco não pode corroborar o próprio palpite:
// "cargo" é ferramenta real E radical de "carga", mas a entidade não prova
// que o pedido era sobre uptime — corroboração vem de token forte.
func TestNLUWeakEntityDoesNotSelfCorroborate(t *testing.T) {
	m, w := nuTestWorld(t)
	if in := m.Parse("cargo", w); in.Confidence >= nuMinConf {
		t.Errorf("cargo => %s (%.2f) — entidade no token fraco não prova o conceito deduzido", in.Name, in.Confidence)
	}
	if got := nuPredict(m.Parse("qual a carga da máquina", w)); got != "uptime_load" {
		t.Errorf("carga da máquina => %s, want uptime_load", got)
	}
}

// Falsos matches medidos no corpus de 372 pedidos: polissemia de conceito e
// desempate do classificador. Cada um virou recusa honesta (conf < nuMinConf)
// ou o intent da família certa — nunca ação errada em silêncio.
func TestNLUCorpusFalseMatches(t *testing.T) {
	m, w := nuTestWorld(t)
	under := map[string]float64{
		// verbo de compactação não é listar arquivo
		"compacta a pasta logs em tar.gz":         nuMinConf,
		"compress the logs folder into a tarball": nuMinConf,
		// "make" + atributo não é build
		"make script.sh executable": nuMinConf,
		// serviços que falharam no boot é consulta de serviço, não ambiente nem conserto
		"quais serviços falharam no boot": nuMinConf,
		"which services failed at boot":   nuMinConf,
		// entidade espúria não vende tool_info
		"cargo": nuMinConf,
	}
	for text, max := range under {
		if in := m.Parse(text, w); in.Confidence >= max {
			t.Errorf("%q => %s (%.2f, %v) — falso match do corpus, devia ficar sob %.2f", text, in.Name, in.Confidence, in.Evidence, max)
		}
	}
	right := map[string]string{
		// modificação de arquivo é busca, não status de ambiente/ferramenta
		// file_find é a resposta melhor: descoberta de arquivo por mtime,
		// não busca de conteúdo ('find' cobre texto dentro de arquivos)
		"find all the files in the file system which have been modified in the last day": "file_find",
		// procurar repositório é find, não git_status
		"find the official code repository for each paper in this list": "find",
		// quantidade de cpu é ambiente, não ferramenta
		"how many cpus does this box have": "env_status",
	}
	for text, want := range right {
		if in := m.Parse(text, w); in.Name != want {
			t.Errorf("%q => %s (%v), want intent %s", text, in.Name, in.Evidence, want)
		}
	}
	// guardas: leituras legítimas que os vetos novos não podem matar
	keep := map[string]string{
		"o que tem dentro do zip":    "archive_list",
		"lista o conteúdo do tar.gz": "archive_list",
		"tem o git instalado?":       "tool_info",
		"status do sistema":          "env_status",
		"lista os arquivos":          "list_files",
	}
	for text, want := range keep {
		if in := m.Parse(text, w); in.Name != want {
			t.Errorf("guarda %q => %s (%v), want intent %s", text, in.Name, in.Evidence, want)
		}
	}
}

// v5: o cardápio de arquivos/texto/permissões vira caminho do cérebro.
// Os intents novos cobrem a classe nl2bash (find-by-attr, permissões,
// fatia de linhas, contagem, extração) sem engolir os vizinhos.
func TestNLUV5Wiring(t *testing.T) {
	m, w := nuTestWorld(t)
	right := map[string]string{
		"find empty files":                       "file_find",
		"procura arquivos vazios":                "file_find",
		"achar pastas vazias":                    "file_find",
		"qual o arquivo mais novo da pasta logs": "file_find",
		"maiores arquivos da pasta":              "file_find",
		"arquivos com permissão 777":             "perm_find",
		"world-writable files":                   "perm_find",
		"find suid binaries":                     "perm_find",
		"executable files":                       "perm_find",
		"arquivos do usuário matheus":            "perm_find",
		// perm_find group/owner negações — cluster 72 falhas do v8
		"files owned by group staff":         "perm_find",
		"arquivos do grupo admin":            "perm_find",
		"files not owned by wnj":             "perm_find",
		"files with no owner":                "perm_find",
		"files with no group":                "perm_find",
		"files owned by user apache":         "perm_find",
		"files belonging to group developer": "perm_find",
		"files with permissions not 777":     "perm_find",
		"files without execute permission":   "perm_find",
		"files not world-writable":           "perm_find",
		// mass-delete separa conjunto de alvo-descrito; leitura não vira delete
		"apaga todos os arquivos .tmp":         "delete_file",
		"delete all the .log files":            "delete_file",
		"delete files named *.log":             "delete_file",
		"apaga os diretórios vazios":           "delete_file",
		"extrai os ips do log":                 "text_extract",
		"extract all emails from mbox.txt":     "text_extract",
		"últimas 10 linhas do app.log":         "line_slice",
		"lines 5 to 20 of main.go":             "line_slice",
		"quem está usando o arquivo db.sqlite": "file_users",
		"mostra a tabela de rotas":             "net_route",
		"qual o gateway padrão":                "net_route",
		"arquivos duplicados na pasta":         "file_dupes",
		"mostra as variáveis de ambiente":      "env_get",
		"diff between dir1 and dir2":           "dir_diff",
		"how often does 'x' appear in app.log": "text_count",
		"conta quantas linhas tem o app.log":   "wordcount_stats",
		"who last touched main.go":             "git_blame",
		"o que tem dentro do release.zip":      "archive_list",
	}
	for text, want := range right {
		if in := m.Parse(text, w); in.Name != want {
			t.Errorf("%q => %s (%.2f, %v), want %s", text, in.Name, in.Confidence, in.Evidence, want)
		}
	}
	// slots do perm_find: owner/group/negações — 'staff' (typo-repair→start)
	// e 'apache' (@service) têm que passar como nomes de usuário
	slotw := map[string]map[string]string{
		"files owned by group staff":       {"group": "staff"},
		"arquivos do grupo admin":          {"group": "admin"},
		"files not owned by wnj":           {"not_owner": "wnj"},
		"files owned by user apache":       {"owner": "apache"},
		"files with no owner":              {"no_owner": "true"},
		"files with no group":              {"no_group": "true"},
		"files with permissions not 777":   {"not_mode": "777"},
		"files without execute permission": {"nx": "true"},
		"files not world-writable":         {"nww": "true"},
		"arquivos com permissão 777":       {"mode": "777"},
		// "no projeto" é "em+o", não negação — o modo é 777, não not_mode
		"quais arquivos tem permissão 777 no projeto": {"mode": "777"},
		"which files are 777 in this project":         {"mode": "777"},
		"files without 777 permission":                {"not_mode": "777"},
	}
	slotw["busca arquivos modificados entre 2 e 10 minutos"] = map[string]string{"newer": "10", "older": "2"}
	slotw["find files modified between 2 and 10 minutes ago"] = map[string]string{"newer": "10", "older": "2"}
	slotw["find files accessed 2 to 6 minutes ago"] = map[string]string{"newer": "6", "older": "2", "tfield": "atime"}
	slicew := map[string]map[string]string{
		"Search for line 111 in file active_record.rb with 2 lines of context": {"start": "109", "end": "113", "file": "active_record.rb"},
		"linha 10 do x com 3 linhas de contexto":                               {"start": "7", "end": "13"},
		"mostra as linhas 5 a 20 do parser.go":                                 {"start": "5", "end": "20", "file": "parser.go"},
	}
	for text, wants := range slicew {
		in := m.Parse(text, w)
		if in.Name != "line_slice" {
			t.Errorf("%q → %s, want line_slice", text, in.Name)
			continue
		}
		for k, v := range wants {
			if in.Slots[k] != v {
				t.Errorf("%q slots %s=%q want %q (%v)", text, k, in.Slots[k], v, in.Slots)
			}
		}
	}
	for text, wants := range slotw {
		in := m.Parse(text, w)
		for k, v := range wants {
			if in.Slots[k] != v {
				t.Errorf("%q slots %s=%q want %q (%v)", text, k, in.Slots[k], v, in.Slots)
			}
		}
	}
	under := map[string]float64{
		// download não é file_find; latência não é tabela de rotas
		"download the latest release tarball":  nuMinConf,
		"baixa o arquivo do release mais novo": nuMinConf,
		"measure latency to the gateway":       nuMinConf,
		"mede a latência pro gateway":          nuMinConf,
		// split/chmod/clean são mutações, não leituras
		"split file data.csv into pieces per 100 lines": nuMinConf,
		"deixa o script.sh executável":                  nuMinConf,
		"torna o script executável":                     nuMinConf,
		// "users logged in" não é file_users (não existe método de sessões)
		"Print a list of unique users who are logged in": nuMinConf,
		"Print a column of CPU usage numbers":            nuMinConf,
		// ssh-login é connect/exec composto, não "quem está logado"
		"Login to \"user@host\" with key \"keyFile\" and execute \"./script\"": nuMinConf,
		// `date` literal é comando de data, não busca por janela temporal
		"date --date=\"222 days ago\" +\"%m\"": nuMinConf,
		// "name server" é termo DNS (dig); 'server' como @file não pode
		// derrubar o veto — consulta a servidor não é inventário local
		"Request IP address of 'myip.opendns.com' from name server 'resolver1.opendns.com'": nuMinConf,
	}
	for text, max := range under {
		if in := m.Parse(text, w); in.Confidence >= max {
			t.Errorf("%q => %s (%.2f, %v) — falso match v5, devia ficar sob %.2f", text, in.Name, in.Confidence, in.Evidence, max)
		}
	}
	// slots derivados dos helpers: os args certos saem das palavras-cue
	if in := m.Parse("últimas 10 linhas do app.log", w); in.Slots["tail"] != "10" {
		t.Errorf("últimas 10 linhas: tail=%q, want 10", in.Slots["tail"])
	}
	if in := m.Parse("linhas 5 a 20 do x", w); in.Slots["start"] != "5" || in.Slots["end"] != "20" {
		t.Errorf("linhas 5 a 20: start=%q end=%q, want 5/20", in.Slots["start"], in.Slots["end"])
	}
	if in := m.Parse("extrai os ips do log", w); in.Slots["pattern"] != "ipv4" {
		t.Errorf("extrai os ips: pattern=%q, want ipv4", in.Slots["pattern"])
	}
	if in := m.Parse("achar pastas vazias", w); in.Slots["type"] != "dir" || in.Slots["empty"] != "true" {
		t.Errorf("pastas vazias: type=%q empty=%q, want dir/true", in.Slots["type"], in.Slots["empty"])
	}
	// helpers resolve antes dos slots genéricos: nem ".log" vira dir, nem
	// "10MB" vira limit, nem "set" vira owner (medidos no corpus)
	if in := m.Parse("acha todos os arquivos .log maiores que 10MB", w); in.Slots["min_size"] != "10485760" || in.Slots["ext"] != "log" || in.Slots["limit"] != "" || in.Slots["dir"] != "" {
		t.Errorf("maiores que 10MB: slots=%v, want min_size=10485760 ext=log", in.Slots)
	}
	if in := m.Parse("find files larger than 100kb", w); in.Slots["min_size"] != "102400" || in.Slots["dir"] != "" || in.Slots["name"] != "" {
		t.Errorf("larger than 100kb: slots=%v, want min_size=102400", in.Slots)
	}
	if in := m.Parse("arquivos com suid setado", w); in.Slots["owner"] != "" || in.Slots["suid"] != "true" {
		t.Errorf("suid setado: slots=%v, want suid=true sem owner", in.Slots)
	}
	if in := m.Parse("arquivos do usuário matheus", w); in.Slots["owner"] != "matheus" {
		t.Errorf("usuário matheus: owner=%q, want matheus", in.Slots["owner"])
	}
}

// An entity tag on a real file/dir object disarms veto concepts the name
// carries: a directory literally called "downloads" is a noun, not the
// verb "baixar". Tools/ports/services keep vetoing (make, 8080, nginx).
func TestNLUEntityNameDoesNotVeto(t *testing.T) {
	m := nuGet()
	if m.loadErr != nil {
		t.Fatal(m.loadErr)
	}
	w := nuWorldFromDef(nuWorldDef{Dirs: []string{"downloads", "logs"}})
	if in := m.Parse("qual o arquivo mais novo da pasta downloads", w); nuPredict(in) != "file_find" {
		t.Errorf("pasta downloads (dir real) vetou file_find: %s conf=%.2f", nuPredict(in), in.Confidence)
	}
	// controle: a palavra sem entidade continua vetoando (download != busca)
	if in := m.Parse("baixa o arquivo mais novo da pasta x", w); nuPredict(in) == "file_find" {
		t.Errorf("'baixa' sem entidade devia vetar file_find, veio %s", nuPredict(in))
	}
	// ferramenta nomeada como verbo continua vetoando ("make" é @tool)
	if in := m.Parse("make script.sh executable", w); nuPredict(in) == "perm_find" {
		t.Errorf("'make' (@tool) não devia perder o veto: %s conf=%.2f", nuPredict(in), in.Confidence)
	}
}

// Perguntas de julgamento ao assistente ("o que tu acha", "o que tu
// melhoraria") são conversa — vão pro modelo, nunca pro cérebro offline.
// 'acha' (opinar) não é 'acha' (encontrar); condicional de julgamento não
// é self_info. Os comandos com as mesmas palavras seguem funcionando.
func TestNLUOpinionIsNotIntent(t *testing.T) {
	m := nuGet()
	if m.loadErr != nil {
		t.Fatal(m.loadErr)
	}
	w := nuWorldFromDef(m.data.World)
	opinions := []string{
		"e o que tu melhoraria nessa porra?",
		"o que tu acha dessa porra?",
		"o que tu melhoraria no jazz",
		"o que você faria diferente",
		"como você melhoraria isso",
		"eai o que tu acha",
		"o que tu acha do erro no log",
		"me diz tua opinião",
		"what do you think about this",
		"what would you change",
	}
	for _, s := range opinions {
		if in := m.Parse(s, w); nuPredict(in) != "none" {
			t.Errorf("%q => %s (%.2f) — opinião tem que ir pro modelo, não virar intent offline", s, in.Name, in.Confidence)
		}
	}
	// comandos legítimos com as mesmas palavras sobrevivem
	if in := m.Parse("acha o arquivo mais novo", w); nuPredict(in) != "file_find" {
		t.Errorf("'acha o arquivo mais novo' devia ser file_find, veio %s", nuPredict(in))
	}
	if in := m.Parse("acha arquivos .log", w); nuPredict(in) != "file_find" && nuPredict(in) != "list_files" && nuPredict(in) != "find" {
		t.Errorf("'acha arquivos .log' devia ser um intent de arquivo, veio %s", nuPredict(in))
	}
	if in := m.Parse("me fala sobre você", w); nuPredict(in) != "self_info" {
		t.Errorf("'me fala sobre você' devia ser self_info, veio %s", nuPredict(in))
	}
	if in := m.Parse("que versão você é", w); nuPredict(in) != "self_info" {
		t.Errorf("'que versão você é' devia ser self_info, veio %s", nuPredict(in))
	}
}

// Corpus false-matches killed at the method/intent layer: every line here was a
// real corpus verdict, now pinned forever.
func TestNLUCorpusMethodLayer(t *testing.T) {
	m := nuGet()
	w := nuWorldFromDef(m.data.World)
	for _, tc := range []struct {
		s    string
		want string
		slot string // optional "k=v" to assert
	}{
		{"show sshd status", "service_status", "service=sshd"},
		{"mostra o status do sshd", "service_status", "service=sshd"},
		{"status do nginx", "service_status", "service=nginx"},
		{"procura 'timeout' em todos os arquivos .conf", "find", "ext=conf"},
		{"procura 'timeout' nos arquivos", "find", "query=timeout"},
		{"mostra quem editou o main.go por último", "git_blame", "target=main.go"},
		{"limpa imagens docker não usadas", "container_prune", ""},
		{"docker image prune", "container_prune", ""},
		{"Display source and mount point of the file system containing /some/path", "disk_usage", "path=/some/path"},
		{"espaço em disco", "disk_usage", ""},
		// janelas temporais: direção vem do comparativo, não de 'ago'/'há'
		{"files modified less than 7 days ago", "file_find", "newer=10080"},
		{"files modified more than 30 days ago", "file_find", "older=43200"},
		{"files modified in the last 3 days", "file_find", "newer=4320"},
		{"arquivos modificados há menos de 7 dias", "file_find", "newer=10080"},
		{"arquivos modificados há mais de 30 dias", "file_find", "older=43200"},
		// tamanho exato converge min=max; comparativo escolhe a direção
		{"find all the files which have the size 40 bytes", "file_find", "min_size=40"},
		{"files smaller than 2 kilobytes", "file_find", "max_size=2048"},
		{"arquivos maiores que 10mb", "file_find", "min_size=10485760"},
		// vocabulário EN de busca em árvore de diretórios
		{"List files ending in .html in the current directory tree", "list_files", ""},
		{"Remove regular files in the current directory tree", "delete_file", ""},
		{"quem está logado", "logged_in", ""},
		{"who is logged in", "logged_in", ""},
		{"lista as sessões ativas", "logged_in", ""},
		{"últimos logins", "logged_in", ""},
		{"who else is on this machine", "logged_in", ""},
	} {
		in := m.Parse(tc.s, w)
		if got := nuPredict(in); got != tc.want {
			t.Errorf("%q => %s (%.2f), want %s", tc.s, got, in.Confidence, tc.want)
			continue
		}
		if tc.slot != "" {
			k, v, _ := strings.Cut(tc.slot, "=")
			if in.Slots[k] != v {
				t.Errorf("%q slot %s=%q, want %q", tc.s, k, in.Slots[k], v)
			}
		}
	}
	// free-form generation/awk/scaffold must refuse honestly, not become
	// generic create/format — the model owns those.
	for _, s := range []string{
		"scaffold the project folder structure",
		"Execute awk script and format the output",
		"make a rust file that also compiles as c++",
	} {
		if in := m.Parse(s, w); nuPredict(in) != "none" {
			t.Errorf("%q => %s (%.2f) — pedido livre tem que ir pro modelo", s, in.Name, in.Confidence)
		}
	}
	// "por último" descreve autoria, nunca file_find
	if in := m.Parse("quem editou por último", w); nuPredict(in) == "file_find" {
		t.Errorf("'quem editou por último' virou file_find — 'último' contaminou")
	}
	// "lista os arquivos .conf" não pode tratar .conf como diretório
	in := m.Parse("lista os arquivos .conf", w)
	if in.Slots["dir"] == ".conf" || in.Slots["dir"] == "conf" {
		t.Errorf("'.conf' voltou a encher slot dir: %v", in.Slots)
	}
	// os donos legítimos do "quem" não podem vazar pro logged_in
	for _, tc := range []struct{ s, want string }{
		{"quem está usando a porta 8080", "port_owner"},
		{"quem está usando o arquivo db.sqlite", "file_users"},
		{"quem é você", "self_info"},
	} {
		if got := nuPredict(m.Parse(tc.s, w)); got != tc.want {
			t.Errorf("%q => %s — logged_in roubou o dono certo", tc.s, got)
		}
	}
}

// text_replace: sufixo de backup não é escopo nem nome-alvo; lista de
// extensões vira name_re; "file names" continua nome-alvo (recusa honesta
// no portão de método).
func TestNLUTextReplaceSlots(t *testing.T) {
	m, w := nuTestWorld(t)
	in := m.Parse("Replace 'company' with 'newcompany' in all files under current directory and keep backups with .bakup extension", w)
	if in.Name != "text_replace" || in.Confidence < nuMinConf {
		t.Fatalf("backup => %s %.2f %v", in.Name, in.Confidence, in.Slots)
	}
	if in.Slots["backup"] != ".bakup" || in.Slots["namefile"] != "" || in.Slots["ext"] == "bakup" {
		t.Errorf("backup slots errados: %v", in.Slots)
	}
	in = m.Parse("Replace all occurrences of '2013 Magento Inc.' with '2012 Magento Inc.' in all files with '.php', '.xml' and '.phtml' extensions under current directory tree", w)
	if in.Slots["exts"] != `\.(php|xml|phtml)$` || in.Slots["to"] != "2012 Magento Inc." {
		t.Errorf("multi-ext slots errados: %v", in.Slots)
	}
	for _, s := range []string{
		"Replace spaces in file names with underscores in all files",
		"Replace 'a' with 'b' in file names",
	} {
		if in = m.Parse(s, w); in.Slots["namefile"] == "" {
			t.Errorf("%q devia marcar namefile: %v", s, in.Slots)
		}
	}
}
