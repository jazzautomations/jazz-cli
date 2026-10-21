package main

// scores.go: measured per-model quality, used to order the fallback chain.
// Every --until run and every quality-gate evaluation is a pass or a fail for
// the model that gave the stopping answer; every first-byte stall counts
// against the model too, and successful requests feed a latency average.
// The fallback candidates after the first one (the model asked for) are
// sorted by that score, with speed as the tiebreaker, but only inside runs of
// the same price class, so a paid model never jumps ahead of free ones (the
// omni.paid policy and free-first stay intact). Models with fewer than
// bgScoreMinObs observations keep their configured place.
//
// Data: dataDir()/model-scores.json (merged on save, flock'd, atomic). The
// "tasks" section holds the omni judge's per-(model, task type) records
// (judge.go); both sections are flushed together.
// Config `fallback_order: score|config` (default score), env JAZZ_FALLBACK_ORDER.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// bgScoreMinObs is how many pass/fail/stall observations a model needs before
// its score moves it in the chain.
const bgScoreMinObs = 3

// bgScore is one model's record.
type bgScore struct {
	UntilOK   int     `json:"until_ok,omitempty"`
	UntilFail int     `json:"until_fail,omitempty"`
	QualOK    int     `json:"quality_ok,omitempty"`
	QualFail  int     `json:"quality_fail,omitempty"`
	Stalls    int     `json:"stalls,omitempty"`
	OK        int     `json:"ok,omitempty"` // successful requests
	MS        float64 `json:"ms,omitempty"` // moving average of a successful request, ms
	At        int64   `json:"at,omitempty"` // last update, unix seconds
}

func (s *bgScore) obs() int {
	return s.UntilOK + s.UntilFail + s.QualOK + s.QualFail + s.Stalls
}

// value is the Laplace-smoothed pass rate, discounted by the stall rate.
func (s *bgScore) value() float64 {
	pass, fail := float64(s.UntilOK+s.QualOK), float64(s.UntilFail+s.QualFail)
	q := (pass + 1) / (pass + fail + 2)
	stall := float64(s.Stalls) / float64(s.Stalls+s.OK+2)
	return q * (1 - 0.5*stall)
}

func (s *bgScore) add(d *bgScore) {
	s.UntilOK += d.UntilOK
	s.UntilFail += d.UntilFail
	s.QualOK += d.QualOK
	s.QualFail += d.QualFail
	s.Stalls += d.Stalls
	s.OK += d.OK
	if d.MS > 0 {
		if s.MS <= 0 {
			s.MS = d.MS
		} else {
			s.MS = 0.6*s.MS + 0.4*d.MS
		}
	}
	s.At = max(s.At, d.At)
}

// bgScoreStore is the file's content plus this process's unsaved deltas.
type bgScoreStore struct {
	mu    sync.Mutex
	path  string // file the base was loaded from (JAZZ_HOME may change in tests)
	base  map[string]*bgScore
	delta map[string]*bgScore
}

var bgScoreDB bgScoreStore

func bgScoresPath() string { return filepath.Join(dataDir(), "model-scores.json") }

// bgScoreLocked returns the delta record of ref, loading the base when needed.
func bgScoreLocked(ref string) *bgScore {
	db := bgScoreLoadLocked()
	d := db.delta[ref]
	if d == nil {
		d = &bgScore{}
		db.delta[ref] = d
	}
	d.At = time.Now().Unix()
	return d
}

// bgScoreLoadLocked loads the file once per data dir.
func bgScoreLoadLocked() *bgScoreStore {
	db := &bgScoreDB
	if p := bgScoresPath(); db.path != p || db.base == nil {
		db.path, db.base, db.delta = p, bgReadScores(p), map[string]*bgScore{}
	}
	return db
}

// ojReadTasks reads the judge's section of the file.
func ojReadTasks(p string) map[string]*ojStat {
	out := map[string]*ojStat{}
	if b, err := os.ReadFile(p); err == nil {
		var f struct {
			Tasks map[string]*ojStat `json:"tasks"`
		}
		if json.Unmarshal(b, &f) == nil && f.Tasks != nil {
			out = f.Tasks
		}
	}
	return out
}

func bgReadScores(p string) map[string]*bgScore {
	out := map[string]*bgScore{}
	if b, err := os.ReadFile(p); err == nil {
		var f struct {
			Refs map[string]*bgScore `json:"refs"`
		}
		if json.Unmarshal(b, &f) == nil && f.Refs != nil {
			out = f.Refs
		}
	}
	return out
}

// bgScoreRecord counts a pass/fail of kind "until" or "quality" for ref.
func bgScoreRecord(ref, kind string, ok bool) {
	if ref == "" {
		return
	}
	bgScoreDB.mu.Lock()
	defer bgScoreDB.mu.Unlock()
	d := bgScoreLocked(ref)
	switch {
	case kind == "until" && ok:
		d.UntilOK++
	case kind == "until":
		d.UntilFail++
	case ok:
		d.QualOK++
	default:
		d.QualFail++
	}
}

// bgScoreStall counts a first-byte stall of ref.
func bgScoreStall(ref string) {
	bgScoreDB.mu.Lock()
	defer bgScoreDB.mu.Unlock()
	bgScoreLocked(ref).Stalls++
	ojObserveStall(ref)
}

// bgScoreOK counts a successful request of ref that took d.
func bgScoreOK(ref string, d time.Duration) {
	bgScoreDB.mu.Lock()
	defer bgScoreDB.mu.Unlock()
	ojObserveOK(ref, d)
	s := bgScoreLocked(ref)
	s.OK++
	ms := float64(d.Milliseconds())
	if s.MS <= 0 {
		s.MS = ms
	} else {
		s.MS = 0.7*s.MS + 0.3*ms
	}
}

// bgScoresFlush merges this process's deltas into the file.
func bgScoresFlush() {
	db := &bgScoreDB
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.path == "" {
		db.path = bgScoresPath()
	}
	if len(db.delta) == 0 && !ojHasDelta() {
		return
	}
	if err := os.MkdirAll(filepath.Dir(db.path), 0o700); err != nil {
		return
	}
	unlock, err := pvFlock(db.path+".lock", 2*time.Second)
	if err != nil {
		return // keep the deltas for the next flush
	}
	defer unlock()
	cur := bgReadScores(db.path)
	for ref, d := range db.delta {
		s := cur[ref]
		if s == nil {
			s = &bgScore{}
			cur[ref] = s
		}
		s.add(d)
	}
	tasks := ojReadTasks(db.path)
	ojMergeInto(tasks)
	b, err := json.MarshalIndent(map[string]any{"v": 2, "refs": cur, "tasks": tasks}, "", " ")
	if err != nil || writeFileAtomic(db.path, b, 0o600) != nil {
		return
	}
	db.base, db.delta = cur, map[string]*bgScore{}
	ojCommit(tasks)
}

// bgScoreOf is the merged (file + unsaved) record of ref.
func bgScoreOf(ref string) bgScore {
	bgScoreDB.mu.Lock()
	defer bgScoreDB.mu.Unlock()
	db := bgScoreLoadLocked()
	var s bgScore
	if b := db.base[ref]; b != nil {
		s = *b
	}
	if d := db.delta[ref]; d != nil {
		s.add(d)
	}
	return s
}

func (a *Agent) scoreOrderOn() bool {
	v := strings.ToLower(strings.TrimSpace(a.cfg.FallbackOrder))
	if e := strings.ToLower(strings.TrimSpace(os.Getenv("JAZZ_FALLBACK_ORDER"))); e != "" {
		v = e
	}
	return v != "config"
}

// bgOrderCands sorts the fallback candidates (all but the first) by measured
// quality, then speed, within runs of the same price class.
func (a *Agent) bgOrderCands(cands []string) []string {
	if len(cands) < 3 || !a.scoreOrderOn() {
		return cands
	}
	out := append(make([]string, 0, len(cands)), cands[0])
	rest := cands[1:]
	for i := 0; i < len(rest); {
		free := a.cfg.pvIsFreeish(rest[i])
		j := i + 1
		for j < len(rest) && a.cfg.pvIsFreeish(rest[j]) == free {
			j++
		}
		out = append(out, bgSortRun(rest[i:j])...)
		i = j
	}
	return out
}

// bgSortRun orders refs by score bucket (0.1 wide; too few observations = the
// 0.5 prior), stable; inside a bucket, refs with a known speed are sorted by
// it among their own positions, the others keep their place.
func bgSortRun(refs []string) []string {
	if len(refs) < 2 {
		return refs
	}
	type item struct {
		ref    string
		bucket int
		ms     float64 // 0 = unknown
	}
	items := make([]item, len(refs))
	for i, r := range refs {
		s := bgScoreOf(r)
		it := item{ref: r, bucket: 5}
		if s.obs() >= bgScoreMinObs {
			it.bucket = int(s.value()*10 + 1e-9)
		}
		if s.OK >= bgScoreMinObs {
			it.ms = s.MS
		}
		items[i] = it
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].bucket > items[j].bucket })
	for i := 0; i < len(items); {
		j := i + 1
		for j < len(items) && items[j].bucket == items[i].bucket {
			j++
		}
		var pos []int
		var known []item
		for k := i; k < j; k++ {
			if items[k].ms > 0 {
				pos = append(pos, k)
				known = append(known, items[k])
			}
		}
		sort.SliceStable(known, func(x, y int) bool { return known[x].ms < known[y].ms })
		for n, k := range pos {
			items[k] = known[n]
		}
		i = j
	}
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.ref
	}
	return out
}
