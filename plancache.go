package main

// plancache.go: verified plan cache with hindsight notes
// (docs/research/tocomas-plan-reuse.md A4, docs/research/shepherd-meta-agents.md A3).
//
//   - Store: after a VERIFIED success (--until passed, a quality gate passed,
//     or the last build/test bash exited 0 with nothing changed after it) of
//     at least pcMinSteps model steps, a plan template (task keywords, files
//     touched, approach, dead ends, verify command) goes to
//     dataDir()/plans/<project slug>.jsonl. Never because the model says done.
//   - Reuse: on a new request, BM25 over the keywords in pure Go (no
//     embeddings: Pentium M) picks at most 3 plans, rendered in ≤ 600 tokens
//     as a <system-reminder> after the user's message (append-only: the
//     prompt cache holds).
//   - Evict: a turn that fails objectively after a reuse counts fails++ on
//     the injected plans; two fails evict a plan. A verified win counts wins++.
//   - Hindsight (config "hindsight": on | <provider/model>, default off): one
//     extra tool-less call to the strongest model of the chain writes a
//     ≤ 1500-char hint (right file, approach, dead end) stored with the plan.
//
// Config "plan_cache": on (default) | off; JAZZ_PLAN_CACHE / JAZZ_HINDSIGHT
// override for A/B runs.

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	pcMinSteps      = 12   // shorter runs are not worth a template (JAZZ_PC_MIN_STEPS overrides; harvest uses lower)
	pcMaxInject     = 3    // TOCOMAS: k=3 beats more context
	pcMaxChars      = 2100 // ≈ 600 tokens at 3.5 chars/token
	pcMaxPlans      = 100  // per project
	pcEvictFails    = 2    // fails after reuse that evict a plan
	pcMaxApproach   = 8
	pcMaxDeadEnds   = 4
	hdMaxHint       = 1500
	hdMinQ          = 78 // omni quality prior that counts as a strong model
	hdTimeout       = 90 * time.Second
	hdMaxTrailChars = 9000
	pcMaxCmds       = 12 // full command lines kept for the distill bridge
	pcMaxWitness    = 4  // shape variants kept per plan (distill slots)
)

// pcEnt is one spotted entity of a request: the slot a distill can learn.
type pcEnt struct {
	Kind string `json:"kind"` // dir, file, test, symbol, service...
	Name string `json:"name"` // resolved name
	Raw  string `json:"raw"`  // what the user typed (fill semantics)
}

// pcWitness is one earlier variant of the same shape: its literal request,
// its entity values and the exact commands that run verified.
type pcWitness struct {
	Task string   `json:"task"`
	Ents []pcEnt  `json:"ents,omitempty"`
	Cmds []string `json:"cmds,omitempty"`
}

// pcPlan is one stored plan template.
type pcPlan struct {
	ID     string   `json:"id"`
	Time   int64    `json:"time"`
	Task   string   `json:"task"` // the request, clipped
	Keys   []string `json:"keys"` // normalized keywords of the request
	Shape  string   `json:"shape,omitempty"`
	Ents   []pcEnt  `json:"ents,omitempty"` // entities of the request, in order
	Files  []string `json:"files,omitempty"`
	Steps  []string `json:"steps,omitempty"` // approach: the actions that changed or checked something
	Cmds   []string `json:"cmds,omitempty"`  // the full bash command lines behind Steps (distill.go replays these)
	Dead   []string `json:"dead,omitempty"`  // dead ends to skip
	Verify string   `json:"verify,omitempty"`
	Hint   string   `json:"hint,omitempty"` // hindsight note (A3)
	// Witnesses are the variants that merged into this plan before the
	// current trajectory — the proof a distill slot needs (distill.go).
	Witnesses []pcWitness `json:"witnesses,omitempty"`
	// DsDrop/DsWins guardam o último veredito do portão de destilação e os
	// wins que o plano tinha na hora: o mesmo veredito na mesma evidência
	// não gasta portão de novo; evidência nova (wins/testemunhas) reabre.
	// DsGen é a geração de plataforma do veredito (dsCapGen): uma cap nova
	// sobe a geração e reabre todos os carimbos — o "muta estado" de hoje
	// pode ter encaixe amanhã.
	DsDrop string `json:"dsdrop,omitempty"`
	DsWins int    `json:"dswins,omitempty"`
	DsGen  int    `json:"dsgen,omitempty"`
	NSteps int    `json:"n_steps"` // model steps of the run that produced it
	Wins   int    `json:"wins"`
	Fails  int    `json:"fails"`
	// lesson reuse (lessons_reuse.go): task class and repo signature
	Kind    string `json:"kind,omitempty"` // bgClassify: size + kind, e.g. "M/bugfix"
	Repo    string `json:"repo,omitempty"` // lrRepoSig of the project when stored
	Session string `json:"session,omitempty"`
	// V is the plan schema: below pcSchema the plan went through the 0.14
	// migration (pcMigrate) on load.
	V int `json:"v,omitempty"`
}

// pcSchema 1 (0.14): "verified" needs a real verifier, no literal of a
// text command is kept, and fewer than 2 content words is no task.
const pcSchema = 1

// pcEnabled: JAZZ_PLAN_CACHE > config plan_cache > on.
func pcEnabled(c *Config) bool {
	v := os.Getenv("JAZZ_PLAN_CACHE")
	if v == "" && c != nil {
		v = c.PlanCache
	}
	return !pcIsOff(v)
}

// hdSetting: JAZZ_HINDSIGHT > config hindsight; "" or off = disabled, on =
// strongest model of the chain, anything with a "/" = that model.
func hdSetting(c *Config) string {
	v := strings.TrimSpace(os.Getenv("JAZZ_HINDSIGHT"))
	if v == "" && c != nil {
		v = strings.TrimSpace(c.Hindsight)
	}
	if v == "" || pcIsOff(v) {
		return ""
	}
	return v
}

func pcIsOff(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "off", "0", "false", "no", "none":
		return true
	}
	return false
}

func pcPath(root string) string {
	return filepath.Join(dataDir(), "plans", projectSlug(root)+".jsonl")
}

// pcLoad reads a project's plans (missing file or torn lines are fine).
func pcLoad(root string) []pcPlan {
	f, err := os.Open(pcPath(root))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []pcPlan
	old := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var p pcPlan
		if json.Unmarshal(sc.Bytes(), &p) == nil && p.ID != "" && h2PlanPoison(p) == "" { // poisoned plans are never reused (hardening2.go)
			out = append(out, p)
			old = old || p.V < pcSchema
		}
	}
	if old {
		f.Close()
		out = pcMigrate(root, out)
	}
	return out
}

// pcBadIDs are plans known to be poisoned by the old rules: stored as
// "verified" from turns that failed (d610: "Não consegui arrumar o wifi",
// reinjected in 5 sessions, "oi" included).
var pcBadIDs = smSet("dc2e261ebfea", "55031196017e")

// pcMigrate brings plans stored before pcSchema to the 0.14 rules, once,
// with a backup of the file: known-bad plans, plans "verified" only by a
// probe (ls/stat/wc/test) that names nothing they changed, plans whose
// task is fewer than 2 content words and plans whose reply admitted
// failure go; text-command literals are re-redacted.
func pcMigrate(root string, plans []pcPlan) []pcPlan {
	path := pcPath(root)
	if b, err := os.ReadFile(path); err == nil {
		bak := path + ".pre-0.14.bak"
		if _, err := os.Stat(bak); err != nil {
			_ = os.WriteFile(bak, b, 0o600)
		}
	}
	var keep []pcPlan
	dropped := 0
	for _, p := range plans {
		if p.V >= pcSchema {
			keep = append(keep, p)
			continue
		}
		if why := pcMigrateDrop(p); why != "" {
			dropped++
			dbg("plan_cache", map[string]any{"migrate_drop": p.ID, "why": why})
			continue
		}
		for i := range p.Cmds {
			p.Cmds[i] = nlCredScrub(pcSafeCmd(p.Cmds[i]))
		}
		for i := range p.Steps {
			if rest, ok := strings.CutPrefix(p.Steps[i], "bash "); ok {
				p.Steps[i] = "bash " + pcSafeCmd(rest)
			}
			p.Steps[i] = nlCredScrub(p.Steps[i])
		}
		for i := range p.Witnesses {
			for j := range p.Witnesses[i].Cmds {
				p.Witnesses[i].Cmds[j] = nlCredScrub(pcSafeCmd(p.Witnesses[i].Cmds[j]))
			}
		}
		p.Verify = nlCredScrub(pcSafeCmd(p.Verify))
		p.Keys = pcUniq(pcTerms(strings.Join(p.Keys, " ")))
		p.V = pcSchema
		keep = append(keep, p)
	}
	if err := pcSave(root, keep); err != nil {
		dbg("plan_cache", map[string]any{"migrate_error": err.Error()})
	} else if dropped > 0 {
		auditRec("plan.migrated", map[string]any{"dropped": dropped, "kept": len(keep)})
	}
	return keep
}

// pcMigrateDrop says why an old plan does not survive the 0.14 rules.
func pcMigrateDrop(p pcPlan) string {
	if pcBadIDs[p.ID] {
		return "known_bad"
	}
	if len(pcUniq(pcTerms(p.Task))) < pcMinTerms {
		return "few_terms"
	}
	if pcFailRE.MatchString(p.Task + " " + p.Hint) {
		return "admitted_failure"
	}
	if v := strings.TrimSpace(p.Verify); v != "" && !rcIsBuildTest(v) {
		rc := &rcTurn{mutPaths: append([]string(nil), p.Files...)}
		for _, c := range p.Cmds {
			if !IsReadOnly(c) && !rcIsVerifyCmd(c) {
				rc.mutPaths = append(rc.mutPaths, rcPathWords(c)...)
			}
		}
		if !rcProbeOK(rc, v) {
			return "probe_only"
		}
	}
	return ""
}

// pcSave rewrites the project's plan file atomically (0600).
func pcSave(root string, plans []pcPlan) error {
	p := pcPath(root)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	if len(plans) > pcMaxPlans { // keep the best, then the newest
		sort.SliceStable(plans, func(i, j int) bool {
			si, sj := plans[i].Wins-plans[i].Fails, plans[j].Wins-plans[j].Fails
			if si != sj {
				return si > sj
			}
			return plans[i].Time > plans[j].Time
		})
		plans = plans[:pcMaxPlans]
	}
	var b strings.Builder
	for _, pl := range plans {
		pl.V = pcSchema // written by this version: under its rules (old files migrate on load)
		j, err := json.Marshal(pl)
		if err != nil {
			return err
		}
		b.Write(j)
		b.WriteByte('\n')
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// ---------- keywords and BM25 ----------

var pcStop = smSet(
	// English
	"the", "and", "for", "with", "that", "this", "from", "into", "onto", "are", "was", "were", "you", "your",
	"can", "could", "would", "should", "please", "make", "sure", "all", "any", "some", "use", "using", "not",
	"but", "then", "than", "when", "what", "which", "where", "how", "why", "its", "it's", "have", "has", "had",
	"just", "also", "now", "there", "here", "them", "they", "their", "our", "out", "about", "after", "before",
	"does", "did", "done", "get", "got", "let", "via", "per", "each", "one", "two", "need", "want",
	// Portuguese
	"que", "com", "para", "pra", "por", "uma", "umas", "uns", "dos", "das", "nos", "nas", "num", "numa", "não",
	"nao", "sim", "mais", "menos", "como", "quando", "onde", "porque", "isso", "isto", "esse", "essa", "este",
	"esta", "aqui", "ali", "ser", "ter", "tem", "está", "esta", "estao", "favor", "faz", "faça", "faca", "fazer",
	"tudo", "todo", "toda", "todos", "todas", "seu", "sua", "seus", "suas", "meu", "minha", "depois", "antes",
	"também", "tambem", "agora", "ainda", "já", "pelo", "pela", "pelos", "pelas", "entre", "sobre", "sem",
	// interjections, curse words and "go on" markers: this user curses in
	// greetings and follow-ups ("porra arruma ae essa merda", "bora",
	// "marcha"); as keywords they matched plans on one word (26 injections,
	// none relevant)
	"porra", "caralho", "caraio", "krl", "crl", "merda", "bosta", "foda", "fodase", "marcha", "bora", "borá",
	"vamo", "vamos", "mano", "manow", "ae", "aê", "aí", "então", "entao", "kkk", "kkkk", "kkkkk", "pô",
	"tipo", "cara", "véi", "vei", "mermão", "mermao", "vai", "beleza", "blz", "isso", "ok", "okay", "valeu",
	"tranquilo", "massa", "pode", "manda", "veja", "olha",
	"fala", "diz", "porfavor", "pfv", "pls", "pff", "tá", "ta", "tô", "to", "né", "ne", "hein", "aqui",
	"essa", "esse", "nessa", "nesse", "dessa", "desse", "coisa", "parada", "negócio", "negocio", "treco",
)

// pcMinTerms: a request with fewer content words than this neither looks
// up nor stores a plan — one word ("porra", "marcha") matched anything.
const pcMinTerms = 2

// pcFailRE: a final reply that says it did not work. A turn that ends
// like this is never stored, and a plan reused in it counts as a fail.
var pcFailRE = lazyRx(`(?i)(n[ãa]o consegui|n[ãa]o foi poss[íi]vel|n[ãa]o deu (certo|pra)|falhou|falharam|precisa(ria)? de root|precisa de sudo|bloquead|nenhuma altera[çc][ãa]o|couldn'?t|could not|unable to|failed to|did not work|didn'?t work)`)

// pcFold lowercases and strips the common Latin accents.
func pcFold(r rune) rune {
	switch r {
	case 'á', 'à', 'â', 'ã', 'ä':
		return 'a'
	case 'é', 'è', 'ê', 'ë':
		return 'e'
	case 'í', 'ì', 'î', 'ï':
		return 'i'
	case 'ó', 'ò', 'ô', 'õ', 'ö':
		return 'o'
	case 'ú', 'ù', 'û', 'ü':
		return 'u'
	case 'ç':
		return 'c'
	}
	return r
}

// pcTerms tokenizes text into normalized keywords (order kept, with repeats).
func pcTerms(s string) []string {
	s = strings.ToLower(s)
	var out []string
	for _, w := range strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' }) {
		if pcStop[w] {
			continue
		}
		w = strings.Map(pcFold, w)
		if len(w) < 3 || pcStop[w] {
			continue
		}
		if len(w) > 4 && strings.HasSuffix(w, "s") { // tests/testes, files
			w = strings.TrimSuffix(w, "s")
		}
		if len(w) > 4 && strings.HasSuffix(w, "e") && !strings.HasSuffix(w, "ee") { // teste/test
			w = strings.TrimSuffix(w, "e")
		}
		out = append(out, w)
	}
	return out
}

func pcUniq(l []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range l {
		if !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

// pcDocTerms are the indexed terms of a plan: request keywords plus the
// names of the files it touched.
func pcDocTerms(p pcPlan) []string {
	t := append([]string(nil), p.Keys...)
	for _, f := range p.Files {
		t = append(t, pcTerms(strings.TrimSuffix(filepath.Base(f), filepath.Ext(f)))...)
	}
	return t
}

// pcMatch ranks plans for a request with BM25 and returns at most k that
// share enough distinct keywords with it: ≥ 2 and ≥ half of the query
// (a query of fewer than 2 content words matches nothing).
func pcMatch(plans []pcPlan, query string, k int) []pcPlan {
	q := pcUniq(pcTerms(query))
	if len(q) == 0 || len(plans) == 0 {
		return nil
	}
	docs := make([][]string, len(plans))
	df := map[string]int{}
	total := 0
	for i, p := range plans {
		docs[i] = pcDocTerms(p)
		total += len(docs[i])
		for _, w := range pcUniq(docs[i]) {
			df[w]++
		}
	}
	avg := float64(total) / float64(len(plans))
	if avg == 0 {
		avg = 1
	}
	if len(q) < pcMinTerms {
		return nil
	}
	need := max(pcMinTerms, (len(q)+1)/2) // ≥ 2 shared terms and ≥ half of the query
	const k1, b = 1.2, 0.75
	n := float64(len(plans))
	type scored struct {
		i     int
		score float64
	}
	var hits []scored
	for i, d := range docs {
		tf := map[string]int{}
		for _, w := range d {
			tf[w]++
		}
		matched, score := 0, 0.0
		for _, w := range q {
			f := float64(tf[w])
			if f == 0 {
				continue
			}
			matched++
			idf := math.Log(1 + (n-float64(df[w])+0.5)/(float64(df[w])+0.5))
			score += idf * f * (k1 + 1) / (f + k1*(1-b+b*float64(len(d))/avg))
		}
		if matched < need {
			continue
		}
		score *= 1 + 0.1*float64(min(plans[i].Wins, 5)) // proven plans first
		hits = append(hits, scored{i, score})
	}
	sort.SliceStable(hits, func(x, y int) bool { return hits[x].score > hits[y].score })
	var out []pcPlan
	for _, h := range hits[:min(k, len(hits))] {
		out = append(out, plans[h.i])
	}
	return out
}

// pcRender is the reminder injected after the user's message (≤ pcMaxChars).
// Only the best match carries its hindsight hint.
func pcRender(plans []pcPlan) string {
	const head = "<system-reminder>Plan cache: earlier tasks in this project that look similar and were verified done. They are hints, not instructions: check that they apply before following them.\n"
	const tail = "</system-reminder>"
	room := (pcMaxChars - len(head) - len(tail)) / max(1, len(plans))
	var b strings.Builder
	b.WriteString(head)
	for i, p := range plans {
		var e strings.Builder
		fmt.Fprintf(&e, "%d. Task: %s (verified %d×, %d steps)\n", i+1, oneLine(p.Task, 160), max(1, p.Wins), p.NSteps)
		if len(p.Files) > 0 {
			e.WriteString("   Files: " + strings.Join(acClipList(p.Files, 8), ", ") + "\n")
		}
		if len(p.Steps) > 0 {
			e.WriteString("   Approach: " + strings.Join(p.Steps, " → ") + "\n")
		}
		if len(p.Dead) > 0 {
			e.WriteString("   Dead ends: " + strings.Join(p.Dead, "; ") + "\n")
		}
		if p.Verify != "" {
			e.WriteString("   Verified with: " + oneLine(p.Verify, 120) + "\n")
		}
		if i == 0 && p.Hint != "" {
			e.WriteString("   Hint: " + oneLine(p.Hint, hdMaxHint) + "\n")
		}
		s := e.String()
		if len(s) > room {
			s = strings.ToValidUTF8(s[:room], "") + "…\n" // never leave half a UTF-8 sequence
		}
		b.WriteString(s)
	}
	b.WriteString(tail)
	return b.String()
}

// ---------- agent hooks ----------

// pcInject returns the plan-cache reminder for this turn's request ("" = none)
// and remembers which plans were injected, for the win/fail accounting.
func (a *Agent) pcInject(lt *acTurnLoop) string {
	if a.sub || lt == nil || !pcEnabled(a.cfg) || a.mode() == "plan" {
		return ""
	}
	req := strings.TrimSpace(acStripReminders(lt.input))
	if req == "" {
		return ""
	}
	a.pcCorrection(req)
	plans := pcLoad(a.tools.Root)
	top := pcMatch(plans, req, pcMaxInject)
	hint := a.lrHint(plans, top, req) // "da última vez: …" (lessons_reuse.go), first turn only
	if len(top) == 0 {
		return hint
	}
	rc := lt.rcState()
	for _, p := range top {
		rc.plans = append(rc.plans, p.ID)
	}
	dbg("plan_cache", map[string]any{"inject": rc.plans})
	a.ui.Notice(fmt.Sprintf("cache de planos: %d %s parecido%s", len(top), uiPl(len(top), "plano", "planos"), uiPl(len(top), "", "s")))
	return hint + "\n\n" + pcRender(top)
}

// pcFailed: stop reasons that count as an objective failure of a reused plan.
func pcFailed(stop StopReason) bool {
	switch stop {
	case StopUntilFailed, StopLoop, StopStepLimit, StopUnverified, StopTruncated, StopQualityFailed:
		return true
	}
	return false
}

// acTurnDone runs when a turn ends: plan cache accounting and storage, and
// the optional hindsight hint.
func (a *Agent) acTurnDone(ctx context.Context, lt *acTurnLoop, stop StopReason, steps int) {
	if a.sub || lt == nil || !pcEnabled(a.cfg) {
		return
	}
	rc := lt.rcState()
	if len(rc.plans) > 0 {
		a.pcPrev = pcPrevTurn{ids: append([]string(nil), rc.plans...), req: strings.TrimSpace(acStripReminders(lt.input))}
	}
	verified := stop == StopDone && (rc.untilPassed || rc.gateOK || (a.loop.opts.Until == "" && rc.verifyOK))
	// "Não consegui arrumar o wifi" after an `ls` exit 0 is not a success,
	// whatever the checks said (d610 dc2e261ebfea)
	saidFail := pcFailRE.MatchString(a.pcFinalReply())
	if saidFail {
		verified = false
	}
	root := a.tools.Root
	if len(rc.plans) > 0 && (verified || saidFail || pcFailed(stop)) {
		ids := rc.plans
		if verified { // a win only for a plan the turn actually followed
			ids = pcFollowed(root, ids, a.pcTrajectory(lt))
		}
		if err := pcScore(root, ids, verified); err != nil {
			dbg("plan_cache", map[string]any{"score_error": err.Error()})
		}
	}
	if !verified || steps < pcStepsFloor() || ctx.Err() != nil || a.mode() == "plan" {
		return
	}
	p := a.pcBuild(lt, steps)
	if len(p.Keys) < pcMinTerms {
		dbg("plan_cache", map[string]any{"skip": "few_terms"})
		return
	}
	if hdSetting(a.cfg) != "" {
		p.Hint = a.hdWrite(ctx, lt, p)
	}
	if err := pcStore(root, p, rc.plans); err != nil {
		dbg("plan_cache", map[string]any{"store_error": err.Error()})
		return
	}
	dbg("plan_cache", map[string]any{"stored": p.ID, "steps": steps, "hint": p.Hint != ""})
	dsNote(a, root, p) // a ponte online→offline avisa quando o plano vira candidato
}

// pcPrevTurn remembers the plans injected in the last turn and its request.
type pcPrevTurn struct {
	ids []string
	req string
}

// pcCorrection: the request right after a turn that got plans injected
// says whether they helped. An explicit negation at the start ("não, to
// falando da sessão do claude") or the same request again (Jaccard ≥ 0.6)
// counts a fail on each of them and records a "rejected" signal. A curse
// word alone is not a correction ("porra, ficou bom").
func (a *Agent) pcCorrection(req string) {
	prev := a.pcPrev
	a.pcPrev = pcPrevTurn{}
	if len(prev.ids) == 0 {
		return
	}
	how := ""
	switch {
	case syIsCorrection(req):
		how = "negation"
	case pcJaccard(pcUniq(pcTerms(req)), pcUniq(pcTerms(prev.req))) >= 0.6 && len(pcUniq(pcTerms(req))) >= pcMinTerms:
		how = "repeat"
	default:
		return
	}
	if err := pcScore(a.tools.Root, prev.ids, false); err != nil {
		dbg("plan_cache", map[string]any{"score_error": err.Error()})
	}
	dbg("plan_cache", map[string]any{"corrected": prev.ids, "how": how})
	if sigEnabled() {
		s := sigOf("rejected", prev.req, Intent{}, map[string]string{"how": "next_msg", "kind": how, "plans": strings.Join(prev.ids, ",")})
		s.Class = "plan"
		_ = sigAppend(s)
	}
}

// pcFinalReply is the text of the turn's last assistant message.
func (a *Agent) pcFinalReply() string {
	for i := len(a.Msgs) - 1; i >= 0; i-- {
		switch a.Msgs[i].Role {
		case "assistant":
			if strings.TrimSpace(a.Msgs[i].Content) != "" {
				return a.Msgs[i].Content
			}
		case "user":
			return ""
		}
	}
	return ""
}

// pcTrajectory is what the turn touched: changed files and bash commands.
func (a *Agent) pcTrajectory(lt *acTurnLoop) []string {
	var out []string
	if a.tools != nil {
		out = append(out, a.tools.rcTurnFiles()...)
	}
	for _, e := range lt.rcState().log {
		if e.tool == "bash" || e.mut {
			out = append(out, e.target)
		}
	}
	return out
}

// pcTrajTerms are the comparable words of files and commands.
func pcTrajTerms(items []string) []string {
	var out []string
	for _, it := range items {
		for _, w := range strings.Fields(it) {
			out = append(out, pcTerms(strings.TrimSuffix(filepath.Base(w), filepath.Ext(w)))...)
			out = append(out, pcTerms(w)...)
		}
	}
	return pcUniq(out)
}

// pcFollowed keeps the injected plans whose files and commands overlap
// what the turn did (Jaccard ≥ 0.3): an injected plan the model ignored
// does not earn a win from someone else's success.
func pcFollowed(root string, ids []string, traj []string) []string {
	tt := pcTrajTerms(traj)
	if len(tt) == 0 {
		return nil
	}
	want := smSet(ids...)
	var out []string
	for _, p := range pcLoad(root) {
		if !want[p.ID] {
			continue
		}
		if pcJaccard(pcTrajTerms(append(append([]string(nil), p.Files...), p.Cmds...)), tt) >= 0.3 {
			out = append(out, p.ID)
		}
	}
	return out
}

// pcStepsFloor: JAZZ_PC_MIN_STEPS > pcMinSteps. The harvest pipeline lowers it
// so a verified short procedure can still become a witness.
func pcStepsFloor() int {
	if v, err := strconv.Atoi(os.Getenv("JAZZ_PC_MIN_STEPS")); err == nil && v > 0 {
		return v
	}
	return pcMinSteps
}

// pcScore counts a verified win or a failure on the plans injected in a turn;
// pcEvictFails failures evict a plan.
func pcScore(root string, ids []string, win bool) error {
	plans := pcLoad(root)
	used := smSet(ids...)
	changed := false
	var keep []pcPlan
	for _, p := range plans {
		if used[p.ID] {
			changed = true
			if win {
				p.Wins++
			} else {
				p.Fails++
			}
			if p.Fails >= pcEvictFails {
				dbg("plan_cache", map[string]any{"evict": p.ID, "fails": p.Fails})
				continue
			}
		}
		keep = append(keep, p)
	}
	if !changed {
		return nil
	}
	return pcSave(root, keep)
}

// pcAddWitness records a displaced variant of a merged plan (cap
// pcMaxWitness, newest first dropped last). skipTask dedups the variant
// that is the stored trajectory itself.
func pcAddWitness(ws []pcWitness, w pcWitness, skipTask string) []pcWitness {
	if w.Task == "" || w.Task == skipTask {
		return ws
	}
	for _, x := range ws {
		if x.Task == w.Task {
			return ws
		}
	}
	ws = append(ws, w)
	if len(ws) > pcMaxWitness {
		ws = ws[len(ws)-pcMaxWitness:]
	}
	return ws
}

// pcStore adds p, or refreshes a plan for the same task — same keywords
// (Jaccard ≥ 0.7) or the same masked shape ("comprime @dir" covers
// "comprime docs" and "comprime src"). A displaced variant is kept as a
// witness: it is the proof distill needs before parameterizing commands.
// A plan reused in this turn already got its win from pcScore.
func pcStore(root string, p pcPlan, reused []string) error {
	if why := h2PlanPoison(p); why != "" {
		auditRec("plan.rejected", map[string]any{"reason": why, "task": oneLine(p.Task, 120)})
		return nil // never written (hardening2.go)
	}
	plans := pcLoad(root)
	was := smSet(reused...)
	for i, old := range plans {
		sameShape := old.Shape != "" && old.Shape == p.Shape
		if !sameShape && pcJaccard(old.Keys, p.Keys) < 0.7 {
			continue
		}
		if !was[old.ID] {
			old.Wins++
		}
		if p.NSteps <= old.NSteps || old.NSteps == 0 { // keep the shorter trajectory
			hint := old.Hint
			old.Witnesses = pcAddWitness(old.Witnesses, pcWitness{Task: old.Task, Ents: old.Ents, Cmds: old.Cmds}, p.Task)
			old.Task, old.Keys, old.Files, old.Steps, old.Cmds, old.Dead, old.Verify, old.NSteps = p.Task, p.Keys, p.Files, p.Steps, p.Cmds, p.Dead, p.Verify, p.NSteps
			// Shape/Ents descrevem a trajetória guardada: trocam juntos
			// (p.Task ficou), nunca com os dados do plano que não entrou.
			old.Shape, old.Ents = p.Shape, p.Ents
			old.Hint = hint
		} else { // the stored trajectory stays; the new variant is the witness
			old.Witnesses = pcAddWitness(old.Witnesses, pcWitness{Task: p.Task, Ents: p.Ents, Cmds: p.Cmds}, old.Task)
		}
		if old.Shape == "" { // plano antigo sem metadado: deriva do texto guardado
			old.Shape, old.Ents = pcShapeEnts(old.Task, nuRuntimeWorld(root, nil))
		}
		if p.Hint != "" {
			old.Hint = p.Hint
		}
		old.Time = p.Time
		plans[i] = old
		return pcSave(root, plans)
	}
	return pcSave(root, append(plans, p))
}

func pcJaccard(a, b []string) float64 {
	sa, sb := smSet(a...), smSet(b...)
	if len(sa)+len(sb) == 0 {
		return 0
	}
	inter := 0
	for w := range sa {
		if sb[w] {
			inter++
		}
	}
	return float64(inter) / float64(len(sa)+len(sb)-inter)
}

// pcEntsOf converts the spotted entities of a request for storage.
func pcEntsOf(hits []nuHit) []pcEnt {
	var out []pcEnt
	for _, h := range hits {
		out = append(out, pcEnt{Kind: h.Kind, Name: h.Name, Raw: h.Raw})
	}
	return out
}

// pcShapeEnts derives a request's shape: the canon text with every spotted
// entity masked as "@<kind>" — "comprime docs" reads "comprime @dir". Two
// requests that differ only in entity values share the shape and merge
// into one plan; their entity values are the distill witnesses.
func pcShapeEnts(text string, w *nuWorld) (string, []pcEnt) {
	m := nuGet()
	if m == nil || m.loadErr != nil || w == nil {
		return "", nil
	}
	toks := m.normalize(nuTokenize(text))
	hits := m.spot(toks, w)
	return nuShapeCanon(toks, hits), pcEntsOf(hits)
}

// pcBuild turns the turn's tool log into a template: read-only exploration
// is dropped, consecutive repeats are folded, failed commands that never
// succeeded later become dead ends.
func (a *Agent) pcBuild(lt *acTurnLoop, steps int) pcPlan {
	rc := lt.rcState()
	req := redactSecrets(nlCredScrub(a.pcTaskReq(strings.TrimSpace(acStripReminders(lt.input)))))
	p := pcPlan{
		Time:   time.Now().Unix(),
		Task:   oneLine(req, 300),
		Keys:   pcUniq(pcTerms(req)),
		Files:  a.tools.rcTurnFiles(),
		NSteps: steps,
		Wins:   1,
		V:      pcSchema,
	}
	p.Shape, p.Ents = pcShapeEnts(req, nuRuntimeWorld(a.tools.Root, nil))
	p.Kind, p.Repo, p.Session = lrTaskClass(req), lrRepoSig(a.tools.Root), a.SessionID
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", p.Task, time.Now().UnixNano())))
	p.ID = hex.EncodeToString(sum[:6])
	ok := map[string]bool{}
	for _, e := range rc.log {
		if !e.isErr {
			ok[e.tool+" "+e.target] = true
		}
	}
	var lastStep string
	reps := 0
	flush := func() {
		if lastStep == "" {
			return
		}
		s := lastStep
		if reps > 1 {
			s += fmt.Sprintf(" ×%d", reps)
		}
		p.Steps = append(p.Steps, s)
	}
	for _, e := range rc.log {
		if e.tool == "bash" && e.isErr && !ok["bash "+e.target] && len(p.Dead) < pcMaxDeadEnds {
			d := "`" + oneLine(pcSafeCmd(e.target), 80) + "`"
			if e.errTxt != "" {
				d += " (" + oneLine(e.errTxt, 60) + ")"
			}
			p.Dead = append(p.Dead, redactSecrets(d))
		}
		if e.tool == "bash" && len(p.Cmds) < pcMaxCmds {
			p.Cmds = append(p.Cmds, redactSecrets(nlCredScrub(pcSafeCmd(e.target))))
		}
		if !(e.verify || e.mut && !e.isErr) {
			continue
		}
		tgt := e.target
		if e.tool == "bash" {
			tgt = pcSafeCmd(tgt)
		}
		s := e.tool + " " + oneLine(tgt, 70)
		if e.verify {
			s += fmt.Sprintf(" (exit %d)", e.exit)
		}
		if s == lastStep {
			reps++
			continue
		}
		flush()
		lastStep, reps = s, 1
	}
	flush()
	if len(p.Steps) > pcMaxApproach { // keep the start and the end of the approach
		p.Steps = append(append(append([]string(nil), p.Steps[:3]...), "…"), p.Steps[len(p.Steps)-(pcMaxApproach-4):]...)
	}
	for i := range p.Steps {
		p.Steps[i] = redactSecrets(nlCredScrub(p.Steps[i]))
	}
	if rc.fired != "" {
		p.Dead = append(p.Dead, "the first attempt stalled ("+rc.fired+") and was handed off")
	}
	switch {
	case rc.untilPassed && rc.untilCmd != "":
		p.Verify = rc.untilCmd
	case a.loop.opts.Until != "":
		p.Verify = a.loop.opts.Until
	default:
		p.Verify = rc.verifyCmd
	}
	p.Verify = redactSecrets(nlCredScrub(pcSafeCmd(p.Verify)))
	return p
}

// pcTaskReq: a short follow-up ("bora", "marcha", "continua") is not the
// task — the last substantive request of the session is.
func (a *Agent) pcTaskReq(req string) string {
	if len(pcUniq(pcTerms(req))) >= pcMinTerms {
		return req
	}
	for i := len(a.Msgs) - 1; i >= 0; i-- {
		m := a.Msgs[i]
		if m.Role != "user" {
			continue
		}
		prev := strings.TrimSpace(acStripReminders(m.Content))
		if strings.Contains(prev, "## Task (the user's request)") {
			break // a handoff replaced the history: what came before is gone
		}
		if prev != req && len(pcUniq(pcTerms(prev))) >= pcMinTerms && !strings.HasPrefix(prev, "<") {
			return prev
		}
	}
	return req
}

// pcLitMark replaces the literal argument of a text command in a stored
// plan: `sed 's/^SENHA.*/…/'` carried a password fragment to disk and to a
// free provider.
const pcLitMark = "«literal omitido»"

// pcLitVerbs are commands whose quoted/literal arguments are data that may
// hold a secret; only the verb and the path words are kept.
var pcLitVerbs = smSet("sed", "echo", "printf", "grep", "egrep", "fgrep", "rg", "awk", "perl", "tee")

// pcSafeCmd keeps verb, redirections and file operands of a text command
// with literal arguments; any other command passes through unchanged.
func pcSafeCmd(cmd string) string {
	f := strings.Fields(cmd)
	i := 0
	for i < len(f) && (f[i] == "sudo" || strings.Contains(f[i], "=") && !strings.HasPrefix(f[i], "-")) {
		i++
	}
	if i >= len(f) || !pcLitVerbs[filepath.Base(f[i])] {
		return cmd
	}
	verb := filepath.Base(f[i])
	out := append([]string(nil), f[:i+1]...)
	out = append(out, pcLitMark)
	inQ := byte(0)
	pattern := verb == "echo" || verb == "printf" || verb == "tee" // no pattern operand
	redirect := false
	for _, w := range f[i+1:] {
		switch {
		case inQ != 0:
			if strings.HasSuffix(w, string(inQ)) {
				inQ = 0
			}
		case w[0] == '\'' || w[0] == '"':
			if len(w) == 1 || !strings.HasSuffix(w, string(w[0])) {
				inQ = w[0]
			}
			if !redirect {
				pattern = true
			}
			redirect = false
		case w == "|" || w == "&&" || w == ";" || w == "||":
			return strings.Join(out, " ") + " " + w + " …" // the rest is another command
		case strings.HasPrefix(w, ">") || strings.HasPrefix(w, "<") || w == "2>&1":
			out = append(out, w)
			redirect = len(strings.TrimLeft(w, "<>&12")) == 0
		case redirect:
			out = append(out, w)
			redirect = false
		case strings.HasPrefix(w, "-"):
		case verb == "echo" || verb == "printf":
		case !pattern:
			pattern = true // grep SENHA f: the first operand is the pattern
		case !strings.ContainsAny(w, "^$*[]\\"):
			out = append(out, w)
		}
	}
	return strings.Join(out, " ")
}

// ---------- hindsight (A3) ----------

const hdSystemPrompt = `You write hindsight notes for a coding agent. You get the record of a run that finished and passed its verification. Write a short hint for a future agent that gets the same or a similar task in this repository, so it gets there in fewer steps.
Name the exact file paths that mattered, the approach that worked, and the dead ends to skip. Give concrete commands where useful. Do not write code and do not restate the whole solution.
Reply with the hint only, at most 1200 characters. If the run was already direct and there is nothing useful to add, reply with exactly: NO_HINT`

// hdWrite asks the strongest model of the chain for the hindsight hint ("" = none).
func (a *Agent) hdWrite(ctx context.Context, lt *acTurnLoop, p pcPlan) string {
	ref := a.hdStrongRef()
	if ref == "" {
		dbg("hindsight", map[string]any{"skip": "no strong model in the chain"})
		return ""
	}
	rc := lt.rcState()
	var u strings.Builder
	u.WriteString("Task:\n" + acClip(strings.TrimSpace(acStripReminders(lt.input)), 2000) + "\n\n")
	if len(p.Files) > 0 {
		u.WriteString("Files changed: " + strings.Join(acClipList(p.Files, 30), ", ") + "\n")
	}
	if p.Verify != "" {
		u.WriteString("Verified with: " + p.Verify + "\n")
	}
	fmt.Fprintf(&u, "Model steps used: %d\n", p.NSteps)
	if rc.fired != "" {
		u.WriteString("The run stalled once (" + rc.fired + ") and was restarted from a handoff note.\n")
	}
	u.WriteString("\nTool calls, in order:\n")
	var trail strings.Builder
	for i, e := range rc.log {
		fmt.Fprintf(&trail, "%d. %s\n", i+1, e.summary())
	}
	u.WriteString(truncateHeadTail(trail.String(), hdMaxTrailChars))
	a.ui.Thinking("escrevendo dica (hindsight)")
	defer a.ui.StopThinking()
	text, err := hdChat(ctx, a, ref, hdSystemPrompt, redactSecrets(u.String()))
	if err != nil {
		dbg("hindsight", map[string]any{"ref": ref, "error": err.Error()})
		return ""
	}
	text = strings.TrimSpace(text)
	if text == "" || strings.HasPrefix(strings.ToUpper(strings.Trim(text, "*`\"' ")), "NO_HINT") {
		return ""
	}
	if r := []rune(text); len(r) > hdMaxHint {
		text = string(r[:hdMaxHint-1]) + "…"
	}
	return redactSecrets(text)
}

// hdChat makes the tool-less hindsight request (a var so tests can stub it).
var hdChat = func(ctx context.Context, a *Agent, ref, system, user string) (string, error) {
	if ok, _ := a.limiter().Allow(ref); !ok {
		return "", errors.New("rate limited")
	}
	pname, mid := SplitModel(ref)
	prov, err := a.cfg.NewProvider(pname)
	if err != nil {
		return "", err
	}
	cctx, cancel := context.WithTimeout(ctx, hdTimeout)
	defer cancel()
	resp, err := prov.Chat(cctx, ChatRequest{Model: mid, System: system, Messages: []Message{{Role: "user", Content: user}}, MaxTokens: 1200}, nil)
	if err != nil {
		a.limiter().Report(ref, err)
		return "", err
	}
	if resp == nil {
		return "", errors.New("empty response")
	}
	a.priceUsage(ref, &resp.Usage)
	a.addUsage(resp.Usage)
	return resp.Text, nil
}

// hdStrongRef picks the hindsight writer: the configured model, else the
// strongest candidate of the session's chain (omni quality prior ≥ hdMinQ;
// a paid model without a prior counts as strong unless paid use is "never").
func (a *Agent) hdStrongRef() string {
	if s := hdSetting(a.cfg); strings.Contains(s, "/") {
		return s
	}
	refs := []string{a.TurnModel(), a.Model}
	if a.cfg.Omni.Enabled || isOmniRef(a.Model) {
		refs = append([]string{OmniProvider + "/smart"}, refs...)
	}
	best, bestQ := "", 0
	for _, r := range a.cfg.CandidatesFor(Need{Role: "main", Tokens: 6000}, refs...) {
		q := a.cfg.ModelSettings(r).Q
		if q == 0 && !a.cfg.pvIsFreeish(r) && a.cfg.Omni.Paid != "never" {
			q = 90
		}
		if q > bestQ {
			best, bestQ = r, q
		}
	}
	if bestQ < hdMinQ {
		return ""
	}
	return best
}
