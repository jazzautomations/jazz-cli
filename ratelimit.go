package main

// ratelimit.go: Retry-After handling, free-tier quota tracking and the omni limiter (G4). Owner: providers.
// Contract surface (CONTRACT.md): RetryDelay, MarkExhausted, ExhaustedUntil.
//
// The providers do the bookkeeping themselves (OpenAIProvider/AnthropicProvider call
// rlBegin/rlEnd around every request), so the router (omni.go) knows which candidates
// are Ready, Waiting or Dead without any help from the agent loop. State persists in
// dataDir()/ratelimit.json (0600), merged under an flock so concurrent jazz processes
// (an SSH tab plus a cron `jazz -p`) share one view of the quotas.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Limits describes a free-tier quota. Zero fields are unknown/unlimited.
type Limits struct {
	RPM      int    `json:"rpm,omitempty"`
	RPH      int    `json:"rph,omitempty"`
	RPD      int    `json:"rpd,omitempty"`
	TPM      int    `json:"tpm,omitempty"`
	TPD      int    `json:"tpd,omitempty"`
	Monthly  int    `json:"monthly,omitempty"`  // requests per calendar month (UTC)
	Lifetime int    `json:"lifetime,omitempty"` // total tokens ever (one-time credit): stop at 95%
	Conc     int    `json:"conc,omitempty"`     // concurrent requests
	MaxReq   int    `json:"max_req,omitempty"`  // max prompt+max_tokens per request
	Scope    string `json:"scope,omitempty"`    // model (default) | key (one budget for the whole provider)
	Reset    string `json:"reset,omitempty"`    // UTC (default) | PT (America/Los_Angeles) | rolling
}

type rlBucket struct {
	Day       string           `json:"day,omitempty"`
	Req       int              `json:"req,omitempty"`
	Tok       int              `json:"tok,omitempty"`
	Month     string           `json:"month,omitempty"`
	MReq      int              `json:"mreq,omitempty"`
	Life      int              `json:"life,omitempty"`
	Recent    []int64          `json:"recent,omitempty"`     // unix ms of requests in the last hour
	RecentTok [][2]int64       `json:"recent_tok,omitempty"` // [ms, tokens] in the last minute
	CoolUntil int64            `json:"cool,omitempty"`       // short cooldown (unix ms)
	DeadUntil int64            `json:"dead,omitempty"`       // quota exhausted / gone (unix ms)
	Strikes   int              `json:"strikes,omitempty"`    // consecutive 429s
	Trans     int              `json:"trans,omitempty"`      // consecutive transient errors
	HdrReq    int              `json:"hdr_req,omitempty"`    // remaining requests (day) learned from headers
	HdrAt     int64            `json:"hdr_at,omitempty"`     // when HdrReq was learned (0 = unknown)
	HdrReset  int64            `json:"hdr_reset,omitempty"`  // when HdrReq resets (unix ms)
	LRPD      int              `json:"lrpd,omitempty"`       // learned daily request limit (OpenRouter /key)
	AuthBad   string           `json:"auth_bad,omitempty"`   // fingerprint of a refused key
	AuthBadS  map[string]int64 `json:"auth_bad_s,omitempty"` // refused key fingerprints → when (key pools)
	Gone      int64            `json:"gone,omitempty"`       // model retired/removed by the provider (unix ms; no expiry)
	MaxReq    int              `json:"max_req,omitempty"`    // learned from 413 "Limit N"
	LastErr   string           `json:"last_err,omitempty"`
	LastAt    int64            `json:"last_at,omitempty"`
	LastKind  string           `json:"last_kind,omitempty"` // class of the last error: quota | daily | rate | auth | …
	OKAt      int64            `json:"ok_at,omitempty"`     // last success (unix ms), on the provider bucket
	inflight  int
	// this process's increments since its last successful write: the merge
	// adds them to the file's counters, so concurrent jazz processes (eval
	// lanes) do not lose each other's requests (max() alone undercounted)
	dReq, dTok, dMReq, dLife int
}

type rateLimiter struct {
	mu       sync.Mutex
	path     string
	B        map[string]*rlBucket
	dirty    bool
	lastSave time.Time
	lastOK   string // ref of the last successful request (affinity bonus)
	pending  bool   // a deferred save is scheduled
}

var omniRL = &rateLimiter{}

// rlOKAt: when a model request last succeeded in this process (unix nanos;
// 0 = none yet). The startup network probe checks it before warning.
var rlOKAt atomic.Int64

// rlOKSince reports whether a model request succeeded at or after t.
func rlOKSince(t time.Time) bool {
	n := rlOKAt.Load()
	return n != 0 && n >= t.UnixNano()
}

// rlNow is the clock (tests replace it).
var rlNow = time.Now

func rlPath() string { return filepath.Join(dataDir(), "ratelimit.json") }

// loadLocked (re)loads the state when dataDir() changed (tests switch JAZZ_HOME).
func (rl *rateLimiter) loadLocked() {
	p := rlPath()
	if rl.path == p && rl.B != nil {
		return
	}
	rl.path, rl.B, rl.dirty, rl.lastOK = p, map[string]*rlBucket{}, false, ""
	if m, err := rlReadFile(p); err == nil {
		rl.B = m
	} else if !errors.Is(err, os.ErrNotExist) {
		dbg("ratelimit", "arquivo de limites corrompido, começando vazio: "+err.Error())
	}
}

func rlReadFile(p string) (map[string]*rlBucket, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var f struct {
		Buckets map[string]*rlBucket `json:"buckets"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	if f.Buckets == nil {
		f.Buckets = map[string]*rlBucket{}
	}
	for k, v := range f.Buckets {
		if v == nil {
			delete(f.Buckets, k)
		}
	}
	return f.Buckets, nil
}

func (rl *rateLimiter) bucketLocked(key string) *rlBucket {
	b := rl.B[key]
	if b == nil {
		b = &rlBucket{}
		rl.B[key] = b
	}
	return b
}

// saveLocked persists when dirty: immediately when urgent, else at most every 5 s.
func (rl *rateLimiter) saveLocked(urgent bool) {
	if !rl.dirty || rl.path == "" {
		return
	}
	if !urgent && time.Since(rl.lastSave) < 5*time.Second {
		if !rl.pending {
			rl.pending = true
			time.AfterFunc(5*time.Second, func() {
				rl.mu.Lock()
				defer rl.mu.Unlock()
				rl.pending = false
				if rl.path == rlPath() { // tests may have switched JAZZ_HOME meanwhile
					rl.saveLocked(true)
				}
			})
		}
		return
	}
	if err := rl.writeLocked(); err != nil {
		dbg("ratelimit", "falha ao salvar: "+err.Error())
		return
	}
	rl.dirty, rl.lastSave = false, time.Now()
}

// writeLocked merges with the file under an exclusive flock, then writes it.
func (rl *rateLimiter) writeLocked() error {
	dir := filepath.Dir(rl.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	unlock, err := pvFlock(filepath.Join(dir, "ratelimit.lock"), 3*time.Second)
	if err != nil {
		return err
	}
	defer unlock()
	if theirs, err := rlReadFile(rl.path); err == nil {
		for k, t := range theirs {
			if mine, ok := rl.B[k]; ok {
				rlMerge(mine, t)
			} else {
				rl.B[k] = t
			}
		}
	}
	now := rlNow().UnixMilli()
	for k, b := range rl.B { // drop buckets with nothing left to remember
		rlTrim(b, now)
		if b.inflight == 0 && rlEmpty(b, now) {
			delete(rl.B, k)
		}
	}
	data, err := json.Marshal(map[string]any{"buckets": rl.B})
	if err != nil {
		return err
	}
	if err := writeFileAtomic(rl.path, data, 0o600); err != nil {
		return err
	}
	for _, b := range rl.B {
		b.dReq, b.dTok, b.dMReq, b.dLife = 0, 0, 0, 0
	}
	return nil
}

func rlEmpty(b *rlBucket, now int64) bool {
	today := time.UnixMilli(now).UTC().Format("2006-01-02")
	return len(b.Recent) == 0 && len(b.RecentTok) == 0 && b.CoolUntil <= now && b.DeadUntil <= now &&
		b.AuthBad == "" && b.Gone == 0 && b.MaxReq == 0 && b.Life == 0 && b.MReq == 0 && b.LRPD == 0 &&
		(b.Req == 0 || b.Day < today) && b.Strikes == 0 && b.LastErr == "" && b.OKAt < now-7*24*3600_000
}

// rlMerge folds another process's view of a bucket into mine.
func rlMerge(m, t *rlBucket) {
	switch {
	case t.Day == m.Day:
		m.Req, m.Tok = max(m.Req, t.Req+m.dReq), max(m.Tok, t.Tok+m.dTok)
	case t.Day > m.Day:
		m.Day, m.Req, m.Tok = t.Day, t.Req, t.Tok
	}
	switch {
	case t.Month == m.Month:
		m.MReq = max(m.MReq, t.MReq+m.dMReq)
	case t.Month > m.Month:
		m.Month, m.MReq = t.Month, t.MReq
	}
	m.Life = max(m.Life, t.Life+m.dLife)
	seen := map[int64]bool{}
	for _, v := range m.Recent {
		seen[v] = true
	}
	for _, v := range t.Recent {
		if !seen[v] {
			m.Recent = append(m.Recent, v)
		}
	}
	sort.Slice(m.Recent, func(i, j int) bool { return m.Recent[i] < m.Recent[j] })
	seenT := map[int64]bool{}
	for _, v := range m.RecentTok {
		seenT[v[0]] = true
	}
	for _, v := range t.RecentTok {
		if !seenT[v[0]] {
			m.RecentTok = append(m.RecentTok, v)
		}
	}
	m.CoolUntil, m.DeadUntil = max(m.CoolUntil, t.CoolUntil), max(m.DeadUntil, t.DeadUntil)
	m.MaxReq = max(m.MaxReq, t.MaxReq)
	m.LRPD = max(m.LRPD, t.LRPD)
	m.Gone = max(m.Gone, t.Gone) // never cleared by a merge: revival is read from the catalog (rlGoneRevived)
	if m.AuthBad == "" {
		m.AuthBad = t.AuthBad
	}
	if t.HdrAt > m.HdrAt {
		m.HdrReq, m.HdrAt, m.HdrReset = t.HdrReq, t.HdrAt, t.HdrReset
	}
	if t.LastAt > m.LastAt {
		m.LastErr, m.LastAt, m.LastKind = t.LastErr, t.LastAt, t.LastKind
	}
	m.OKAt = max(m.OKAt, t.OKAt)
}

func rlTrim(b *rlBucket, now int64) {
	i := 0
	for i < len(b.Recent) && b.Recent[i] < now-3600_000 {
		i++
	}
	b.Recent = b.Recent[i:]
	j := 0
	for _, v := range b.RecentTok {
		if v[0] >= now-60_000 {
			b.RecentTok[j] = v
			j++
		}
	}
	b.RecentTok = b.RecentTok[:j]
}

// FlushRateLimits writes pending limiter state now (turn end, exit).
func FlushRateLimits() {
	omniRL.mu.Lock()
	defer omniRL.mu.Unlock()
	omniRL.saveLocked(true)
}

// ---- day / month boundaries ----

// pacificOffset is America/Los_Angeles' UTC offset without loading tzdata (antiX may
// lack it and time/tzdata costs ~450 KB): PDT from the 2nd Sunday of March 10:00 UTC
// to the 1st Sunday of November 09:00 UTC, else PST.
func pacificOffset(t time.Time) time.Duration {
	t = t.UTC()
	y := t.Year()
	nthSunday := func(m time.Month, n int) time.Time {
		d := time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
		for d.Weekday() != time.Sunday {
			d = d.AddDate(0, 0, 1)
		}
		return d.AddDate(0, 0, 7*(n-1))
	}
	start := nthSunday(time.March, 2).Add(10 * time.Hour)
	end := nthSunday(time.November, 1).Add(9 * time.Hour)
	if !t.Before(start) && t.Before(end) {
		return -7 * time.Hour
	}
	return -8 * time.Hour
}

// rlDay returns the quota day key and when that day ends.
func rlDay(reset string, now time.Time) (string, time.Time) {
	off := time.Duration(0)
	if strings.EqualFold(reset, "PT") {
		off = pacificOffset(now)
	}
	local := now.UTC().Add(off)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	return start.Format("2006-01-02"), start.Add(24 * time.Hour).Add(-off)
}

func rlMonth(now time.Time) (string, time.Time) {
	u := now.UTC()
	start := time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
	return start.Format("2006-01"), start.AddDate(0, 1, 0)
}

// ---- limits resolution ----

// pvEffLimits merges the model's limits over the provider's and returns the key of
// the bucket that holds the counters. Gateways with FreeIDs (OpenRouter, Kilo,
// Vercel) only limit their free models; their bucket for those is "<prov>/*free".
func (c *Config) pvEffLimits(ref string) (Limits, string) {
	prov, model := SplitModel(ref)
	var lim Limits
	key := ref
	pc := c.Providers[prov]
	if pc != nil && pc.Limits != nil && !(pc.FreeIDs && !pvFreeID(model)) {
		lim = *pc.Limits
		if lim.Scope == "key" {
			key = prov
			if pc.FreeIDs {
				key = prov + "/*free"
			}
		}
	}
	ms := c.ModelSettings(ref)
	if ms.Limits != nil {
		m := *ms.Limits
		pvOverlayLimits(&lim, m)
		if m.Scope != "" {
			lim.Scope = m.Scope
			if m.Scope != "key" {
				key = ref
			}
		}
		if m.Reset != "" {
			lim.Reset = m.Reset
		}
	}
	if ms.MaxReq > 0 && (lim.MaxReq == 0 || ms.MaxReq < lim.MaxReq) {
		lim.MaxReq = ms.MaxReq
	}
	return lim, key
}

// ---- key pools (Cred.Keys) ----
//
// With more than one key for a provider, the limiter tracks per-key state so an
// exhausted or refused key only takes itself down. Counter/dead/cool buckets
// get the key's fingerprint suffix ("<scope>@<fp>": each key keeps its own
// daily/monthly budget). Account-level outcomes (quota 429, no credit) and
// refusals also mark the "<prov>@<fp>" shadow bucket and the provider's
// AuthBadS set, which pickPoolKey consults without knowing the model. Nothing
// changes for single-key providers: no suffix, no shadow, same buckets as
// before.

// rlPoolKey names the counter/dead bucket for (scope bucket, key fingerprint).
func (c *Config) rlPoolKey(prov, bucket, fp string) string {
	if fp == "" || fp == "nokey" || bucket == "" {
		return bucket
	}
	if pc := c.Providers[prov]; pc == nil || pc.Type == "codex" {
		return bucket
	}
	if len(c.APIKeys(prov)) <= 1 {
		return bucket
	}
	return bucket + "@" + fp
}

// rlKeyBad reports whether fingerprint fp was refused for prov.
func (rl *rateLimiter) rlKeyBad(pb *rlBucket, fp string) bool {
	if pb == nil || fp == "" || fp == "nokey" {
		return false
	}
	return pb.AuthBad == fp || pb.AuthBadS[fp] > 0
}

// pickPoolKey returns the first pool key still alive for prov — not refused,
// not account-dead (prov@fp shadow) and, when ref is given, not dead on that
// ref's per-key scope bucket. A key in cooldown is only used when nothing else
// is ready; with every key bad or dead it falls back to keys[0] so the caller
// gets a deterministic key (and the request a clear auth/quota error).
func (c *Config) pickPoolKey(prov, ref string, keys []string) string {
	rl := omniRL
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.loadLocked()
	now := rlNow()
	ms := now.UnixMilli()
	var lim Limits
	scope := ""
	if ref != "" {
		lim, scope = c.pvEffLimits(ref)
	}
	pb := rl.B[prov]
	var cooled string
	for _, k := range keys {
		fp := pvKeyPrint(k)
		if rl.rlKeyBad(pb, fp) {
			continue
		}
		ready := true
		if kb := rl.B[prov+"@"+fp]; kb != nil {
			if kb.DeadUntil > ms {
				continue
			}
			if kb.CoolUntil > ms {
				ready = false
			}
		}
		if ready && scope != "" {
			st := rl.stateLocked(ref, prov, scope+"@"+fp, fp, lim, 0, now)
			if st.Dead {
				continue
			}
			if !st.Ready {
				ready = false
			}
		}
		if ready {
			return k
		}
		if cooled == "" {
			cooled = k
		}
	}
	if cooled != "" {
		return cooled
	}
	return keys[0]
}

// poolRateTries caps the rotations one request spends on plain 429s: a 429 may
// be the upstream model being busy, not the key, and then every key gets it.
const poolRateTries = 2

// poolNext reports whether a request to ref that just failed with kind (the
// limiter has recorded it) should go again with another key of the provider's
// pool: the failure is the key's (refused, out of quota or credit, rate
// limited) and a key is ready right now. tried counts the rotations this
// request already made; they never exceed the pool size.
func (c *Config) poolNext(ref, kind string, tokens, tried int) bool {
	switch kind {
	case "auth", "daily", "credit":
	case "rate":
		if tried >= poolRateTries {
			return false
		}
	default:
		return false
	}
	prov, _ := SplitModel(ref)
	return tried+1 < len(c.APIKeys(prov)) && c.pvState(ref, tokens).Ready
}

func pvOverlayLimits(dst *Limits, s Limits) {
	set := func(d *int, v int) {
		if v > 0 {
			*d = v
		}
	}
	set(&dst.RPM, s.RPM)
	set(&dst.RPH, s.RPH)
	set(&dst.RPD, s.RPD)
	set(&dst.TPM, s.TPM)
	set(&dst.TPD, s.TPD)
	set(&dst.Monthly, s.Monthly)
	set(&dst.Lifetime, s.Lifetime)
	set(&dst.Conc, s.Conc)
	set(&dst.MaxReq, s.MaxReq)
}

func (l Limits) has() bool {
	return l.RPM+l.RPH+l.RPD+l.TPM+l.TPD+l.Monthly+l.Lifetime+l.Conc > 0
}

// pvFreeID reports a model id that a router serves for free.
func pvFreeID(model string) bool {
	return strings.HasSuffix(model, ":free") || strings.HasSuffix(model, "-free") || strings.HasSuffix(model, "/free") || model == "free"
}

// pvKeyPrint fingerprints a key so a refused one is remembered without storing it.
func pvKeyPrint(key string) string {
	if key == "" {
		return "nokey"
	}
	s := sha256.Sum256([]byte(key))
	return hex.EncodeToString(s[:4])
}

// ---- state ----

type rlState struct {
	Ready  bool
	Dead   bool          // unusable until Until (quota, auth, gone)
	TooBig bool          // this request does not fit the model's per-request cap
	Wait   time.Duration // > 0 when a short wait makes it Ready
	Until  time.Time     // for Dead
	Why    string        // PT-BR reason
	Frac   float64       // remaining fraction of the tightest daily/monthly budget (1 = unknown)
}

// pvState reports whether ref can take a request of `tokens` tokens now.
func (c *Config) pvState(ref string, tokens int) rlState {
	lim, key := c.pvEffLimits(ref)
	prov, _ := SplitModel(ref)
	fp := ""
	if pc := c.Providers[prov]; pc != nil && pc.Type != "codex" {
		fp = pvKeyPrint(c.APIKeyFor(ref))
	}
	key = c.rlPoolKey(prov, key, fp)
	rl := omniRL
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.loadLocked()
	return rl.stateLocked(ref, prov, key, fp, lim, tokens, rlNow())
}

func (rl *rateLimiter) stateLocked(ref, prov, key, fp string, lim Limits, tokens int, now time.Time) rlState {
	ms := now.UnixMilli()
	st := rlState{Ready: true, Frac: 1}
	dead := func(until int64, why string) rlState {
		return rlState{Dead: true, Until: time.UnixMilli(until), Wait: time.Duration(until-ms) * time.Millisecond, Why: why}
	}
	if pb := rl.B[prov]; rl.rlKeyBad(pb, fp) {
		return rlState{Dead: true, Until: now.Add(100 * 365 * 24 * time.Hour), Why: "credencial recusada — rode /omni add " + prov}
	}
	if mb := rl.B[ref]; mb != nil && mb.Gone > 0 && !rlGoneRevived(ref, mb.Gone) {
		// retired/removed (410, "end of life", 404): no expiry — only the
		// provider's catalog listing it again, or `jazz omni reset`, revives it
		return rlState{Dead: true, Until: now.Add(100 * 365 * 24 * time.Hour), Why: "modelo aposentado/removido pelo provedor"}
	}
	var cool int64
	check := rlKeys(ref, prov, key)
	if fp != "" && fp != "nokey" {
		check = append(check, prov+"@"+fp) // per-key shadow (key pools)
	}
	for _, k := range check {
		b := rl.B[k]
		if b == nil {
			continue
		}
		if b.DeadUntil > ms {
			return dead(b.DeadUntil, "cota esgotada")
		}
		cool = max(cool, b.CoolUntil)
	}
	maxReq := lim.MaxReq
	if mb := rl.B[ref]; mb != nil && mb.MaxReq > 0 && (maxReq == 0 || mb.MaxReq < maxReq) {
		maxReq = mb.MaxReq
	}
	if maxReq > 0 && tokens > maxReq {
		return rlState{TooBig: true, Why: fmt.Sprintf("pedido grande demais para o modelo (%d > %d tokens)", tokens, maxReq)}
	}
	if lim.TPM > 0 && tokens > lim.TPM {
		return rlState{TooBig: true, Why: fmt.Sprintf("pedido maior que o limite por minuto (%d > %d tokens)", tokens, lim.TPM)}
	}
	b := rl.B[key]
	if b != nil {
		rlTrim(b, ms)
		day, dayEnd := rlDay(lim.Reset, now)
		if b.Day != day {
			b.Day, b.Req, b.Tok = day, 0, 0
		}
		rpd := lim.RPD
		if b.LRPD > 0 {
			rpd = b.LRPD
		}
		if rpd > 0 {
			if b.Req >= rpd {
				return dead(dayEnd.UnixMilli(), fmt.Sprintf("limite diário (%d/%d)", b.Req, rpd))
			}
			st.Frac = min(st.Frac, float64(rpd-b.Req)/float64(rpd))
		}
		if b.HdrAt > 0 && b.HdrReq == 0 {
			reset := b.HdrReset
			if reset == 0 {
				reset = dayEnd.UnixMilli()
			}
			if reset > ms {
				return dead(reset, "limite diário (cabeçalhos)")
			}
		}
		if lim.TPD > 0 {
			if b.Tok+tokens > lim.TPD {
				return dead(dayEnd.UnixMilli(), "limite diário de tokens")
			}
			st.Frac = min(st.Frac, float64(lim.TPD-b.Tok)/float64(lim.TPD))
		}
		if lim.Monthly > 0 {
			mon, monEnd := rlMonth(now)
			if b.Month != mon {
				b.Month, b.MReq = mon, 0
			}
			if b.MReq >= lim.Monthly {
				return dead(monEnd.UnixMilli(), "limite mensal")
			}
			st.Frac = min(st.Frac, float64(lim.Monthly-b.MReq)/float64(lim.Monthly))
		}
		if lim.Lifetime > 0 && b.Life >= lim.Lifetime*95/100 {
			return dead(now.Add(100*365*24*time.Hour).UnixMilli(), "crédito único usado (95%)")
		}
		if lim.Conc > 0 && b.inflight >= lim.Conc {
			st.Ready, st.Wait, st.Why = false, 2*time.Second, "requisição simultânea em andamento"
		}
		wait := func(window int64, limit int, why string) {
			if limit <= 0 {
				return
			}
			var in []int64
			for _, t := range b.Recent {
				if t >= ms-window {
					in = append(in, t)
				}
			}
			if len(in) >= limit {
				w := time.Duration(in[len(in)-limit]+window-ms) * time.Millisecond
				if w > st.Wait {
					st.Ready, st.Wait, st.Why = false, w, why
				}
			}
		}
		wait(60_000, lim.RPM, "limite por minuto")
		wait(3600_000, lim.RPH, "limite por hora")
		if lim.TPM > 0 {
			sum := 0
			for _, v := range b.RecentTok {
				sum += int(v[1])
			}
			if sum+tokens > lim.TPM && len(b.RecentTok) > 0 {
				w := time.Duration(b.RecentTok[0][0]+60_000-ms) * time.Millisecond
				if w > st.Wait {
					st.Ready, st.Wait, st.Why = false, w, "limite de tokens por minuto"
				}
			}
		}
	}
	if cool > ms {
		w := time.Duration(cool-ms) * time.Millisecond
		if w > st.Wait {
			st.Ready, st.Wait, st.Why = false, w, "esfriando após limite (429)"
		}
	}
	return st
}

// rlGoneRevived reports whether the provider's catalog (models-cache.json,
// fetched from /models of kilo and OpenRouter) listed ref again after it was
// marked gone at goneAt (unix ms). Providers without a catalog never revive
// on their own: `jazz omni reset <provedor>` clears the mark.
var rlGoneRevived = func(ref string, goneAt int64) bool {
	prov, _ := SplitModel(ref)
	listed := pvListedAt(prov)
	if listed.IsZero() || listed.UnixMilli() <= goneAt {
		return false
	}
	m, ok := pvCachedRefs(prov)[ref]
	return ok && m.At >= listed.Unix()
}

// rlKeys lists the buckets whose Dead/Cool apply to ref.
func rlKeys(ref, prov, key string) []string {
	ks := []string{ref}
	if key != ref {
		ks = append(ks, key)
	}
	if key != prov {
		ks = append(ks, prov)
	}
	return ks
}

// ---- request accounting (called by the providers) ----

// rlBegin records a request about to be sent (no-op for unlimited refs). fp is
// the fingerprint of the key actually used ("" = derive, as before).
func (c *Config) rlBegin(ref string, tokens int, fp string) {
	if c == nil {
		return
	}
	lim, key := c.pvEffLimits(ref)
	if !lim.has() {
		return
	}
	prov, _ := SplitModel(ref)
	if fp == "" {
		fp = pvKeyPrint(c.APIKey(prov))
	}
	key = c.rlPoolKey(prov, key, fp)
	now := rlNow()
	ms := now.UnixMilli()
	rl := omniRL
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.loadLocked()
	b := rl.bucketLocked(key)
	rlTrim(b, ms)
	day, _ := rlDay(lim.Reset, now)
	if b.Day != day {
		b.Day, b.Req, b.Tok, b.dReq, b.dTok = day, 0, 0, 0, 0
	}
	b.Req++
	b.dReq++
	if lim.Monthly > 0 {
		mon, _ := rlMonth(now)
		if b.Month != mon {
			b.Month, b.MReq, b.dMReq = mon, 0, 0
		}
		b.MReq++
		b.dMReq++
	}
	if lim.RPM > 0 || lim.RPH > 0 {
		b.Recent = append(b.Recent, ms)
	}
	if lim.TPM > 0 {
		b.RecentTok = append(b.RecentTok, [2]int64{ms, int64(tokens)})
	}
	if b.HdrAt > 0 && b.HdrReq > 0 {
		b.HdrReq--
	}
	b.inflight++
	rl.dirty = true
	rl.saveLocked(false)
}

// rlEnd records the outcome of a request started with rlBegin. fp is the
// fingerprint of the key the request used — the provider pins it at build
// time, so a rotated pick cannot split the accounting across buckets.
func (c *Config) rlEnd(ref string, tokens int, u Usage, err error, fp string) {
	if c == nil {
		return
	}
	ojObserveReq(ref, err) // per-turn request/429 counts for the judge (judge.go)
	lim, key := c.pvEffLimits(ref)
	prov, _ := SplitModel(ref)
	if fp == "" {
		if pc := c.Providers[prov]; pc != nil && pc.Type != "codex" {
			fp = pvKeyPrint(c.APIKey(prov))
		}
	}
	key = c.rlPoolKey(prov, key, fp)
	now := rlNow()
	rl := omniRL
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.loadLocked()
	if lim.has() {
		b := rl.bucketLocked(key)
		if b.inflight > 0 {
			b.inflight--
		}
		used := u.In + u.Out
		if used == 0 && err == nil {
			used = tokens
		}
		if err == nil {
			b.Tok += used
			b.dTok += used
			if lim.Lifetime > 0 {
				b.Life += used
				b.dLife += used
			}
			rl.dirty = true
		}
	}
	urgent := rl.classifyLocked(c, ref, prov, key, fp, lim, tokens, err, now)
	rl.saveLocked(urgent)
}

var (
	pvTooBigRE = lazyRx(`(?i)request too large|limit (\d+), requested (\d+)`)
	pvLimitNRE = lazyRx(`(?i)limit:?\s*(\d+),?\s*requested:?\s*(\d+)`)
	// pvEOLRE: a model the provider retired (NVIDIA answers HTTP 410 "reached
	// its end of life on 2026-10-28"); it never comes back by waiting.
	pvEOLRE = lazyRx(`(?i)end[ -]of[ -]life|has been retired|\bretired\b|no longer available|deprecated and removed`)
	// pvBadKeyRE: a refused key answered with HTTP 400 (Gemini: "API key not
	// valid. Please pass a valid API key." / API_KEY_INVALID).
	// Only a key being refused counts — a 400 that merely mentions "API key"
	// (BYOK hints, docs links) must not kill every model behind the key.
	pvBadKeyRE = lazyRx(`(?i)api[ _-]?key[^.]{0,40}\b(not valid|invalid|expired|incorrect|revoked|missing|required)|\b(valid|invalid|incorrect|expired|revoked|missing|bad)[ _-]api[ _-]?key|API_KEY_INVALID|unauthenticated`)
	// pvTooManyToolsRE: the request carried more tools than the provider takes
	// (Groq: "maximum number of items is 128").
	pvTooManyToolsRE = lazyRx(`(?i)maximum number of items is (\d+)`)
	pvGoneRE         = lazyRx(`(?i)model.{0,80}(not found|does not exist|decommission|not available|no longer|is not supported|invalid model)|no endpoints found(?: for| matching)`)
	pvNoToolsRE      = lazyRx(`(?i)(tool|function.?call)[^.]{0,80}(not supported|unsupported|does not support|not available)|does not support (tools|function)|no endpoints found that support tool use`)
	// pvToolCallRE marks provider rejections of the model's own generated tool
	// call (invented name, undecodable arguments): Groq's tool_use_failed and
	// equivalent errors from OpenAI-compatible servers, often delivered as a
	// status-0 SSE error event. Not the transport's fault and not the
	// endpoint's: the model generated something invalid.
	pvToolCallRE = lazyRx(`(?i)tool[ _-]?calls?[ _-]?validation failed|attempted to call tool|not in request[. _]?tools|failed to parse tool[ _-]?call|tool_use_failed|invalid tool[ _-]?calls?`)
	// pvToolCallNameRE pulls the invented name out of the common phrasings
	// ("attempted to call tool 'x'", "called tool \"x\"").
	pvToolCallNameRE = lazyRx(`(?i)(?:attempted to call|called?|invoke[sd]?|use[sd]?) (?:the )?tool ['"` + "`" + `]?([\w.:/-]+)['"` + "`" + `]?`)
)

// pvToolCallName extracts the tool name a provider says the model attempted
// ("" when the error does not name one, e.g. "Failed to parse tool call
// arguments as JSON").
func pvToolCallName(err error) string {
	var ae *APIError
	if !errors.As(err, &ae) {
		return ""
	}
	if m := pvToolCallNameRE.FindStringSubmatch(ae.Body); m != nil {
		return m[1]
	}
	return ""
}

// pvClassify names the error kind: rate, daily, toobig, credit, auth, freetier,
// gone, notools, toomanytools, toolcall, overflow, transient, cancel or other.
// It is the one classifier: the agent's fallback loop, the limiter state
// (classifyLocked), the final message (friendlyErr) and `jazz omni health`
// (omniHealthKind) all read it.
func pvClassify(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, errStreamIdle) {
		return "transient"
	}
	var ae *APIError
	if !errors.As(err, &ae) {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "cancel"
		}
		return "other"
	}
	b := ae.Body
	switch {
	case strings.Contains(b, "FreeTierError"):
		return "freetier"
	case ae.Daily:
		return "daily"
	case ae.Status == 413 || pvTooBigRE.MatchString(b):
		return "toobig"
	case ae.Status == 429:
		return "rate"
	case ae.Status == 402 || (ae.Status == 400 && strings.Contains(strings.ToLower(b), "credit balance")):
		// Anthropic reports an empty balance as HTTP 400 invalid_request_error
		return "credit"
	case ae.Status == 401 || ae.Status == 403:
		return "auth"
	case ae.Status == 400 && pvBadKeyRE.MatchString(b):
		// before "gone": a bad key must never tombstone a live model
		return "auth"
	case ae.Status == 410 || ((ae.Status == 400 || ae.Status == 404 || ae.Status == 422) && pvEOLRE.MatchString(b)):
		return "gone"
	case ae.Status == 400 && pvTooManyToolsRE.MatchString(b):
		return "toomanytools"
	case (ae.Status == 400 || ae.Status == 404 || ae.Status == 422) && pvNoToolsRE.MatchString(b):
		return "notools"
	case pvToolCallRE.MatchString(b):
		// the provider rejected the model's generated tool call — often a
		// status-0 SSE error event, so this must come before "transient"
		return "toolcall"
	case pvIsOverflow(ae):
		return "overflow"
	case ae.Status == 404 || (ae.Status == 400 && pvGoneRE.MatchString(b)):
		return "gone"
	case ae.Status == 0 || ae.Status >= 500 || ae.Status == 408:
		return "transient"
	}
	return "other"
}

func pvIsOverflow(ae *APIError) bool {
	if ae.Status != 400 && ae.Status != 413 && ae.Status != 422 {
		return false
	}
	b := strings.ToLower(ae.Body)
	return strings.Contains(b, "context_length") || strings.Contains(b, "prompt is too long") || strings.Contains(b, "too many tokens") ||
		(strings.Contains(b, "context") && (strings.Contains(b, "exceed") || strings.Contains(b, "too long") || strings.Contains(b, "maximum") || strings.Contains(b, "window")))
}

// classifyLocked applies an outcome to the buckets. Returns true when the change
// must be persisted right away (dead/auth).
func (rl *rateLimiter) classifyLocked(c *Config, ref, prov, key, fp string, lim Limits, tokens int, err error, now time.Time) bool {
	ms := now.UnixMilli()
	if err == nil {
		for _, k := range rlKeys(ref, prov, key) {
			if b := rl.B[k]; b != nil && (b.Strikes > 0 || b.Trans > 0 || b.LastErr != "") {
				b.Strikes, b.Trans, b.LastErr = 0, 0, ""
				rl.dirty = true
			}
		}
		rl.lastOK = ref
		rlOKAt.Store(now.UnixNano())
		if lim.has() { // unlimited refs leave no state
			rl.bucketLocked(prov).OKAt = ms
			rl.dirty = true
		}
		return false
	}
	kind := pvClassify(err)
	if kind == "cancel" || kind == "other" {
		return false
	}
	var ae *APIError
	errors.As(err, &ae)
	ra := time.Duration(0)
	if ae != nil {
		ra = ae.RetryAfter
	}
	note := func(b *rlBucket) {
		s := err.Error()
		if len(s) > 160 {
			s = s[:160] + "…"
		}
		b.LastErr, b.LastAt, b.LastKind = s, ms, kind
		if ae != nil && ae.Quota {
			b.LastKind = "quota"
		}
	}
	rl.dirty = true
	// key pools: account-level outcomes (quota, credit, refusal) belong to the
	// key, model-level ones to its scope bucket. shadow() is the "<prov>@<fp>"
	// bucket pickPoolKey checks without knowing the model; model-scope marks
	// (a daily cap that lives on "<ref>@<fp>") must not poison the key for
	// other models, so they only shadow when the scope is provider-wide.
	pooled := fp != "" && fp != "nokey" && c != nil && len(c.APIKeys(prov)) > 1
	shadow := func(force bool) *rlBucket {
		if !pooled {
			return nil
		}
		if !force && strings.TrimSuffix(key, "@"+fp) == ref {
			return nil
		}
		return rl.bucketLocked(prov + "@" + fp)
	}
	switch kind {
	case "rate":
		b := rl.bucketLocked(key)
		b.Strikes++
		ladder := []time.Duration{20 * time.Second, 60 * time.Second, 180 * time.Second, 600 * time.Second}
		cool := max(ra, ladder[min(b.Strikes-1, 3)])
		b.CoolUntil = max(b.CoolUntil, now.Add(cool).UnixMilli())
		if s := shadow(false); s != nil {
			s.CoolUntil = max(s.CoolUntil, b.CoolUntil)
		}
		note(b)
		return false
	case "daily":
		b := rl.bucketLocked(key)
		if ae != nil && ae.Quota {
			// a quota 429 (e.g. kilo PROMOTION_MODEL_LIMIT_REACHED) cools the whole
			// account: with a key pool that is this key's shadow (rotation → next
			// key), without one the provider bucket (rotation → other providers);
			// it is not a quality signal (judge.go)
			if s := shadow(true); s != nil {
				b = s
			} else if !pooled {
				b = rl.bucketLocked(prov)
			}
			until := now.Add(pvQuotaDefault)
			if ra > 0 {
				until = now.Add(ra)
			}
			b.DeadUntil = max(b.DeadUntil, until.UnixMilli())
			note(b)
			return true
		}
		_, dayEnd := rlDay(lim.Reset, now)
		until := dayEnd
		body := ""
		if ae != nil {
			body = strings.ToLower(ae.Body)
		}
		switch {
		case strings.Contains(body, "per hour"):
			until = now.Truncate(time.Hour).Add(time.Hour)
		case strings.Contains(body, "month") || strings.Contains(body, "quota_for_entity"):
			_, until = rlMonth(now)
		case ra > time.Minute:
			until = now.Add(ra)
		case b.HdrReset > ms:
			until = time.UnixMilli(b.HdrReset)
		}
		b.DeadUntil = max(b.DeadUntil, until.UnixMilli())
		if s := shadow(false); s != nil {
			s.DeadUntil = max(s.DeadUntil, b.DeadUntil)
		}
		note(b)
		return true
	case "toobig":
		b := rl.bucketLocked(ref)
		n := 0
		if ae != nil {
			if m := pvLimitNRE.FindStringSubmatch(ae.Body); m != nil {
				n, _ = strconv.Atoi(m[1])
			}
		}
		if n == 0 {
			n = tokens
		}
		if n > 0 {
			b.MaxReq = n * 9 / 10
		}
		note(b)
		return true
	case "credit":
		// out of credit is an account condition: the key's shadow when pooling,
		// the provider bucket otherwise
		b := rl.bucketLocked(prov)
		if s := shadow(true); s != nil {
			b = s
		}
		b.DeadUntil = max(b.DeadUntil, now.Add(6*time.Hour).UnixMilli())
		note(b)
		return true
	case "auth":
		if ae != nil && ae.Status == 403 && !pvBadKeyRE.MatchString(ae.Body) {
			// 403 without a bad-key body is usually this model only (not in
			// the plan, region, moderation): park the ref, keep the provider
			b := rl.bucketLocked(ref)
			b.DeadUntil = max(b.DeadUntil, now.Add(6*time.Hour).UnixMilli())
			note(b)
			return true
		}
		b := rl.bucketLocked(prov)
		if fp == "" || fp == "nokey" {
			// no key to blame (keyless gateway, local server): retry later instead
			b.DeadUntil = max(b.DeadUntil, now.Add(10*time.Minute).UnixMilli())
		} else {
			b.AuthBad = fp // last refused fp, kept for older binaries reading the file
			if b.AuthBadS == nil {
				b.AuthBadS = map[string]int64{}
			}
			b.AuthBadS[fp] = ms
			if s := shadow(true); s != nil {
				s.DeadUntil = max(s.DeadUntil, now.Add(100*365*24*time.Hour).UnixMilli())
			}
		}
		note(b)
		return true
	case "freetier":
		b := rl.bucketLocked(ref)
		b.DeadUntil = now.Add(30 * 24 * time.Hour).UnixMilli()
		note(b)
		return true
	case "gone":
		// persistent tombstone (no 24h expiry: laguna 404'd on 31/10, 02/11,
		// 04/11 and 12/11 under the old one); rlGoneRevived lifts it
		b := rl.bucketLocked(ref)
		if c != nil && c.Providers[prov] != nil && c.Providers[prov].Local {
			// a local server's 404 is "not pulled yet" (ollama pull fixes it in
			// a minute): never a tombstone, just out of the chain for a while
			b.DeadUntil = max(b.DeadUntil, now.Add(10*time.Minute).UnixMilli())
			note(b)
			return true
		}
		b.Gone = ms
		note(b)
		pvCacheInvalidate(prov)
		return true
	case "toomanytools":
		// telemetry only: the request shape (MCP tool count) is the problem,
		// not the model's health
		note(rl.bucketLocked(ref))
		return false
	case "notools":
		if c != nil {
			c.MarkTextTools(ref)
		}
		note(rl.bucketLocked(ref))
		return false
	case "toolcall":
		// the model's generation was rejected (invented tool, bad args):
		// a model fault, not the ref's health — no strikes, no cooldown
		note(rl.bucketLocked(ref))
		return false
	case "overflow":
		if tokens > 0 {
			pvLearnCtx(ref, tokens*9/10)
		}
		note(rl.bucketLocked(ref))
		return false
	case "transient":
		b := rl.bucketLocked(ref)
		b.Trans++
		if b.Trans >= 3 {
			b.CoolUntil = max(b.CoolUntil, now.Add(60*time.Second).UnixMilli())
			b.Trans = 0
		}
		note(b)
		return false
	}
	return false
}

// rlObserve learns remaining quota from response headers (successful answers).
// Groq: x-ratelimit-*-requests = DAY, *-tokens = MINUTE (Go durations).
// SambaNova: *-requests = MINUTE, *-requests-day = DAY.
func (c *Config) rlObserve(ref, base string, h http.Header, fp string) {
	if c == nil || h == nil {
		return
	}
	get := func(k string) (int, bool) {
		v := strings.TrimSpace(h.Get(k))
		if v == "" {
			return 0, false
		}
		n, err := strconv.Atoi(v)
		return n, err == nil
	}
	now := rlNow()
	ms := now.UnixMilli()
	_, key := c.pvEffLimits(ref)
	prov, _ := SplitModel(ref)
	if fp == "" {
		fp = pvKeyPrint(c.APIKey(prov))
	}
	key = c.rlPoolKey(prov, key, fp)
	ref = c.rlPoolKey(prov, ref, fp)
	rl := omniRL
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.loadLocked()
	changed := false
	setDay := func(k string, rem int, resetHdr string) {
		b := rl.bucketLocked(k)
		b.HdrReq, b.HdrAt = rem, ms
		if d := pvParseReset(h.Get(resetHdr), now); d > 0 {
			b.HdrReset = now.Add(d).UnixMilli()
		}
		changed = true
	}
	cool := func(k, resetHdr string) {
		if d := pvParseReset(h.Get(resetHdr), now); d > 0 && d <= 10*time.Minute {
			b := rl.bucketLocked(k)
			b.CoolUntil = max(b.CoolUntil, now.Add(d).UnixMilli())
			changed = true
		}
	}
	switch {
	case strings.Contains(base, "groq.com"):
		if n, ok := get("x-ratelimit-remaining-requests"); ok {
			setDay(ref, n, "x-ratelimit-reset-requests")
		}
		if n, ok := get("x-ratelimit-remaining-tokens"); ok && n < 500 {
			cool(ref, "x-ratelimit-reset-tokens")
		}
	case strings.Contains(base, "sambanova"):
		if n, ok := get("x-ratelimit-remaining-requests-day"); ok {
			setDay(ref, n, "x-ratelimit-reset-requests-day")
		}
		if n, ok := get("x-ratelimit-remaining-requests"); ok && n == 0 {
			cool(ref, "x-ratelimit-reset-requests")
		}
	default:
		if n, ok := get("x-ratelimit-remaining-requests"); ok && n == 0 {
			cool(key, "x-ratelimit-reset-requests")
		}
	}
	if changed {
		rl.dirty = true
		rl.saveLocked(false)
	}
}

// rlSetDaily records an exact daily count (OpenRouter GET /api/v1/key). fp is
// the fingerprint of the key the quota was read from ("" = the current pick).
func (c *Config) rlSetDaily(ref string, used, limit int, fp string) {
	lim, key := c.pvEffLimits(ref)
	prov, _ := SplitModel(ref)
	if fp == "" {
		fp = pvKeyPrint(c.APIKey(prov))
	}
	key = c.rlPoolKey(prov, key, fp)
	now := rlNow()
	rl := omniRL
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.loadLocked()
	b := rl.bucketLocked(key)
	day, _ := rlDay(lim.Reset, now)
	if used >= 0 {
		b.Day, b.Req = day, used
	}
	if limit > 0 {
		b.LRPD = limit
	}
	rl.dirty = true
	rl.saveLocked(true)
}

// rlBudget describes the remaining quota of ref in PT-BR ("37/50 hoje", "?" when unknown).
func (c *Config) rlBudget(ref string) string {
	lim, key := c.pvEffLimits(ref)
	now := rlNow()
	rl := omniRL
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.loadLocked()
	b := rl.B[key]
	if b == nil {
		b = &rlBucket{}
	}
	var parts []string
	day, _ := rlDay(lim.Reset, now)
	req := b.Req
	if b.Day != day {
		req = 0
	}
	rpd := lim.RPD
	if b.LRPD > 0 {
		rpd = b.LRPD
	}
	switch {
	case rpd > 0:
		parts = append(parts, fmt.Sprintf("%d/%d hoje", req, rpd))
	case b.HdrAt > 0 && b.HdrReq >= 0:
		parts = append(parts, fmt.Sprintf("restam %d hoje", b.HdrReq))
	}
	if lim.Monthly > 0 {
		mon, _ := rlMonth(now)
		m := b.MReq
		if b.Month != mon {
			m = 0
		}
		parts = append(parts, fmt.Sprintf("%d/%d no mês", m, lim.Monthly))
	}
	if lim.RPH > 0 {
		n := 0
		for _, t := range b.Recent {
			if t >= now.UnixMilli()-3600_000 {
				n++
			}
		}
		parts = append(parts, fmt.Sprintf("%d/%d na hora", n, lim.RPH))
	}
	if lim.RPM > 0 && len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%d/min", lim.RPM))
	}
	if len(parts) == 0 {
		return "?"
	}
	return strings.Join(parts, ", ")
}

// rlLastOK is the ref of the last successful request (router affinity).
func rlLastOK() string {
	omniRL.mu.Lock()
	defer omniRL.mu.Unlock()
	return omniRL.lastOK
}

// rlInfo returns a status snapshot for ref: strikes, last error, and learned max request.
func rlInfo(ref string) (strikes int, lastErr string, maxReq int) {
	prov, _ := SplitModel(ref)
	rl := omniRL
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.loadLocked()
	var at int64
	for _, k := range []string{ref, prov, prov + "/*free"} {
		if b := rl.B[k]; b != nil {
			strikes = max(strikes, b.Strikes)
			if b.LastAt > at {
				at, lastErr = b.LastAt, b.LastErr
			}
			if k == ref {
				maxReq = b.MaxReq
			}
		}
	}
	return
}

// ResetRateLimits forgets the state of one provider ("" = everything): `jazz omni reset`.
func ResetRateLimits(prov string) {
	rl := omniRL
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.loadLocked()
	for k := range rl.B {
		if p, _ := SplitModel(k); prov == "" || p == prov || strings.HasPrefix(k, prov+"@") {
			delete(rl.B, k)
		}
	}
	rl.dirty = true
	// write without merging the old file back in
	if err := os.MkdirAll(filepath.Dir(rl.path), 0o700); err == nil {
		data, _ := json.Marshal(map[string]any{"buckets": rl.B})
		if writeFileAtomic(rl.path, data, 0o600) == nil {
			rl.dirty, rl.lastSave = false, time.Now()
		}
	}
}

// ---- contract functions ----

// RetryDelay is how long to wait before retry number attempt (0-based) after err.
// It honours APIError.RetryAfter (capped at 60s); otherwise 2s, 6s, 15s with ±20% jitter.
// It returns 0 when retrying the same model is pointless (move to the next candidate):
// daily quota, request too big for the model (413), auth, gone, no tool support,
// a rejected tool call (the caller's reminder retry handles that case first).
func RetryDelay(err error, attempt int) time.Duration {
	switch pvClassify(err) {
	case "daily", "toobig", "auth", "freetier", "gone", "notools", "toomanytools", "credit", "overflow", "toolcall":
		return 0
	}
	var ae *APIError
	if errors.As(err, &ae) && ae.RetryAfter > 0 {
		return min(ae.RetryAfter, 60*time.Second)
	}
	steps := []time.Duration{2 * time.Second, 6 * time.Second, 15 * time.Second}
	if pvClassify(err) == "rate" {
		// per-minute limits (NVIDIA 40/min) reopen within 60 s; 2+6+15 s gave up at ~23 s
		// with the model about to answer again (eval v2 on the d610, 13/11). The caller's
		// wait budget (30 s interactive, 120 s headless) and the move-to-next-candidate rule
		// for waits over 20 s still apply.
		steps = append(steps, 30*time.Second, 40*time.Second)
	}
	if attempt < 0 || attempt >= len(steps) {
		return 0
	}
	d := steps[attempt]
	j := 0.8 + 0.4*rand.Float64()
	return time.Duration(float64(d) * j)
}

// MarkExhausted records that ref hit its quota until reset (dataDir()/ratelimit.json).
func MarkExhausted(ref string, reset time.Time) {
	rl := omniRL
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.loadLocked()
	b := rl.bucketLocked(ref)
	b.DeadUntil = max(b.DeadUntil, reset.UnixMilli())
	rl.dirty = true
	rl.saveLocked(true)
}

// ExhaustedUntil reports whether ref is still marked exhausted, and until when.
// It looks at the model's own bucket and its provider-wide buckets.
func ExhaustedUntil(ref string) (time.Time, bool) {
	prov, model := SplitModel(ref)
	keys := []string{ref, prov}
	if pvFreeID(model) {
		keys = append(keys, prov+"/*free")
	}
	rl := omniRL
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.loadLocked()
	now := rlNow().UnixMilli()
	if b := rl.B[ref]; b != nil && b.Gone > 0 && !rlGoneRevived(ref, b.Gone) {
		return rlNow().Add(100 * 365 * 24 * time.Hour), true
	}
	var until int64
	for _, k := range keys {
		if b := rl.B[k]; b != nil && b.DeadUntil > now {
			until = max(until, b.DeadUntil)
		}
	}
	if until == 0 {
		return time.Time{}, false
	}
	return time.UnixMilli(until), true
}

// ---- file locking ----

// pvFlock takes an exclusive advisory lock on path, polling up to timeout.
func pvFlock(path string, timeout time.Duration) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for {
		busy, err := pfTryLock(f)
		if err == nil && !busy {
			return func() {
				pfUnlock(f)
				f.Close()
			}, nil
		}
		if err != nil {
			f.Close()
			return nil, err
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("trava %s ocupada", filepath.Base(path))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// rlLimiter is the Limiter backed by the limiter state (PROV-3): Allow says no
// to a ref the state calls dead (quota, refused key, retired model, provider
// cooled by a quota 429). Report is a no-op: the providers record every
// outcome through rlEnd.
type rlLimiter struct{ c *Config }

func (l rlLimiter) Allow(ref string) (bool, time.Duration) {
	if l.c == nil || isOmniRef(ref) {
		return true, 0
	}
	if st := l.c.pvState(ref, 0); st.Dead {
		return false, st.Wait
	}
	return true, 0
}

func (rlLimiter) Report(string, error) {}
