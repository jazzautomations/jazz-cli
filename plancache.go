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
	"strings"
	"time"
	"unicode"
)

const (
	pcMinSteps      = 12   // shorter runs are not worth a template
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
)

// pcPlan is one stored plan template.
type pcPlan struct {
	ID     string   `json:"id"`
	Time   int64    `json:"time"`
	Task   string   `json:"task"` // the request, clipped
	Keys   []string `json:"keys"` // normalized keywords of the request
	Files  []string `json:"files,omitempty"`
	Steps  []string `json:"steps,omitempty"` // approach: the actions that changed or checked something
	Dead   []string `json:"dead,omitempty"`  // dead ends to skip
	Verify string   `json:"verify,omitempty"`
	Hint   string   `json:"hint,omitempty"` // hindsight note (A3)
	NSteps int      `json:"n_steps"`        // model steps of the run that produced it
	Wins   int      `json:"wins"`
	Fails  int      `json:"fails"`
	// lesson reuse (lessons_reuse.go): task class and repo signature
	Kind    string `json:"kind,omitempty"` // bgClassify: size + kind, e.g. "M/bugfix"
	Repo    string `json:"repo,omitempty"` // lrRepoSig of the project when stored
	Session string `json:"session,omitempty"`
}

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
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var p pcPlan
		if json.Unmarshal(sc.Bytes(), &p) == nil && p.ID != "" && h2PlanPoison(p) == "" { // poisoned plans are never reused (hardening2.go)
			out = append(out, p)
		}
	}
	return out
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
)

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
// share enough distinct keywords with it (≥ 2, or all of a 1–2 word query,
// and ≥ 40% of the query).
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
	need := 2
	if len(q) < 2 {
		need = len(q)
	}
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
		if matched < need || float64(matched) < 0.4*float64(len(q)) {
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
	verified := stop == StopDone && (rc.untilPassed || rc.gateOK || (a.loop.opts.Until == "" && rc.verifyOK))
	root := a.tools.Root
	if len(rc.plans) > 0 && (verified || pcFailed(stop)) {
		if err := pcScore(root, rc.plans, verified); err != nil {
			dbg("plan_cache", map[string]any{"score_error": err.Error()})
		}
	}
	if !verified || steps < pcMinSteps || ctx.Err() != nil || a.mode() == "plan" {
		return
	}
	p := a.pcBuild(lt, steps)
	if hdSetting(a.cfg) != "" {
		p.Hint = a.hdWrite(ctx, lt, p)
	}
	if err := pcStore(root, p, rc.plans); err != nil {
		dbg("plan_cache", map[string]any{"store_error": err.Error()})
		return
	}
	dbg("plan_cache", map[string]any{"stored": p.ID, "steps": steps, "hint": p.Hint != ""})
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

// pcStore adds p, or refreshes a plan for the same task (keyword Jaccard ≥ 0.7).
// A plan reused in this turn already got its win from pcScore.
func pcStore(root string, p pcPlan, reused []string) error {
	if why := h2PlanPoison(p); why != "" {
		auditRec("plan.rejected", map[string]any{"reason": why, "task": oneLine(p.Task, 120)})
		return nil // never written (hardening2.go)
	}
	plans := pcLoad(root)
	was := smSet(reused...)
	for i, old := range plans {
		if pcJaccard(old.Keys, p.Keys) < 0.7 {
			continue
		}
		if !was[old.ID] {
			old.Wins++
		}
		if p.NSteps <= old.NSteps || old.NSteps == 0 { // keep the shorter trajectory
			hint := old.Hint
			old.Task, old.Keys, old.Files, old.Steps, old.Dead, old.Verify, old.NSteps = p.Task, p.Keys, p.Files, p.Steps, p.Dead, p.Verify, p.NSteps
			old.Hint = hint
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

// pcBuild turns the turn's tool log into a template: read-only exploration
// is dropped, consecutive repeats are folded, failed commands that never
// succeeded later become dead ends.
func (a *Agent) pcBuild(lt *acTurnLoop, steps int) pcPlan {
	rc := lt.rcState()
	req := redactSecrets(strings.TrimSpace(acStripReminders(lt.input)))
	p := pcPlan{
		Time:   time.Now().Unix(),
		Task:   oneLine(req, 300),
		Keys:   pcUniq(pcTerms(req)),
		Files:  a.tools.rcTurnFiles(),
		NSteps: steps,
		Wins:   1,
	}
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
			d := "`" + oneLine(e.target, 80) + "`"
			if e.errTxt != "" {
				d += " (" + oneLine(e.errTxt, 60) + ")"
			}
			p.Dead = append(p.Dead, redactSecrets(d))
		}
		if !(e.verify || e.mut && !e.isErr) {
			continue
		}
		s := e.tool + " " + oneLine(e.target, 70)
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
		p.Steps[i] = redactSecrets(p.Steps[i])
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
	return p
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
