package main

// judge.go: the omni judge. Per-(model, task type) scoreboard + a UCB1 bandit
// that picks the main editor model of a turn (docs/research/omni-judge.md).
//
//   - every main turn is classified (route_task.go) and its outcome is
//     recorded for the model that gave the final answer: success (final
//     answer, quality gate and honest-done guards passed, tests ran after the
//     last code edit), failure, timeout or cutoff, plus requests, request
//     latency, 429s, first-byte stalls, wall time and cost. Recording is
//     always on (it only writes statistics);
//   - with --omni-judge on (config judge.mode, JAZZ_OMNI_JUDGE) the turn's
//     model is chosen by UCB1 over the candidate chain the router already
//     filtered by Need (tools, context size, free/paid policy, dead refs),
//     with ε-greedy exploration; the fallback chain after it is ordered by
//     the same score, so a first-byte stall moves to the next-best model;
//   - cold start: a static prior table filled from measured eval results
//     (ojPriors, provenance in the comment of each row);
//   - every decision is a debug record of kind "route".
//
// Data: the "tasks" section of dataDir()/model-scores.json (scores.go flushes it
// with the per-model section, under the same flock, merged additively).

import (
	"context"
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------- settings and flags ----------

// JudgeConf is config "judge": {"mode": "on", "epsilon": 0.1, "verifier": "on",
// "hedge": "on", "panel": "on"}.
type JudgeConf struct {
	Mode     string   `json:"mode,omitempty"`     // off (default) | on
	Epsilon  *float64 `json:"epsilon,omitempty"`  // exploration rate (default 0.1)
	Verifier string   `json:"verifier,omitempty"` // off (default) | on
	Hedge    string   `json:"hedge,omitempty"`    // auto (default: on with the judge) | on | off
	Panel    string   `json:"panel,omitempty"`    // off (default) | on
}

// RolesConf is config "roles": {"planner", "editor", "verifier"}: "auto" (or
// empty) lets the judge pick; a "provider/model" ref pins the role. (The key is
// not "models" because that one already holds per-model settings.)
type RolesConf struct {
	Planner  string `json:"planner,omitempty"`
	Editor   string `json:"editor,omitempty"`
	Verifier string `json:"verifier,omitempty"`
}

// FusionConf is config "fusion": {"mode": "on", "lead": "auto", "sidekick": "auto"}.
type FusionConf struct {
	Mode     string `json:"mode,omitempty"`
	Lead     string `json:"lead,omitempty"`
	Sidekick string `json:"sidekick,omitempty"`
}

// ojFlagVals are the per-run flags (never saved).
type ojFlagVals struct{ judge, verifier, hedge, panel, fusion string }

var ojRun ojFlagVals

var ojValuedFlags = map[string]bool{"--omni-judge": true, "-omni-judge": true, "--verifier": true, "-verifier": true,
	"--hedge": true, "-hedge": true, "--panel": true, "-panel": true, "--fusion": true, "-fusion": true}

func init() {
	for k := range ojValuedFlags {
		opValuedFlags[k] = true // reorderFlags (main.go) learns they take a value
	}
}

// ojFlags registers --omni-judge, --verifier, --hedge, --panel and --fusion.
type ojFlags struct{ v ojFlagVals }

func ojRegisterFlags(fs *flag.FlagSet) *ojFlags {
	f := &ojFlags{}
	fs.StringVar(&f.v.judge, "omni-judge", "", "")
	fs.StringVar(&f.v.verifier, "verifier", "", "")
	fs.StringVar(&f.v.hedge, "hedge", "", "")
	fs.StringVar(&f.v.panel, "panel", "", "")
	fs.StringVar(&f.v.fusion, "fusion", "", "")
	return f
}

func (f *ojFlags) apply() error {
	for name, v := range map[string]string{"--omni-judge": f.v.judge, "--verifier": f.v.verifier, "--hedge": f.v.hedge, "--panel": f.v.panel, "--fusion": f.v.fusion} {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "", "on", "off":
		default:
			return fmt.Errorf("%s deve ser on ou off", name)
		}
	}
	ojRun = f.v
	return nil
}

func ojOn(flagV, env, conf string) bool {
	return bgSetting(flagV, env, conf, "off", "on", "off") == "on"
}

func (c *Config) ojJudgeOn() bool    { return ojOn(ojRun.judge, "JAZZ_OMNI_JUDGE", c.Judge.Mode) }
func (c *Config) ojVerifierOn() bool { return ojOn(ojRun.verifier, "JAZZ_VERIFIER", c.Judge.Verifier) }
func (c *Config) ojPanelOn() bool    { return ojOn(ojRun.panel, "JAZZ_PANEL", c.Judge.Panel) }
func (c *Config) ojFusionOn() bool   { return ojOn(ojRun.fusion, "JAZZ_FUSION", c.Fusion.Mode) }

// ojHedgeOn: small idempotent requests go to two candidates. Default: with the judge.
func (c *Config) ojHedgeOn() bool {
	def := "off"
	if c.ojJudgeOn() {
		def = "on"
	}
	v := bgSetting(ojRun.hedge, "JAZZ_HEDGE", c.Judge.Hedge, def, "on", "off", "auto")
	return v == "on" || (v == "auto" && def == "on")
}

func (c *Config) ojEpsilon() float64 {
	if e := os.Getenv("JAZZ_JUDGE_EPSILON"); e != "" {
		var f float64
		if _, err := fmt.Sscan(e, &f); err == nil && f >= 0 && f <= 1 {
			return f
		}
	}
	if c.Judge.Epsilon != nil && *c.Judge.Epsilon >= 0 && *c.Judge.Epsilon <= 1 {
		return *c.Judge.Epsilon
	}
	return 0.1
}

// ojPinned: a role set to a concrete ref ("" for auto).
func ojPinned(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || strings.EqualFold(v, "auto") {
		return ""
	}
	return v
}

// ---------- scoreboard ----------

// ojStat is one (model, task type) record; every field is a sum, so the
// on-disk merge of concurrent processes is plain addition.
type ojStat struct {
	OK      int     `json:"ok,omitempty"`      // successful turns
	Fail    int     `json:"fail,omitempty"`    // quality/honest-done/until failed, step limit, loop, unverified, error
	Timeout int     `json:"timeout,omitempty"` // --timeout hit
	Cutoff  int     `json:"cutoff,omitempty"`  // reply cut (truncated)
	Reqs    int     `json:"reqs,omitempty"`    // requests in the recorded turns
	LatN    int     `json:"lat_n,omitempty"`   // successful requests with a latency
	MS      float64 `json:"ms,omitempty"`      // sum of their latency, ms
	R429    int     `json:"r429,omitempty"`    // 429 answers
	Stalls  int     `json:"stalls,omitempty"`  // first-byte stalls
	Wall    float64 `json:"wall,omitempty"`    // sum of turn wall time, s
	Cost    float64 `json:"cost,omitempty"`    // USD
	At      int64   `json:"at,omitempty"`
}

func (s *ojStat) turns() int { return s.OK + s.Fail + s.Timeout + s.Cutoff }

func (s *ojStat) add(d *ojStat) {
	s.OK += d.OK
	s.Fail += d.Fail
	s.Timeout += d.Timeout
	s.Cutoff += d.Cutoff
	s.Reqs += d.Reqs
	s.LatN += d.LatN
	s.MS += d.MS
	s.R429 += d.R429
	s.Stalls += d.Stalls
	s.Wall += d.Wall
	s.Cost += d.Cost
	s.At = max(s.At, d.At)
}

func ojKey(ref, typ string) string { return ref + "|" + typ }

type ojStore struct {
	mu    sync.Mutex
	path  string
	base  map[string]*ojStat
	delta map[string]*ojStat
}

var ojDB ojStore

func ojLoadLocked() *ojStore {
	db := &ojDB
	if p := bgScoresPath(); db.path != p || db.base == nil {
		db.path, db.base, db.delta = p, ojReadTasks(p), map[string]*ojStat{}
	}
	return db
}

// ojRecord adds d to the (ref, typ) record (unsaved until the next flush).
func ojRecord(ref, typ string, d ojStat) {
	if ref == "" || typ == "" {
		return
	}
	ojDB.mu.Lock()
	defer ojDB.mu.Unlock()
	db := ojLoadLocked()
	k := ojKey(ref, typ)
	cur := db.delta[k]
	if cur == nil {
		cur = &ojStat{}
		db.delta[k] = cur
	}
	d.At = time.Now().Unix()
	cur.add(&d)
}

// ojStatOf is the merged (file + unsaved) record.
func ojStatOf(ref, typ string) ojStat {
	ojDB.mu.Lock()
	defer ojDB.mu.Unlock()
	db := ojLoadLocked()
	var s ojStat
	k := ojKey(ref, typ)
	if b := db.base[k]; b != nil {
		s = *b
	}
	if d := db.delta[k]; d != nil {
		s.add(d)
	}
	return s
}

// ojAllStats: every merged record (for jazz models --scores).
func ojAllStats() map[string]ojStat {
	ojDB.mu.Lock()
	defer ojDB.mu.Unlock()
	db := ojLoadLocked()
	out := map[string]ojStat{}
	for k, b := range db.base {
		out[k] = *b
	}
	for k, d := range db.delta {
		s := out[k]
		s.add(d)
		out[k] = s
	}
	return out
}

// ojMergeInto adds this process's deltas to cur (called by bgScoresFlush with
// the file lock held); ojCommit makes cur the new base once it is written.
func ojMergeInto(cur map[string]*ojStat) bool {
	ojDB.mu.Lock()
	defer ojDB.mu.Unlock()
	db := ojLoadLocked()
	for k, d := range db.delta {
		s := cur[k]
		if s == nil {
			s = &ojStat{}
			cur[k] = s
		}
		s.add(d)
	}
	return len(db.delta) > 0
}

func ojCommit(cur map[string]*ojStat) {
	ojDB.mu.Lock()
	defer ojDB.mu.Unlock()
	ojDB.base, ojDB.delta = cur, map[string]*ojStat{}
}

func ojHasDelta() bool {
	ojDB.mu.Lock()
	defer ojDB.mu.Unlock()
	return len(ojDB.delta) > 0
}

// ---------- per-turn request accounting (fed by scores.go and ratelimit.go) ----------

type ojAcc struct {
	reqs, latN, r429, stalls int
	ms                       float64
}

var (
	ojAccMu sync.Mutex
	ojAccOf = map[string]*ojAcc{} // ref -> this turn's counters (process-wide; a turn is the unit)
)

func ojAccLocked(ref string) *ojAcc {
	x := ojAccOf[ref]
	if x == nil {
		x = &ojAcc{}
		ojAccOf[ref] = x
	}
	return x
}

// ojObserveReq counts one finished provider request (ratelimit.go rlEnd).
func ojObserveReq(ref string, err error) {
	ojAccMu.Lock()
	defer ojAccMu.Unlock()
	x := ojAccLocked(ref)
	x.reqs++
	if pvClassify(err) == "rate" {
		x.r429++
	}
}

// ojObserveOK / ojObserveStall: latency of a successful main-chain request and
// a first-byte stall (scores.go).
func ojObserveOK(ref string, d time.Duration) {
	ojAccMu.Lock()
	defer ojAccMu.Unlock()
	x := ojAccLocked(ref)
	x.latN++
	x.ms += float64(d.Milliseconds())
}

func ojObserveStall(ref string) {
	ojAccMu.Lock()
	defer ojAccMu.Unlock()
	ojAccLocked(ref).stalls++
}

func ojTakeAcc() map[string]*ojAcc {
	ojAccMu.Lock()
	defer ojAccMu.Unlock()
	out := ojAccOf
	ojAccOf = map[string]*ojAcc{}
	return out
}

// ---------- priors (cold start) ----------

// ojPrior seeds a (model glob, task type) pair with measured eval results.
// Each observation counts half (other version/hardware), at most 6 pseudo-turns;
// a model's row for the type and its "*" row are pooled.
type ojPrior struct {
	glob string  // ref glob (pvGlobMatch)
	typ  string  // task type, "*" = any
	pass int     // passes measured
	n    int     // tasks measured
	wall float64 // average wall time per task, s (0 = unknown)
}

// ojPriors. Provenance (all jazz -p, free models, Oct 2026):
//
//	laguna  = kilo/poolside/laguna-s-2.1:free. evals/v2 on the Mac, jazz 0.4.1-0.4.3
//	          (~/jazz-eval-results/v2-20261020-*: 21 PASS of 21 runs; per task
//	          01 3/3, 02 1/1, 03 4/4, 04 1/1, 05 1/1, 06 1/1, 07 1/1, 08 2/2,
//	          09 3/3, 10 3/3; mean wall 330 s); terminal-tasks on the Dell 13/14
//	          (only failure cancel-async-tasks = feature); aider-polyglot 13/17 done
//	          (= design_doc) (docs/research/benchmarks.md); v1 10/10.
//	minimax = minimax-m3.1-flash:free. evals/v2 on the Dell 7/10, the 2 known
//	          failures edited the tests (now refused by the honest-done guards)
//	          (benchmarks.md); Mac 3/3 on 01 (185 s), 03 (62 s), 08 (444 s).
//
// Everything else has no measurement: a weak prior from the preset Q (ojQPrior).
var ojPriors = []ojPrior{
	{"*/poolside/laguna-s-2.1*", rtBugfix, 4 + 3, 4 + 3, 300},   // v2 01×3, 05; terminal fix-* ×3
	{"*/poolside/laguna-s-2.1*", rtFeature, 2 + 4, 2 + 5, 330},  // v2 02, 04; terminal 4/5
	{"*/poolside/laguna-s-2.1*", rtBigFile, 4, 4, 280},          // v2 03×4
	{"*/poolside/laguna-s-2.1*", rtShell, 2 + 6, 2 + 6, 170},    // v2 06, 07; terminal git/process ×6
	{"*/poolside/laguna-s-2.1*", rtRefactor, 2 + 1, 2 + 1, 580}, // v2 08×2; v1 03
	{"*/poolside/laguna-s-2.1*", rtDesignDoc, 3 + 13, 3 + 17, 520},
	{"*/poolside/laguna-s-2.1*", rtDebugLog, 3, 3, 205},
	{"*/poolside/laguna-s-2.1*", "*", 10, 10, 300}, // v1 10/10
	{"*/minimax/minimax-m3.1-flash*", "*", 7, 10, 230},
	{"*/minimax/minimax-m3.1-flash*", rtBugfix, 1, 1, 185},
	{"*/minimax/minimax-m3.1-flash*", rtBigFile, 1, 1, 62},
	{"*/minimax/minimax-m3.1-flash*", rtRefactor, 1, 1, 444},
}

// ojPriorFor returns the pseudo-counts (passes, turns) and the wall prior.
func (c *Config) ojPriorFor(ref, typ string) (pass, n, wall float64, src string) {
	var exact, any *ojPrior
	for i := range ojPriors {
		p := &ojPriors[i]
		if !pvGlobMatch(p.glob, ref) {
			continue
		}
		if p.typ == typ && exact == nil {
			exact = p
		} else if p.typ == "*" && any == nil {
			any = p
		}
	}
	// the type's own row and the model's overall row are pooled
	var pp, pn int
	switch {
	case exact != nil && any != nil:
		pp, pn, wall, src = exact.pass+any.pass, exact.n+any.n, exact.wall, "eval"
	case exact != nil:
		pp, pn, wall, src = exact.pass, exact.n, exact.wall, "eval"
	case any != nil:
		pp, pn, wall, src = any.pass, any.n, any.wall, "eval(any)"
	}
	if pn > 0 {
		n = math.Min(6, 0.5*float64(pn))
		pass = n * float64(pp+1) / float64(pn+2)
		return pass, n, wall, src
	}
	q, _ := c.ojQPrior(ref)
	return q, 1, 0, "q"
}

// ojQPrior: one pseudo-turn worth Q/100·0.8 (presets are uncalibrated guesses).
func (c *Config) ojQPrior(ref string) (float64, int) {
	q := 50
	if c != nil {
		if ms := c.ModelSettings(ref); ms.Q > 0 {
			q = ms.Q
		}
	}
	return float64(q) / 100 * 0.8, q
}

// ---------- the bandit ----------

// ojCand is one candidate as the bandit sees it.
type ojCand struct {
	Ref    string  `json:"ref"`
	Mean   float64 `json:"mean"`  // smoothed success rate (prior + measured)
	N      float64 `json:"n"`     // turns (prior pseudo-turns + measured)
	Obs    int     `json:"obs"`   // measured turns only
	Wall   float64 `json:"wall"`  // average wall time per turn, s (0 = unknown)
	R429   float64 `json:"r429"`  // 429s per request
	Score  float64 `json:"score"` // UCB value used for the order
	Ready  bool    `json:"ready"`
	Free   bool    `json:"free"`
	Prior  string  `json:"prior"`
	Pinned bool    `json:"pinned,omitempty"`
}

// Bandit constants: UCB1 exploration weight, latency and 429 penalties.
const (
	ojUCBc     = 0.2
	ojWallFull = 900.0 // seconds at which the latency penalty is complete
	ojWallPen  = 0.10
	ojRatePen  = 0.15
	ojBusyPen  = 0.30 // limiter says not Ready right now
	// ojWallUnknown is the assumed turn time of a model never measured.
	ojWallUnknown = 450.0
)

// ojCands scores refs for task type typ.
func (c *Config) ojCands(refs []string, typ string) []ojCand {
	out := make([]ojCand, 0, len(refs))
	for _, r := range refs {
		s := ojStatOf(r, typ)
		pp, pn, pw, src := c.ojPriorFor(r, typ)
		cd := ojCand{Ref: r, Obs: s.turns(), Prior: src, Free: c.pvIsFreeish(r), Ready: true}
		cd.N = pn + float64(s.turns())
		cd.Mean = (pp + float64(s.OK)) / math.Max(cd.N, 1e-9)
		switch {
		case s.turns() > 0 && pw > 0:
			cd.Wall = (pw*pn + s.Wall) / (pn + float64(s.turns()))
		case s.turns() > 0:
			cd.Wall = s.Wall / float64(s.turns())
		case pw > 0:
			cd.Wall = pw
		default:
			cd.Wall = ojWallUnknown // no measurement: assume slow-ish
		}
		if s.Reqs > 0 {
			cd.R429 = float64(s.R429) / float64(s.Reqs)
		}
		if c != nil && c.Providers != nil {
			if st := c.pvState(r, 0); !st.Ready {
				cd.Ready = false
			}
		}
		out = append(out, cd)
	}
	return out
}

// ojRank orders cands by UCB1 (with penalties); with probability eps the
// first place goes to a random other candidate (exploration). rnd in [0,1).
func ojRank(cands []ojCand, eps float64, rnd func() float64) ([]ojCand, string) {
	if len(cands) == 0 {
		return cands, "empty"
	}
	total := 0.0
	for _, x := range cands {
		total += x.N
	}
	lnN := math.Log(math.Max(total, 1) + 1)
	for i := range cands {
		x := &cands[i]
		x.Score = x.Mean + ojUCBc*math.Sqrt(lnN/math.Max(x.N, 0.5))
		x.Score -= ojWallPen * math.Min(1, x.Wall/ojWallFull)
		x.Score -= ojRatePen * math.Min(1, x.R429*4)
		if !x.Ready {
			x.Score -= ojBusyPen
		}
		if !x.Free {
			x.Score -= 1 // paid only after every free one (omni.paid stays the gate)
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].Score > cands[j].Score })
	reason := "ucb"
	if len(cands) > 1 && eps > 0 && rnd() < eps {
		var pool []int
		for i := 1; i < len(cands); i++ {
			if cands[i].Ready && cands[i].Free == cands[0].Free {
				pool = append(pool, i)
			}
		}
		if len(pool) > 0 {
			k := pool[int(rnd()*float64(len(pool)))%len(pool)]
			x := cands[k]
			copy(cands[1:k+1], cands[0:k])
			cands[0] = x
			reason = "explore"
		}
	}
	return cands, reason
}

// ---------- agent hooks ----------

// ojState is the judge's per-agent state.
type ojState struct {
	task      rtTask
	typed     bool
	repo      *rtRepo
	repoRoot  string
	t0        time.Time
	editor    string          // model picked for this turn ("" = none)
	lastOrder string          // last logged fallback order (to log only changes)
	verified  bool            // the verifier ran this turn
	subModel  string          // model of the next task subagent (verifier, sidekick)
	failTests int             // failed test runs this turn (panel root-cause trigger)
	rcPanel   bool            // root-cause panel already ran this turn
	side      *Agent          // fusion sidekick (persistent context, judge_fusion.go)
	ctx       context.Context // the turn's context (panel after a failed test run)
	input     string          // the user's text of the turn
	sideFor   *Agent          // set on the sidekick: its lead
}

func (a *Agent) ojs() *ojState {
	if a.oj == nil {
		a.oj = &ojState{}
	}
	return a.oj
}

// ojTimedOut reports that the headless --timeout fired (set by main).
var ojTimedOut func() bool

// ojRandom is the bandit's random source (tests replace it).
var ojRandom = func() float64 { return rand.Float64() }

// ojStartTurn is the one call from Agent.Run: classify, pick the turn's model
// (judge on), add the fusion/panel notes. Returns the model and the input.
func (a *Agent) ojStartTurn(ctx context.Context, request, model, input string) (string, string) {
	if a.sub {
		return model, input
	}
	if m := a.ojBeginTurn(request); m != "" {
		model = m
		a.turnModel = m
	}
	return model, a.ojTurnInput(ctx, input)
}

// ojBeginTurn classifies the request and, with the judge on, returns the model
// for the turn ("" = keep the current one). Main agent only.
func (a *Agent) ojBeginTurn(input string) string {
	if a.sub {
		return ""
	}
	st := a.ojs()
	st.t0, st.verified, st.failTests, st.rcPanel, st.editor, st.lastOrder = time.Now(), false, 0, false, "", ""
	ojTakeAcc() // a new turn: drop the counters of whatever ran between turns
	root := ""
	if a.tools != nil {
		root = a.tools.Root
	}
	if st.repo == nil || st.repoRoot != root {
		r := rtWalk(root)
		st.repo, st.repoRoot = &r, root
	}
	st.repo.rtMention(root, input)
	st.input = strings.TrimSpace(acStripReminders(input))
	st.task = rtClassify(input, *st.repo)
	st.typed = true
	if a.cfg.ojJudgeOn() && st.task.Ambiguous {
		if t := a.ojClassifyLLM(input); t != "" {
			st.task.Type, st.task.Via = t, "llm"
		}
	}
	dbg("route", map[string]any{"role": "classify", "task": st.task.Type, "size": string(st.task.Size), "ambiguous": st.task.Ambiguous, "via": st.task.Via, "scores": st.task.Scores, "files": st.repo.Files, "tests": st.repo.HasTests})
	if !a.cfg.ojJudgeOn() {
		return ""
	}
	if r := ojPinned(a.cfg.Roles.Editor); r != "" {
		st.editor = r
		dbg("route", map[string]any{"role": "editor", "task": st.task.Type, "chosen": r, "reason": "pinned (roles.editor)"})
		return r
	}
	if a.cfg.ojFusionOn() {
		if r := ojPinned(a.cfg.Fusion.Lead); r != "" {
			st.editor = r
			dbg("route", map[string]any{"role": "lead", "task": st.task.Type, "chosen": r, "reason": "pinned (fusion.lead)"})
			return r
		}
	}
	refs := a.ojPool()
	if len(refs) == 0 {
		return ""
	}
	cands, reason := ojRank(a.cfg.ojCands(refs, st.task.Type), a.cfg.ojEpsilon(), ojRandom)
	st.editor = cands[0].Ref
	role := "editor"
	if a.cfg.ojFusionOn() {
		role = "lead"
	}
	dbg("route", map[string]any{"role": role, "task": st.task.Type, "size": string(st.task.Size), "cands": cands, "chosen": st.editor, "reason": reason, "eps": a.cfg.ojEpsilon()})
	return st.editor
}

// ojPool: the main-turn candidates the router allows (Need-filtered,
// dead refs dropped, paid only when the policy allows it).
func (a *Agent) ojPool() []string {
	need := Need{Tokens: a.estTokens() + 8000, Tools: true, Role: "main"}
	refs := a.cfg.CandidatesFor(need, a.Model)
	var out []string
	for _, r := range refs {
		if isOmniRef(r) || pvManualOnly[r] {
			continue
		}
		if !a.cfg.pvIsFreeish(r) && a.cfg.Omni.Paid != "always" && !a.cfg.isSubRef(r) {
			continue
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		return refs
	}
	return out
}

// ojOrder reorders the fallback candidates after the first (the turn's model)
// by the judge's score for this turn's task type. Main requests, judge on.
func (a *Agent) ojOrder(role string, cands []string) []string {
	if a.oj == nil || !a.oj.typed || role != "main" || len(cands) < 3 || !a.cfg.ojJudgeOn() {
		return cands
	}
	scored, _ := ojRank(a.cfg.ojCands(cands[1:], a.oj.task.Type), 0, nil)
	out := append(make([]string, 0, len(cands)), cands[0])
	for _, x := range scored {
		out = append(out, x.Ref)
	}
	if k := strings.Join(out, ","); k != a.oj.lastOrder {
		a.oj.lastOrder = k
		dbg("route", map[string]any{"role": "fallback", "task": a.oj.task.Type, "first": cands[0], "order": out[1:], "cands": scored, "reason": "ucb (next-best after a stall or error)"})
	}
	return out
}

// ojOutcome maps a turn's end to the scoreboard ("" = not recorded).
func ojOutcome(stop StopReason, editedUnverified bool) string {
	switch stop {
	case StopDone:
		if editedUnverified {
			return "fail"
		}
		return "ok"
	case StopTimeout:
		return "timeout"
	case StopTruncated:
		return "cutoff"
	case StopQualityFailed, StopUntilFailed, StopStepLimit, StopLoop, StopUnverified, StopError:
		return "fail"
	}
	return "" // interrupted, denied, budget: the user's call, not the model's
}

// ojEndTurn records the turn for the model that gave the final answer
// (called from bgEndTurn, before the flush).
func (a *Agent) ojEndTurn(stop StopReason) {
	if a.sub || a.oj == nil || !a.oj.typed {
		return // a subagent's requests stay in its parent's turn
	}
	st := a.oj
	st.typed = false
	acc := ojTakeAcc()
	unverified := false
	if h := a.hg; h != nil && h.codeEdited && h.lastEdit > h.lastTest && a.tools != nil && hgTestCommand(a.tools.Root) != "" {
		unverified = true
	}
	if stop == StopInterrupted && ojTimedOut != nil && ojTimedOut() {
		stop = StopTimeout // headless --timeout cancels the turn (seen 2026-10-20: was dropped as "interrupted")
	}
	out := ojOutcome(stop, unverified)
	ref := a.TurnModel()
	if out == "" || ref == "" {
		dbg("route", map[string]any{"role": "outcome", "task": st.task.Type, "model": ref, "stop": string(stop), "recorded": false})
		return
	}
	d := ojStat{Wall: time.Since(st.t0).Seconds(), Cost: a.turnUsage.Cost}
	switch out {
	case "ok":
		d.OK = 1
	case "timeout":
		d.Timeout = 1
	case "cutoff":
		d.Cutoff = 1
	default:
		d.Fail = 1
	}
	for r, x := range acc {
		if r == ref {
			d.Reqs, d.LatN, d.MS, d.R429, d.Stalls = x.reqs, x.latN, x.ms, x.r429, x.stalls
			continue
		}
		// other refs of the turn (fallbacks, classifiers): their requests,
		// 429s and stalls count, the outcome does not
		if x.reqs+x.stalls > 0 {
			ojRecord(r, st.task.Type, ojStat{Reqs: x.reqs, LatN: x.latN, MS: x.ms, R429: x.r429, Stalls: x.stalls})
		}
	}
	ojRecord(ref, st.task.Type, d)
	dbg("route", map[string]any{"role": "outcome", "task": st.task.Type, "size": string(st.task.Size), "model": ref, "picked": st.editor, "stop": string(stop), "outcome": out, "unverified_edit": unverified, "wall_s": math.Round(d.Wall), "reqs": d.Reqs, "r429": d.R429, "stalls": d.Stalls, "recorded": true})
}

// ---------- jazz models --scores ----------

// ojPrintScores prints the scoreboard (PT-BR).
func ojPrintScores(cfg *Config, w io.Writer) {
	all := ojAllStats()
	if len(all) == 0 {
		fmt.Fprintln(w, "placar vazio: nenhum turno registrado ainda (", bgScoresPath(), ")")
	} else {
		keys := make([]string, 0, len(all))
		for k := range all {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintf(w, "placar por modelo e tipo de tarefa (%s)\n", bgScoresPath())
		fmt.Fprintf(w, "%-46s %-10s %5s %6s %4s %4s %5s %7s %6s %6s %7s\n", "modelo", "tipo", "turn", "acerto", "tmo", "cut", "req/t", "lat(s)", "429%", "min/t", "custo")
		for _, k := range keys {
			s := all[k]
			ref, typ, _ := strings.Cut(k, "|")
			n := s.turns()
			pct, rpt, wall := "-", "-", "-"
			if n > 0 {
				pct = fmt.Sprintf("%.0f%%", 100*float64(s.OK)/float64(n))
				rpt = fmt.Sprintf("%.1f", float64(s.Reqs)/float64(n))
				wall = fmt.Sprintf("%.1f", s.Wall/float64(n)/60)
			}
			lat, r429 := "-", "-"
			if s.LatN > 0 {
				lat = fmt.Sprintf("%.1f", s.MS/float64(s.LatN)/1000)
			}
			if s.Reqs > 0 {
				r429 = fmt.Sprintf("%.0f", 100*float64(s.R429)/float64(s.Reqs))
			}
			fmt.Fprintf(w, "%-46s %-10s %5d %6s %4d %4d %5s %7s %6s %6s %7.4f\n", oneLine(ref, 46), typ, n, pct, s.Timeout, s.Cutoff, rpt, lat, r429, wall, s.Cost)
		}
	}
	fmt.Fprintln(w, "\nsementes (prior, de evals medidas; peso 0,5 por tarefa, no máximo 6):")
	for _, p := range ojPriors {
		fmt.Fprintf(w, "  %-34s %-10s %d/%d  %4.0fs\n", p.glob, p.typ, p.pass, p.n, p.wall)
	}
	state := "desligado (ligue com --omni-judge on ou judge.mode: on)"
	if cfg != nil && cfg.ojJudgeOn() {
		state = fmt.Sprintf("ligado, exploração ε=%.2f", cfg.ojEpsilon())
	}
	fmt.Fprintln(w, "seleção pelo juiz:", state)
}
