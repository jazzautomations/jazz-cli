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
		"find all the files in the file system which have been modified in the last day": "find",
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
		"extrai os ips do log":                   "text_extract",
		"extract all emails from mbox.txt":       "text_extract",
		"últimas 10 linhas do app.log":           "line_slice",
		"lines 5 to 20 of main.go":               "line_slice",
		"quem está usando o arquivo db.sqlite":   "file_users",
		"mostra a tabela de rotas":               "net_route",
		"qual o gateway padrão":                  "net_route",
		"arquivos duplicados na pasta":           "file_dupes",
		"mostra as variáveis de ambiente":        "env_get",
		"diff between dir1 and dir2":             "dir_diff",
		"how often does 'x' appear in app.log":   "text_count",
		"conta quantas linhas tem o app.log":     "wordcount_stats",
		"who last touched main.go":               "git_blame",
		"o que tem dentro do release.zip":        "archive_list",
	}
	for text, want := range right {
		if in := m.Parse(text, w); in.Name != want {
			t.Errorf("%q => %s (%.2f, %v), want %s", text, in.Name, in.Confidence, in.Evidence, want)
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
