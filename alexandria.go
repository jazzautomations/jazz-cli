package main

// alexandria.go — a "Alexandria": a biblioteca persistente de procedimentos
// reutilizáveis (0.16). O que já existia:
//
//   - methods/*.json + overlay: a biblioteca de métodos do cérebro offline
//     (methods.go) — passos sobre capacidades registradas, com verificador.
//   - plancache → distill (plancache.go, distill.go): trajetórias do modelo
//     verificadas e repetidas viram métodos learned_cmd de replay literal.
//
// A Alexandria é o registro CAUSAL acima disso: um procedimento não é só a
// sequência de passos — carrega o que faz ser ele (objetivo, pré-condições,
// dependências, efeitos colaterais, parâmetros tipados, limites de
// capacidade), o que prova que funcionou (verify) e o que desmente uma
// leitura parecida (refutations: casos de falso positivo) e um sucesso
// mentiroso (false_success: o verificador passa mas o objetivo não se
// cumpriu). Cada um nasce de um método embutido (seed), de um plano
// destilado ou de uma sessão — a proveniência conta de onde veio, e as
// contagens wins/fails medem o quanto a história o confirma.
//
// Estados: candidate → active → demoted | retired. O portão (axGate) só
// promove quem tem verificação objetiva, confinamento declarado e o mínimo
// de sucessos; falha verificada decrementa, rebaixa e descarta.
//
// Arquivo: dataDir()/alexandria/library.json (+ index.json derivado).
// Mesma disciplina do overlay: gravação atômica (tmp+rename com fsync),
// some com JAZZ_FORGE=off, --fresh, eval offline e binários de teste.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	axVersion  = 1
	axMaxByte  = 1 << 20
	axMaxProcs = 200
	axMinWins  = 2 // mínimo de sucessos verificados pra entrar (mesma régua da destilação)
	axMaxFails = 2 // falhas verificadas que aposentam o procedimento
	axMaxLook  = 3 // quantos o lookup devolve
)

// ---------- o registro causal ----------

// axParam é um parâmetro tipado do procedimento (um slot do pedido).
type axParam struct {
	Name     string   `json:"name"`               // nome do slot (target, dir, service…)
	Type     string   `json:"type"`               // path | relpath | name | ext | query | int | enum | bool
	Required bool     `json:"required,omitempty"` // sem ele o procedimento não responde
	Default  string   `json:"default,omitempty"`
	Enum     []string `json:"enum,omitempty"`
	Desc     string   `json:"desc,omitempty"`
}

// axLimits é o envelope de capacidade: até onde o procedimento alcança.
type axLimits struct {
	Scope     string   `json:"scope,omitempty"` // project | host | any — onde ele pode tocar
	ReadOnly  bool     `json:"read_only,omitempty"`
	FSWrites  bool     `json:"fs_writes,omitempty"`   // cria/apaga/renomeia no FS
	FSTypes   []string `json:"fs_types,omitempty"`    // file | dir — o que ele sabe manejar
	MaxItems  int      `json:"max_items,omitempty"`   // teto de itens por corrida (mass ops)
	MaxReadMB int      `json:"max_read_mb,omitempty"` // maior leitura aceitável
	NeedsNet  bool     `json:"needs_net,omitempty"`
}

// axRefutation é um teste negativo do lookup: um pedido que PARECE chamar
// este procedimento mas não chama — e o que ele é de verdade.
type axRefutation struct {
	Like    string `json:"like"`              // o pedido parecido (palavras que casam)
	Why     string `json:"why"`               // por que NÃO é este procedimento
	Instead string `json:"instead,omitempty"` // intenção/procedimento certo, quando há
}

// axVerify é como se prova que deu certo — o mesmo mini-linguagem dos
// métodos (brVerify) mais a nota do que cada check realmente estabelece.
type axVerify struct {
	Check string `json:"check"`          // file_exists | file_gone | exit_zero | cmd_ran | script_ok | tests_passed | …
	Arg   string `json:"arg"`            // "$res", "$slot.target"…
	Note  string `json:"note,omitempty"` // o que este check prova (auditoria humana)
}

// axProv é a proveniência: de onde o procedimento veio e como a história o
// tratou. Trust é derivado — nunca declarado.
type axProv struct {
	Source  string `json:"source"`           // seed | distill | session | manual
	Method  string `json:"method,omitempty"` // método methods/*.json de origem
	Plan    string `json:"plan,omitempty"`   // plano pcPlan de origem (destilação)
	Session string `json:"session,omitempty"`
	Wins    int    `json:"wins"`  // usos com sucesso verificado
	Fails   int    `json:"fails"` // falhas verificadas depois de escolhido
	Created int64  `json:"created"`
	Updated int64  `json:"updated"`
}

// axProcedure é o procedimento persistente: causal, não sequência de passos.
type axProcedure struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`   // único, estável — também vira o nome do método
	Family   string         `json:"family"` // fs | git | proj | archive | svc | proc | sys | net | text
	Goal     string         `json:"goal"`   // o objetivo em linguagem humana
	Intent   string         `json:"intent"` // a intenção da gramática que ele serve
	Keys     []string       `json:"keys"`   // palavras normalizadas do objetivo (lookup)
	Params   []axParam      `json:"params,omitempty"`
	Pre      []mhCond       `json:"pre,omitempty"`     // fatos que têm que valer (mesma língua do método)
	Deps     []string       `json:"deps,omitempty"`    // bin.* que precisam existir / outros procedimentos
	Steps    []mhStep       `json:"steps"`             // a execução, sobre capacidades registradas
	Effects  []string       `json:"effects,omitempty"` // efeitos colaterais ("cria X", "sinal TERM no pid")
	Limits   axLimits       `json:"limits,omitempty"`
	Verify   []axVerify     `json:"verify"`
	Refutes  []axRefutation `json:"refutes,omitempty"`  // o que NÃO é este procedimento
	FalseOK  []string       `json:"false_ok,omitempty"` // casos de falso sucesso conhecidos
	UndoHint string         `json:"undo_hint,omitempty"`
	Risk     string         `json:"risk"`
	CostMS   int            `json:"cost_ms,omitempty"`
	State    string         `json:"state"` // candidate | active | demoted | retired
	Prov     axProv         `json:"prov"`
}

// axMethod projeta o procedimento de volta a um mhMethod executável — a
// biblioteca do cérebro só entende métodos; o registro causal é mais rico.
func (p axProcedure) axMethod() mhMethod {
	return mhMethod{
		Name: p.Name, Intent: p.Intent, Doc: p.Goal,
		Pre: p.Pre, Needs: axDepsBins(p), Steps: p.Steps,
		Verify: axChecks(p.Verify), Risk: p.Risk, CostMS: p.CostMS,
	}
}

// axDepsBins são as dependências declaradas como fatos bin.* (o que o
// executor já sabe checar) — o resto é documentação pra auditoria.
func axDepsBins(p axProcedure) []string {
	var out []string
	for _, d := range p.Deps {
		if strings.HasPrefix(d, "bin.") {
			out = append(out, d)
		}
	}
	return out
}

func axChecks(vs []axVerify) []mhCheck {
	out := make([]mhCheck, 0, len(vs))
	for _, v := range vs {
		out = append(out, mhCheck{Check: v.Check, Arg: v.Arg})
	}
	return out
}

// ---------- a loja (disco) ----------

type axLib struct {
	Version int           `json:"version"`
	Procs   []axProcedure `json:"procedures"`
}

// axIndex é o índice derivado — recriável a partir da biblioteca, nunca a
// fonte da verdade. Serve pra achar por intenção e por estado sem varrer.
type axIndex struct {
	Version  int                 `json:"version"`
	ByIntent map[string][]string `json:"by_intent"` // intent -> ids ativos+candidatos
	ByState  map[string][]string `json:"by_state"`
}

var axDir = func() string { return filepath.Join(dataDir(), "alexandria") }

func axLibPath() string { return filepath.Join(axDir(), "library.json") }
func axIdxPath() string { return filepath.Join(axDir(), "index.json") }

// axBuildIndex deriva o índice da biblioteca.
func axBuildIndex(l *axLib) *axIndex {
	idx := &axIndex{Version: axVersion, ByIntent: map[string][]string{}, ByState: map[string][]string{}}
	for _, p := range l.Procs {
		if p.State != "retired" {
			idx.ByIntent[p.Intent] = append(idx.ByIntent[p.Intent], p.ID)
		}
		idx.ByState[p.State] = append(idx.ByState[p.State], p.ID)
	}
	for _, ids := range idx.ByIntent {
		sort.Strings(ids)
	}
	return idx
}

// axReadLib lê e valida a biblioteca; arquivo ausente é (nil, nil).
func axReadLib(path string) (*axLib, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, axMaxByte+1))
	if err != nil {
		return nil, err
	}
	if len(b) > axMaxByte {
		return nil, errors.New("biblioteca alexandria maior que 1 MiB")
	}
	var l axLib
	if err := json.Unmarshal(b, &l); err != nil {
		return nil, err
	}
	if l.Version != axVersion {
		return nil, errors.New("versão da biblioteca alexandria desconhecida")
	}
	return &l, nil
}

// axWriteLib grava a biblioteca e o índice derivado, ambos atômicos.
func axWriteLib(path string, l *axLib) error {
	l.Version = axVersion
	if len(l.Procs) > axMaxProcs { // os melhores ficam: estado, depois saldo, depois mais novo
		sort.SliceStable(l.Procs, func(i, j int) bool {
			si := axStateRank(l.Procs[i].State) + l.Procs[i].Prov.Wins - l.Procs[i].Prov.Fails
			sj := axStateRank(l.Procs[j].State) + l.Procs[j].Prov.Wins - l.Procs[j].Prov.Fails
			if si != sj {
				return si > sj
			}
			return l.Procs[i].Prov.Updated > l.Procs[j].Prov.Updated
		})
		l.Procs = l.Procs[:axMaxProcs]
	}
	b, err := json.MarshalIndent(l, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := writeFileAtomic(path, append(b, '\n'), 0o600); err != nil {
		return err
	}
	ib, err := json.MarshalIndent(axBuildIndex(l), "", " ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(filepath.Dir(path), "index.json"), append(ib, '\n'), 0o600)
}

func axStateRank(s string) int {
	switch s {
	case "active":
		return 4
	case "candidate":
		return 2
	case "demoted":
		return 1
	}
	return 0
}

func axFind(l *axLib, key string) *axProcedure {
	for i := range l.Procs {
		p := &l.Procs[i]
		if p.ID == key || p.Name == key || strings.HasPrefix(p.ID, key) {
			return p
		}
	}
	return nil
}

// ---------- validação e o portão de promoção ----------

// axCapExists indireciona a checagem de capacidade: se axValid chamasse
// brCap direto, brCaps→brSelfInfo→mhLibrary→axLoadMethods→axValid→brCap
// fecharia um ciclo de inicialização. O init() amarra a checagem real; o
// fallback pré-init só valida a forma do nome.
var axCapExists func(string) bool

func init() { axCapExists = func(n string) bool { return brCap(n) != nil } }

func axCapOK(name string) bool {
	if axCapExists != nil {
		return axCapExists(name)
	}
	if n, ok := strings.CutPrefix(name, "script:"); ok {
		return scNameRe.MatchString(n)
	}
	return name != ""
}

// axValid é a barra estrutural de TODO procedimento, na escrita e na carga
// — editado à mão ou não: intenção nomeada, passos sobre capacidades que
// existem, learned_cmd dentro da cerca da destilação, verificadores que o
// executor conhece. "" = válido.
func axValid(p axProcedure) string {
	if p.Name == "" || p.Intent == "" || strings.ContainsAny(p.Name+p.Intent, " \t\n") {
		return "nome ou intenção inválido"
	}
	if p.Goal == "" {
		return "procedimento sem objetivo"
	}
	switch p.State {
	case "candidate", "active", "demoted", "retired":
	default:
		return "estado desconhecido: " + p.State
	}
	if len(p.Steps) == 0 || len(p.Steps) > 2*dsMaxSteps {
		return "número de passos fora do permitido"
	}
	outs := map[string]bool{}
	for i, st := range p.Steps {
		if !axCapOK(st.Cap) {
			return fmt.Sprintf("passo %d usa capacidade desconhecida %q", i+1, st.Cap)
		}
		if st.Cap == "learned_cmd" {
			if why := dsCmdOK(st.In["cmd"]); why != "" {
				return fmt.Sprintf("passo %d: %s", i+1, why)
			}
			if !dsReplaySafe(st.In["cmd"]) {
				return fmt.Sprintf("passo %d muta estado, não é re-verificável", i+1)
			}
		}
		if st.Out != "" {
			outs[st.Out] = true
		}
	}
	for _, v := range p.Verify {
		switch v.Check {
		case "file_exists", "file_gone", "read_ok", "exit_zero", "cmd_ran",
			"status_obtained", "script_ok", "script_exit_zero", "profile_present", "tests_passed":
		default:
			return "verificador desconhecido: " + v.Check
		}
		if arg, ok := strings.CutPrefix(v.Arg, "$"); ok && !strings.HasPrefix(arg, "slot.") && arg != "goal" && arg != "hint" && !outs[arg] {
			return "verificador aponta um passo inexistente: " + v.Arg
		}
	}
	for _, c := range p.Pre {
		if c.Fact == "" || (c.Op != "" && c.Op != "set" && c.Op != "unset" && c.Op != "eq" && c.Op != "ne") {
			return "pré-condição mal formada"
		}
	}
	if why := axPoison(p); why != "" {
		return why
	}
	return ""
}

// axPoison é a mesma cerca de envenenamento do plancache/lições: objetivo,
// efeitos, dica de undo e comandos embutidos não podem virar vetor.
// Referências "$var"/"$slot.x" não são texto — são apontamentos de
// variável, então saem da varredura (senão "$slot.to" lia como host.tld).
var axReVar = lazyRx(`\$[A-Za-z_.][A-Za-z0-9_.]*`)

func axPoison(p axProcedure) string {
	parts := []string{p.Goal, p.UndoHint}
	parts = append(parts, p.Effects...)
	parts = append(parts, p.FalseOK...)
	for _, st := range p.Steps {
		for _, v := range st.In {
			parts = append(parts, v)
		}
	}
	for _, s := range parts {
		if r := h2PoisonReason(axReVar.R().ReplaceAllString(s, "")); r != "" {
			return r
		}
	}
	return ""
}

// axGate é o portão de promoção: o que separa candidate de active.
// Regras, na ordem — "" = pode promover:
//
//  1. verificação de objetivo: sem check, "deu certo" é palavra — não entra;
//  2. confinamento: o escopo tem que ser declarado; quem escreve no FS ou
//     corre risco ≥ med precisa de dica de undo (o shadow-git cobre, mas a
//     biblioteca exige que o caminho de volta esteja escrito);
//  3. repetição: saldo wins-fails ≥ axMinWins — igual à destilação, um
//     procedimento se aprende quando verificou E se repetiu, e cada falha
//     desfaz a evidência de um sucesso;
//  4. descarte: fails ≥ axMaxFails aposenta direto.
func axGate(p axProcedure) string {
	if why := axValid(p); why != "" {
		return "inválido: " + why
	}
	if p.Prov.Fails >= axMaxFails {
		return fmt.Sprintf("aposentado: %d falhas verificadas", p.Prov.Fails)
	}
	if len(p.Verify) == 0 {
		return "sem verificação de objetivo — não dá pra provar que deu certo"
	}
	if p.Limits.Scope == "" {
		return "sem confinamento declarado (limits.scope)"
	}
	if (p.Limits.FSWrites || cbParseRisk(p.Risk) >= cbMed) && p.UndoHint == "" {
		return "muta estado sem dica de undo"
	}
	if net := p.Prov.Wins - p.Prov.Fails; net < axMinWins {
		return fmt.Sprintf("saldo de sucessos %d — precisa ≥%d", max(net, 0), axMinWins)
	}
	return ""
}

// axTrust é o fator de confiança bayesiano (suavizado): 2/0 ≈ 0.8, 10/0 ≈ 0.92.
func axTrust(p axProcedure) float64 {
	return float64(p.Prov.Wins+1) / float64(p.Prov.Wins+p.Prov.Fails+2)
}

// axRecordUse conta um uso: sucesso verificado soma win; falha verificada
// soma fail — e aí o portão decide o estado de novo. Um ativo que falha
// rebaixa pra demoted (precisa reconquistar axMinWins); axMaxFails falhas
// aposentam. Devolve o estado novo.
func axRecordUse(p *axProcedure, verifiedOK bool) string {
	p.Prov.Updated = time.Now().Unix()
	if p.State == "retired" {
		return p.State // aposentado não volta
	}
	if verifiedOK {
		p.Prov.Wins++
		if axGate(*p) == "" {
			p.State = "active"
		}
		return p.State
	}
	p.Prov.Fails++
	if p.Prov.Fails >= axMaxFails {
		p.State = "retired"
	} else if p.State == "active" {
		p.State = "demoted"
	}
	return p.State
}

// axUse registra o resultado de uma corrida pelo nome do método que rodou —
// o hook do cérebro (brRunIntent). Erros são ignorados: a contabilidade não
// pode derrubar a resposta.
func axUse(methodName string, verifiedOK bool) {
	if !nuForgeOn() || methodName == "" {
		return
	}
	path := axLibPath()
	l, err := axReadLib(path)
	if err != nil || l == nil {
		return
	}
	p := axFind(l, methodName)
	if p == nil {
		return
	}
	old := p.State
	axRecordUse(p, verifiedOK)
	if err := axWriteLib(path, l); err != nil {
		dbg("alexandria", map[string]any{"use_error": err.Error()})
		return
	}
	if p.State != old {
		dbg("alexandria", map[string]any{"proc": p.Name, "de": old, "pra": p.State})
	}
}

// ---------- lookup ----------

// axScored é um procedimento com a nota do lookup.
type axScored struct {
	P     axProcedure
	Score float64
	Why   string // o que contribuiu (auditável)
}

// axSlotSig é a assinatura de slots do procedimento: os nomes de parâmetro
// ordenados — com ela o lookup compara pedidos que diferem só nos valores.
func axSlotSig(p axProcedure) []string {
	var s []string
	for _, pr := range p.Params {
		s = append(s, pr.Name)
	}
	sort.Strings(s)
	return s
}

// axSlotCover mede quanto dos parâmetros o pedido preenche: exigidos têm
// que estar todos (senão 0), opcionais somam. Default conta como preenchido.
func axSlotCover(p axProcedure, slots map[string]string) float64 {
	req, got, total := 0, 0, 0
	for _, pr := range p.Params {
		total++
		filled := slots[pr.Name] != "" || pr.Default != "" || !pr.Required
		if pr.Required {
			req++
			if filled {
				got++
			}
		} else if slots[pr.Name] != "" {
			got++
		}
	}
	if req > 0 && got < req {
		return 0 // exigido sem valor e sem default: não responde
	}
	if total == 0 {
		return 1
	}
	return float64(got) / float64(total)
}

// axRefuted: o pedido casa forte com um teste negativo do procedimento?
// Termos do "like" contidos no pedido acima de 60% derrubam o candidato —
// a Alexandria prefere não responder a responder o que não é dela.
func axRefuted(p axProcedure, qTerms map[string]bool) string {
	for _, r := range p.Refutes {
		lt := pcUniq(pcTerms(r.Like))
		if len(lt) == 0 {
			continue
		}
		hit := 0
		for _, w := range lt {
			if qTerms[w] {
				hit++
			}
		}
		if float64(hit) >= 0.6*float64(len(lt)) {
			return fmt.Sprintf("parece %q mas não é: %s", r.Like, r.Why)
		}
	}
	return ""
}

// axLookup acha o procedimento de um pedido: intenção certa vale 1.0,
// cobertura de slots até 0.5, palavras do objetivo até 0.4 — e o histórico
// multiplica (sucesso comprovado puxa pra cima, falha derruba). Só ativos
// entram; refutados saem com o motivo. Se nenhum bate a intenção, uma
// segunda passada tenta a mesma família (última praia, nota teto 0.15).
// Devolve até axMaxLook, melhor antes.
func axLookup(l *axLib, intent string, slots map[string]string, query string) ([]axScored, []string) {
	if l == nil {
		return nil, nil
	}
	qTerms := smSet(pcUniq(pcTerms(query))...)
	qWords := pcUniq(pcTerms(query))
	var refuted []string
	reported := map[string]bool{}
	score := func(p axProcedure, base float64, why string) (axScored, bool) {
		cover := axSlotCover(p, slots)
		if base >= 1.0 && cover == 0 && len(p.Params) > 0 {
			return axScored{}, false // intenção certa mas exigido faltando: outro método resolve
		}
		s := base + 0.5*cover
		if len(p.Params) > 0 {
			if cover >= 1 {
				why += "+slots"
			} else if cover > 0 {
				why += "+slots~"
			}
		}
		if len(qWords) > 0 {
			if kw := pcJaccard(p.Keys, qWords); kw > 0 {
				s += 0.4 * kw
				why += "+palavras"
			}
		}
		s *= axTrust(p) * (1 + 0.05*float64(min(p.Prov.Wins, 10)))
		if r := axRefuted(p, qTerms); r != "" {
			if !reported[p.Name] {
				refuted = append(refuted, p.Name+": "+r)
				reported[p.Name] = true
			}
			return axScored{}, false
		}
		return axScored{P: p, Score: s, Why: why}, true
	}
	// passada 1: a intenção do pedido
	var scored []axScored
	for _, p := range l.Procs {
		if p.State != "active" || p.Intent != intent {
			continue
		}
		if sc, ok := score(p, 1.0, "intenção"); ok {
			scored = append(scored, sc)
		}
	}
	// passada 2 (última praia): mesma família, outra intenção
	if len(scored) == 0 && intent != "" {
		fam := axIntentFamily(intent)
		for _, p := range l.Procs {
			if p.State != "active" || p.Family != fam {
				continue
			}
			if sc, ok := score(p, 0.15, "família"); ok {
				scored = append(scored, sc)
			}
		}
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
		}
		return scored[i].P.Name < scored[j].P.Name
	})
	if len(scored) > axMaxLook {
		scored = scored[:axMaxLook]
	}
	return scored, refuted
}

// axIntentFamily agrupa as intenções da gramática nas mesmas famílias dos
// procedimentos — o lookup de última praia só compara dentro do grupo.
func axIntentFamily(intent string) string {
	switch {
	case strings.HasPrefix(intent, "git_"):
		return "git"
	case strings.HasPrefix(intent, "service_"), strings.HasPrefix(intent, "container_"):
		return "svc"
	case strings.HasPrefix(intent, "net_"), intent == "connect_host", intent == "list_hosts", intent == "list_ports", intent == "port_owner":
		return "net"
	case intent == "list_processes", intent == "kill_process":
		return "proc"
	case intent == "run_tests", intent == "build", intent == "lint", intent == "format", intent == "fix", intent == "dep_graph", intent == "dep_outdated", intent == "diff_apply_check", intent == "update_dep", intent == "install_dep":
		return "proj"
	case strings.HasPrefix(intent, "archive_"):
		return "archive"
	case intent == "disk_usage", intent == "mem_usage", intent == "uptime_load", intent == "env_status", intent == "host_status", intent == "self_info", intent == "fs_recent", intent == "log_errors_recent", intent == "cron_list", intent == "pkg_installed", intent == "net_ifaces":
		return "sys"
	case intent == "find", intent == "symbol_refs", intent == "list_files", intent == "open_file", intent == "create", intent == "rename", intent == "delete_file", intent == "file_snapshot", intent == "large_funcs", intent == "todo_scan", intent == "md_outline":
		return "fs"
	case intent == "text_grep", intent == "csv_to_json", intent == "date_normalize", intent == "json_schema_infer", intent == "wordcount_stats", intent == "table_render", intent == "regex_test", intent == "config_get", intent == "dotenv_check", intent == "hash_verify", intent == "url_parse", intent == "license_scan", intent == "dup_code":
		return "text"
	}
	return ""
}

// ---------- carga no cérebro ----------

// axLoadMethods funde os procedimentos ativos na biblioteca de métodos —
// mesma disciplina do overlay da destilação (chamada de mhLibrary dentro
// do portão nuForgeOn). Um procedimento demoted/retired sai do ar.
func axLoadMethods(lib map[string][]mhMethod) {
	l, err := axReadLib(axLibPath())
	if err != nil || l == nil {
		if err != nil {
			dbg("alexandria", map[string]any{"lib": "ignorada", "err": err.Error()})
		}
		return
	}
	seen := map[string]bool{}
	for _, ms := range lib {
		for _, m := range ms {
			seen[m.Name] = true
		}
	}
	added := 0
	for _, p := range l.Procs {
		if p.State != "active" || added >= dsMaxMethods {
			continue
		}
		if why := axValid(p); why != "" {
			dbg("alexandria", map[string]any{"proc": p.Name, "ignorado": why})
			continue
		}
		if seen[p.Name] {
			continue
		}
		m := p.axMethod()
		m.Priority = -1 // atrás dos embutidos e dos aprendidos: a história decide pelo uso
		lib[p.Intent] = append(lib[p.Intent], m)
		added++
	}
	if added > 0 {
		for k := range lib {
			ms := lib[k]
			sort.SliceStable(ms, func(i, j int) bool { return ms[i].Priority > ms[j].Priority })
			lib[k] = ms
		}
	}
}

// ---------- jazz alexandria ----------

// cmdAlexandria é o `jazz alexandria`: semeia, lista, promove e esquece.
func cmdAlexandria(root string, args []string, w io.Writer) int {
	var seed, js bool
	var forget, promote string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--list", "-l":
			// o default já é listar
		case "--seed":
			seed = true
		case "--json":
			js = true
		case "--promote":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "uso: jazz alexandria --promote <id|nome>")
				return 2
			}
			i++
			promote = args[i]
		case "--forget":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "uso: jazz alexandria --forget <id|nome>")
				return 2
			}
			i++
			forget = args[i]
		default:
			fmt.Fprintln(os.Stderr, "uso: jazz alexandria [--list] [--seed] [--json] [--promote X] [--forget X]")
			return 2
		}
	}
	path := axLibPath()
	l, err := axReadLib(path)
	if err != nil {
		fmt.Fprintln(w, "alexandria:", err)
		return 1
	}
	if l == nil {
		l = &axLib{Version: axVersion}
	}
	switch {
	case seed:
		return axCmdSeed(l, path, js, w)
	case forget != "":
		return axCmdForget(l, path, forget, w)
	case promote != "":
		return axCmdPromote(l, path, promote, w)
	}
	return axCmdList(l, js, w)
}

// axCmdSeed planta os procedimentos-semente (idempotente por nome).
func axCmdSeed(l *axLib, path string, js bool, w io.Writer) int {
	have := map[string]bool{}
	for _, p := range l.Procs {
		have[p.Name] = true
	}
	added := 0
	for _, p := range axSeeds() {
		if have[p.Name] {
			continue
		}
		l.Procs = append(l.Procs, p)
		added++
	}
	if added == 0 {
		fmt.Fprintln(w, "alexandria: sementes já plantadas")
		return 0
	}
	if err := axWriteLib(path, l); err != nil {
		fmt.Fprintln(w, "alexandria: gravando:", err)
		return 1
	}
	if js {
		json.NewEncoder(w).Encode(map[string]any{"seeded": added, "total": len(l.Procs)})
	} else {
		fmt.Fprintf(w, "alexandria: %d procedimento(s) plantados (%d no acervo)\n", added, len(l.Procs))
	}
	return 0
}

// axCmdForget aposenta (não apaga — a proveniência fica).
func axCmdForget(l *axLib, path, key string, w io.Writer) int {
	p := axFind(l, key)
	if p == nil {
		fmt.Fprintf(w, "não achei %q na biblioteca\n", key)
		return 1
	}
	p.State = "retired"
	p.Prov.Updated = time.Now().Unix()
	if err := axWriteLib(path, l); err != nil {
		fmt.Fprintln(w, "alexandria: gravando:", err)
		return 1
	}
	fmt.Fprintf(w, "aposentado: %s (o registro fica — wins %d, fails %d)\n", p.Name, p.Prov.Wins, p.Prov.Fails)
	return 0
}

// axCmdPromote força a promoção de uma candidata que passa o portão — o
// caminho normal é axRecordUse; isto é a alavanca manual do dono.
func axCmdPromote(l *axLib, path, key string, w io.Writer) int {
	p := axFind(l, key)
	if p == nil {
		fmt.Fprintf(w, "não achei %q na biblioteca\n", key)
		return 1
	}
	if why := axGate(*p); why != "" {
		fmt.Fprintf(w, "o portão recusa %s: %s\n", p.Name, why)
		return 1
	}
	p.State = "active"
	p.Prov.Updated = time.Now().Unix()
	if err := axWriteLib(path, l); err != nil {
		fmt.Fprintln(w, "alexandria: gravando:", err)
		return 1
	}
	fmt.Fprintf(w, "promovido: %s → active\n", p.Name)
	return 0
}

// axCmdList mostra o acervo: estado, saldo, objetivo.
func axCmdList(l *axLib, js bool, w io.Writer) int {
	if js {
		enc := json.NewEncoder(w)
		enc.SetIndent("", " ")
		if err := enc.Encode(l); err != nil {
			return 1
		}
		return 0
	}
	if len(l.Procs) == 0 {
		fmt.Fprintln(w, "alexandria: vazia — 'jazz alexandria --seed' planta os procedimentos-semente")
		return 0
	}
	fmt.Fprintf(w, "alexandria: %d procedimento(s)\n", len(l.Procs))
	for _, p := range l.Procs {
		fmt.Fprintf(w, "  %-9s %-28s %s · %d✓ %d✗ · %s\n", p.State, p.Name, p.Intent, p.Prov.Wins, p.Prov.Fails, oneLine(p.Goal, 60))
	}
	return 0
}

// ---------- a ponte da destilação ----------

// axFromPlan projeta um plano destilado (dsCand aceito) num procedimento
// candidato — a Alexandria guarda o que o replay provou: a sequência roda
// de novo e sai verde. O portão decide se entra na hora ou espera uso.
func axFromPlan(c *dsCand, now int64) axProcedure {
	name := c.Method.Name
	verify := []axVerify{{Check: "exit_zero", Arg: "$r1", Note: "o replay inteiro saiu exit 0 de novo"}}
	if len(c.Method.Verify) > 0 {
		verify[0].Arg = c.Method.Verify[0].Arg
	}
	p := axProcedure{
		ID: "ax-" + c.Plan.ID, Name: name, Family: "proj",
		Goal:   "repetir a trajetória verificada: " + oneLine(c.Plan.Task, 120),
		Intent: c.Method.Intent, Keys: c.Plan.Keys,
		Steps:  c.Method.Steps,
		Verify: verify,
		Risk:   c.Method.Risk, CostMS: c.Method.CostMS,
		Limits: axLimits{Scope: "project"},
		State:  "candidate",
		Prov: axProv{Source: "distill", Method: name, Plan: c.Plan.ID,
			Session: c.Plan.Session, Wins: c.Plan.Wins, Created: now, Updated: now},
	}
	for _, st := range c.Method.Steps {
		if !IsReadOnly(st.In["cmd"]) {
			p.Limits.FSWrites = true
			p.UndoHint = "replay registrado: desfazer manualmente se o comando escreveu"
		}
	}
	p.FalseOK = []string{"o replay é literal: verde no projeto de origem não prova que vale em outro contexto"}
	return p
}

// axNoteDistilled grava o procedimento-candidato de um método recém
// destilado — a Alexandria vira o registro causal do que o portão aceitou
// (chamada de dsRun depois do método entrar no overlay).
func axNoteDistilled(c *dsCand) {
	if !nuForgeOn() {
		return
	}
	path := axLibPath()
	l, err := axReadLib(path)
	if err != nil {
		dbg("alexandria", map[string]any{"note_error": err.Error()})
		return
	}
	if l == nil {
		l = &axLib{Version: axVersion}
	}
	p := axFromPlan(c, time.Now().Unix())
	if axFind(l, p.Name) != nil {
		return
	}
	l.Procs = append(l.Procs, p)
	if err := axWriteLib(path, l); err != nil {
		dbg("alexandria", map[string]any{"note_error": err.Error()})
	}
}
