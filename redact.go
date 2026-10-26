package main

// redact.go: outbound secret redaction at the provider boundary.
//
// Reading secret *paths* (.env, keys, auth.json) is already blocked, but
// secrets embedded in ordinary files and command output (a config.yaml with a
// password, a README with a token, `env`, a git diff, a log with a Bearer
// header) would otherwise reach the model provider verbatim, and free tiers may
// train on prompts. Every request is rewritten right before it leaves the
// machine (rdOutbound, called at the top of each provider's Chat): high
// confidence secrets become stable placeholders such as <secreto:aws-key#1>.
// The reverse map lives only in memory. When the model echoes a placeholder
// into a write/edit/bash call, rdRestoreCalls puts the real value back before
// the call is gated and run locally, so the model can use a token without
// ever seeing it. Output shown to the user is local and never redacted.
//
// Detection is trigger-driven: each rule names a few literals (a prefix such
// as "ghp_" or a key word such as "password"); the text is lowered once and
// a rule's regexp only runs on the line around each trigger hit, so the cost
// is one pass plus a little work per hit. Modes: on (default; high-confidence
// rules), strict (also medium-confidence values and generic high-entropy
// runs), off. Config "redact", flag --redact, env JAZZ_REDACT.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// ---------- settings ----------

type rdMode int

const (
	rdOff rdMode = iota
	rdOn
	rdStrict
)

func (m rdMode) String() string {
	switch m {
	case rdOff:
		return "off"
	case rdStrict:
		return "strict"
	}
	return "on"
}

func rdParse(s string) (rdMode, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "on", "true", "yes", "1", "auto":
		return rdOn, true
	case "off", "false", "no", "0":
		return rdOff, true
	case "strict", "estrito":
		return rdStrict, true
	}
	return rdOn, false
}

// rdLoadMode reads config "redact" and JAZZ_REDACT (env wins).
func rdLoadMode(cfgPath string, getenv func(string) string) rdMode {
	m := rdOn
	var c struct {
		Redact any `json:"redact"`
	}
	if b, err := os.ReadFile(cfgPath); err == nil && json.Unmarshal(b, &c) == nil && c.Redact != nil {
		v := fmt.Sprint(c.Redact)
		if b, ok := c.Redact.(bool); ok {
			v = map[bool]string{true: "on", false: "off"}[b]
		}
		if x, ok := rdParse(v); ok {
			m = x
		}
	}
	if e := getenv("JAZZ_REDACT"); e != "" {
		if x, ok := rdParse(e); ok {
			m = x
		}
	}
	return m
}

// rdFlags is --redact.
type rdFlags struct {
	fs  *flag.FlagSet
	val string
}

func init() {
	opValuedFlags["--redact"] = true // reorderFlags (main.go) learns it takes a value
	opValuedFlags["-redact"] = true
}

func rdRegisterFlags(fs *flag.FlagSet) *rdFlags {
	f := &rdFlags{fs: fs}
	fs.StringVar(&f.val, "redact", "", "")
	return f
}

func (f *rdFlags) apply() error {
	set := false
	f.fs.Visit(func(fl *flag.Flag) {
		if fl.Name == "redact" {
			set = true
		}
	})
	if !set {
		return nil
	}
	m, ok := rdParse(f.val)
	if !ok {
		return errors.New("--redact deve ser on, off ou strict")
	}
	rdSess.SetMode(m)
	return nil
}

// ---------- rules ----------

type rdConf int

const (
	rdNone rdConf = iota
	rdMedium
	rdHigh
)

type rdRule struct {
	kind  string
	trig  []string // lowercase literals searched in the lowered text
	trigC []string // case-sensitive literals searched in the original text
	re    *regexp.Regexp
	grp   int                        // submatch holding the secret (0 = whole match)
	conf  rdConf                     // confidence when check is nil
	check func(key, v string) rdConf // optional: confidence of a candidate
	keyG  int                        // submatch holding the key name (for check)
	pem   bool                       // window runs to the END marker
	low   bool                       // re matches the lowered text (key words in lowercase, no (?i))
	kv    bool                       // window starts at the key name around the trigger (cheap on long lines)
	prio  int                        // overlap winner: higher = more specific
}

func rdHighIf(min int) func(string, string) rdConf {
	return func(_, v string) rdConf {
		if len(v) < min {
			return rdNone
		}
		return rdHigh
	}
}

var rdRules = []rdRule{
	{kind: "chave-privada", trig: []string{"private key"}, pem: true, prio: 9, conf: rdHigh,
		re: regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY[A-Z ]*-----(?s:.*?)(?:-----END [A-Z0-9 ]*PRIVATE KEY[A-Z ]*-----|\z)`)},
	{kind: "gcp-sa-key", trig: []string{"private_key_id"}, prio: 8, conf: rdHigh, grp: 1,
		re: regexp.MustCompile(`"private_key_id"\s*:\s*"([0-9a-f]{40})"`)},
	{kind: "aws-key", trigC: []string{"AKIA", "ASIA", "ABIA", "ACCA"}, prio: 8, conf: rdHigh,
		re: regexp.MustCompile(`\b(?:AKIA|ASIA|ABIA|ACCA)[A-Z0-9]{16}\b`)},
	{kind: "aws-secret", trig: []string{"aws", "secret_access_key", "secretaccesskey"}, prio: 7, grp: 1, check: rdRandom(40), low: true, kv: true,
		re: regexp.MustCompile(`(?:aws|secret_?access)[A-Za-z0-9_.-]{0,30}?(?:secret|key)[A-Za-z0-9_.-]{0,20}["']?\s*(?:=|:|=>)\s*["']?([A-Za-z0-9/+]{40})(?:[^A-Za-z0-9/+=]|\z)`)},
	{kind: "anthropic-key", trig: []string{"sk-ant-"}, prio: 8, check: rdHighIf(20),
		re: regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`)},
	{kind: "openrouter-key", trig: []string{"sk-or-"}, prio: 8, conf: rdHigh,
		re: regexp.MustCompile(`\bsk-or-v1-[0-9a-f]{48,}\b`)},
	{kind: "openai-key", trig: []string{"sk-"}, prio: 6, check: rdOpenAI,
		re: regexp.MustCompile(`\bsk-(?:proj-|svcacct-|admin-|None-)?[A-Za-z0-9_-]{20,}`)},
	{kind: "github-token", trig: []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_"}, prio: 8, conf: rdHigh,
		re: regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36,255}|github_pat_[A-Za-z0-9_]{22,255})\b`)},
	{kind: "gitlab-token", trig: []string{"glpat-", "gldt-", "glptt-", "glrt-", "glsoat-"}, prio: 8, conf: rdHigh,
		re: regexp.MustCompile(`\bgl(?:pat|dt|ptt|rt|soat)-[A-Za-z0-9_-]{20,}\b`)},
	{kind: "slack-token", trig: []string{"xox"}, prio: 8, conf: rdHigh,
		re: regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}\b`)},
	{kind: "slack-webhook", trig: []string{"hooks.slack.com"}, prio: 8, conf: rdHigh,
		re: regexp.MustCompile(`https://hooks\.slack\.com/(?:services|workflows|triggers)/[A-Za-z0-9/_-]{20,}`)},
	{kind: "discord-webhook", trig: []string{"/api/webhooks/"}, prio: 8, conf: rdHigh,
		re: regexp.MustCompile(`https://(?:\w+\.)?discord(?:app)?\.com/api/webhooks/[0-9]+/[A-Za-z0-9_-]{20,}`)},
	{kind: "stripe-key", trig: []string{"_live_", "_test_", "whsec_"}, prio: 8, conf: rdHigh,
		re: regexp.MustCompile(`\b(?:(?:sk|rk)_(?:live|test)_[A-Za-z0-9]{16,}|whsec_[A-Za-z0-9]{20,})\b`)},
	{kind: "twilio-key", trigC: []string{"SK"}, prio: 7, check: rdTwilio,
		re: regexp.MustCompile(`\bSK[0-9a-fA-F]{32}\b`)},
	{kind: "sendgrid-key", trigC: []string{"SG."}, prio: 8, conf: rdHigh,
		re: regexp.MustCompile(`\bSG\.[A-Za-z0-9_-]{22}\.[A-Za-z0-9_-]{43}\b`)},
	{kind: "google-api-key", trigC: []string{"AIza"}, prio: 8, conf: rdHigh,
		re: regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
	{kind: "google-oauth", trig: []string{"ya29."}, prio: 8, conf: rdHigh,
		re: regexp.MustCompile(`\bya29\.[0-9A-Za-z_-]{20,}`)},
	{kind: "groq-key", trig: []string{"gsk_"}, prio: 8, conf: rdHigh,
		re: regexp.MustCompile(`\bgsk_[A-Za-z0-9]{40,}\b`)},
	{kind: "hf-token", trig: []string{"hf_"}, prio: 7, check: rdRandom(30),
		re: regexp.MustCompile(`\bhf_[A-Za-z]{30,}\b`)},
	{kind: "npm-token", trig: []string{"npm_"}, prio: 8, check: rdRandom(36),
		re: regexp.MustCompile(`\bnpm_[A-Za-z0-9]{36}\b`)},
	{kind: "xai-key", trig: []string{"xai-"}, prio: 8, check: rdRandom(40),
		re: regexp.MustCompile(`\bxai-[A-Za-z0-9]{40,}\b`)},
	{kind: "azure-key", trig: []string{"accountkey=", "sharedaccesskey="}, prio: 8, grp: 1, check: rdRandom(40), low: true,
		re: regexp.MustCompile(`(?:accountkey|sharedaccesskey)=([A-Za-z0-9+/]{40,}={0,2})`)},
	{kind: "azure-sas", trig: []string{"sig="}, prio: 7, grp: 1, check: rdRandom(30),
		re: regexp.MustCompile(`[?&]sig=([A-Za-z0-9%+/]{30,}(?:={0,2}|(?:%3D){0,2}))`)},
	{kind: "azure-key", trig: []string{"q~"}, prio: 7, check: rdRandom(30),
		re: regexp.MustCompile(`\b[A-Za-z0-9_~.]{3}\dQ~[A-Za-z0-9_~.-]{31,34}\b`)},
	{kind: "jwt", trig: []string{"eyj"}, prio: 7, conf: rdHigh,
		re: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`)},
	{kind: "basic-auth", trig: []string{"basic "}, prio: 6, grp: 1, check: rdBasic, low: true,
		re: regexp.MustCompile(`\bauthorization["']?\s*[:=]\s*["']?basic\s+([A-Za-z0-9+/]{8,}={0,2})`)},
	{kind: "bearer", trig: []string{"bearer "}, prio: 5, grp: 1, check: rdBearer, low: true,
		re: regexp.MustCompile(`\bbearer\s+([A-Za-z0-9\-._~+/]{8,}=*)`)},
	{kind: "url-credencial", trig: []string{"://"}, prio: 6, grp: 3, keyG: 1, check: rdURLPass, low: true,
		re: regexp.MustCompile(`\b([a-z][a-z0-9+.-]{1,20})://([^/\s:@'"<>]{1,128}):([^@\s/'"<>]{1,256})@`)},
	{kind: "db-conexao", trig: []string{"password=", "pwd="}, prio: 6, grp: 1, check: rdKV, low: true,
		re: regexp.MustCompile(`(?:^|[;"'\s])(?:password|pwd)\s*=\s*([^;'"\s]{4,})\s*;`)},
	{kind: "env-secret", trig: []string{"key", "secret", "token", "pass", "pwd", "auth", "credential"}, prio: 4, grp: 2, keyG: 1, check: rdEnv,
		re: regexp.MustCompile(`(?m)^[ \t]*(?:export[ \t]+)?([A-Z][A-Z0-9_]*(?:KEY|SECRET|TOKEN|PASSWORD|PASSWD|PASS|PWD|AUTH|CREDENTIALS?))[ \t]*=[ \t]*["']?([^\s"'#]+)`)},
	{kind: "senha", trig: []string{"pass", "pwd", "senha"}, prio: 3, grp: 2, keyG: 1, check: rdPass, low: true, kv: true,
		re: regexp.MustCompile(`([a-z0-9_.-]*(?:password|passwd|passphrase|pwd|senha)[A-Za-z0-9_.-]*)["']?\s*(?:=|:|=>|:=)\s*["']?([^\s"'` + "`" + `,;&<>(){}\[\]]+)`)},
	{kind: "segredo", trig: []string{"secret", "token", "api_key", "apikey", "api-key", "access_key", "access-key", "accesskey", "private_key", "privatekey", "credential"}, prio: 2, grp: 2, keyG: 1, check: rdKV, low: true, kv: true,
		re: regexp.MustCompile(`([a-z0-9_.-]*(?:secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credentials?)[A-Za-z0-9_.-]*)["']?\s*(?:=|:|=>|:=)\s*["']?([^\s"'` + "`" + `,;&<>(){}\[\]]+)`)},
}

// ---------- value checks ----------

// rdFakeWords mark obviously fake fixtures and documentation values. The
// red-team canaries contain CANARY on purpose: they are never redacted, so
// the suite measures the guards, not this filter.
var rdFakeWords = []string{"canary", "example", "changeme", "change_me", "change-me", "dummy", "placeholder", "your_", "your-", "yourtoken",
	"xxxx", "****", "redacted", "<secreto", "fake", "sample", "replace", "insert_", "todo", "notreal", "not-a-real", "not_a_real", "foobar",
	"123456789", "abcdefgh", "0000000000"}

func rdFake(v string) bool {
	if v == "" {
		return true
	}
	lv := strings.ToLower(v)
	for _, w := range rdFakeWords {
		if strings.Contains(lv, w) {
			return true
		}
	}
	same := true
	for i := 1; i < len(v); i++ {
		if v[i] != v[0] {
			same = false
			break
		}
	}
	return same
}

var (
	rdUUID   = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	rdDotted = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*(?:\.[A-Za-z_$][A-Za-z0-9_$]*|\[[^\]]*\])+$`)
	rdWord   = regexp.MustCompile(`^[a-z][a-z_.-]*$`)
	rdIdent  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	rdNum    = regexp.MustCompile(`^[0-9][0-9_.,]*$`)
)

// rdVarRef: the value is a reference, not a literal (shell/template/env/code).
func rdVarRef(v string) bool {
	if v == "" {
		return true
	}
	switch v[0] {
	case '$', '%', '{', '<', '@', '!', '[', '(', '/', '~', '#', '\\', '&', '*':
		return true
	}
	if strings.HasPrefix(v, "./") || strings.Contains(v, "://") || strings.Contains(v, "${") || strings.Contains(v, "{{") {
		return true
	}
	lv := strings.ToLower(v)
	for _, p := range []string{"process.env", "os.environ", "os.getenv", "getenv", "env[", "env.", "secrets.", "vars.", "config.", "cfg.", "self.", "this.", "settings."} {
		if strings.HasPrefix(lv, p) {
			return true
		}
	}
	return rdDotted.MatchString(v)
}

func rdEntropy(s string) float64 {
	if s == "" {
		return 0
	}
	var f [256]int
	for i := 0; i < len(s); i++ {
		f[s[i]]++
	}
	n := float64(len(s))
	h := 0.0
	for _, c := range f {
		if c > 0 {
			p := float64(c) / n
			h -= p * math.Log2(p)
		}
	}
	return h
}

func rdClasses(s string) int {
	var lo, up, di, sy bool
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
			lo = true
		case c >= 'A' && c <= 'Z':
			up = true
		case c >= '0' && c <= '9':
			di = true
		default:
			sy = true
		}
	}
	n := 0
	for _, b := range []bool{lo, up, di, sy} {
		if b {
			n++
		}
	}
	return n
}

// rdRandom: a token-shaped value of at least min chars that looks random.
func rdRandom(min int) func(string, string) rdConf {
	return func(_, v string) rdConf {
		if len(v) < min || rdFake(v) {
			return rdNone
		}
		if rdClasses(v) >= 2 && rdEntropy(v) >= 3.0 {
			return rdHigh
		}
		return rdMedium
	}
}

func rdOpenAI(_, v string) rdConf {
	body := strings.TrimPrefix(v, "sk-")
	for _, p := range []string{"proj-", "svcacct-", "admin-", "None-"} {
		if strings.HasPrefix(body, p) {
			return rdHigh
		}
	}
	// sk-learn-… and other prose: a real key mixes cases and digits
	if rdClasses(strings.ReplaceAll(strings.ReplaceAll(body, "-", ""), "_", "")) >= 3 && rdEntropy(body) >= 3.5 {
		return rdHigh
	}
	return rdNone
}

func rdTwilio(_, v string) rdConf {
	if rdEntropy(v) >= 3.0 {
		return rdHigh
	}
	return rdNone
}

func rdBasic(_, v string) rdConf {
	if rdVarRef(v) || rdFake(v) {
		return rdNone
	}
	return rdHigh
}

func rdBearer(_, v string) rdConf {
	if rdVarRef(v) || rdFake(v) || rdWord.MatchString(v) || rdIdent.MatchString(v) && rdClasses(v) < 3 {
		return rdNone
	}
	if len(v) >= 16 && (rdClasses(v) >= 2 && rdEntropy(v) >= 3.0) {
		return rdHigh
	}
	return rdMedium
}

var rdDBSchemes = map[string]bool{"postgres": true, "postgresql": true, "mysql": true, "mariadb": true, "mongodb": true, "mongodb+srv": true,
	"redis": true, "rediss": true, "amqp": true, "amqps": true, "mssql": true, "sqlserver": true, "jdbc:postgresql": true, "clickhouse": true, "cockroachdb": true}

var rdURLFake = map[string]bool{"pass": true, "password": true, "passwd": true, "senha": true, "secret": true, "pwd": true, "x-oauth-basic": true, "x": true}

func rdURLPass(scheme, v string) rdConf {
	if len(v) < 3 || rdURLFake[strings.ToLower(v)] || rdFake(v) || rdVarRef(v) {
		return rdNone
	}
	return rdHigh
}

// rdKV judges the literal value of a password/secret/token assignment.
func rdKV(key, v string) rdConf {
	lk := strings.ToLower(key)
	for _, w := range []string{"public", "publishable", "_url", "_uri", "_path", "_file", "_dir", "_type", "_name", "_len", "_length", "_count", "_size", "_ttl",
		"_expir", "_timeout", "_field", "_header", "_endpoint", "_env", "_var", "_id", "_hash", "_policy", "_prompt", "_label", "_hint", "max_token", "num_token", "tokenizer", "tokens"} {
		if strings.Contains(lk, w) {
			return rdNone
		}
	}
	v = strings.TrimRight(v, ".")
	if len(v) < 6 || rdVarRef(v) || rdFake(v) || rdUUID.MatchString(v) {
		return rdNone
	}
	lv := strings.ToLower(v)
	switch lv {
	case "true", "false", "null", "none", "nil", "undefined", "string", "required", "optional", "secret", "password", "token", "hidden", "masked", "bearer", "basic":
		return rdNone
	}
	if rdNum.MatchString(v) && len(v) < 16 {
		return rdNone
	}
	if rdWord.MatchString(v) || rdIdent.MatchString(v) && rdClasses(v) < 3 {
		return rdMedium // a plain word or identifier: likely code (strict only)
	}
	if len(v) >= 8 && (rdClasses(v) >= 2 || rdEntropy(v) >= 3.0) {
		return rdHigh
	}
	return rdMedium
}

// rdPass judges a password assignment: unlike tokens, short values with a
// digit or symbol count; a plain word or a camelCase name is likely code.
func rdPass(key, v string) rdConf {
	c := rdKV(key, v)
	if c != rdMedium {
		return c
	}
	if rdWord.MatchString(v) || rdIdent.MatchString(v) && !strings.ContainsAny(v, "0123456789") {
		return rdMedium
	}
	if len(v) >= 6 && rdClasses(v) >= 2 {
		return rdHigh
	}
	return rdMedium
}

// rdEnv: dotenv KEY=VALUE with a secret-like KEY; high when the value is random.
func rdEnv(key, v string) rdConf {
	if strings.Contains(key, "PUBLIC") || strings.Contains(key, "PUBLISHABLE") || strings.HasSuffix(key, "_ID") {
		return rdNone
	}
	if len(v) < 6 || rdVarRef(v) || rdFake(v) || rdUUID.MatchString(v) || strings.HasPrefix(v, "pk_") {
		return rdNone
	}
	if len(v) >= 16 && (rdEntropy(v) >= 3.5 || rdClasses(v) >= 3) {
		return rdHigh
	}
	if strings.Contains(key, "PASS") || strings.Contains(key, "PWD") {
		return rdPass(key, v)
	}
	return rdMedium
}

// ---------- scanning ----------

type rdHit struct {
	start, end int
	kind       string
	prio       int
	conf       rdConf
}

const (
	rdWinBefore = 512
	rdWinAfter  = 2048
	rdPEMMax    = 16 << 10
)

// rdLower lowers ASCII letters, keeping byte offsets aligned with s.
func rdLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

// rdScan finds secrets in s. minConf selects the mode (rdHigh = on, rdMedium = strict).
func rdScan(s string, minConf rdConf) []rdHit {
	if len(s) < 8 {
		return nil
	}
	low := rdLower(s)
	var hits []rdHit
	pos := rdTriggerHits(s, low)
	for ri := range rdRules {
		r := &rdRules[ri]
		// every trigger hit of this rule, in order: each window is searched once
		covered := -1
		for _, p := range pos[ri] {
			if p < covered {
				continue
			}
			ws, we := rdWindow(s, p, r.pem)
			if r.kv {
				ws, we = rdKVWindow(s, p, ws, we)
			}
			if ws < covered {
				ws = covered
			}
			covered = we
			hits = rdApply(r, s, low, ws, we, minConf, hits)
		}
	}
	if minConf <= rdMedium {
		hits = append(hits, rdGeneric(s)...)
	}
	return rdResolve(rdDropPlaceholders(s, hits))
}

// Trigger lookup: one pass over the text, a bigram bitset rejects almost
// every position before any string comparison.
type rdTrig struct {
	lit  string
	rule int
}

var (
	rdBigLow, rdBigCS [1 << 10]uint64 // bitsets over the first two bytes
	rdTrigLow         = map[uint16][]rdTrig{}
	rdTrigCS          = map[uint16][]rdTrig{}
)

func init() {
	for ri, r := range rdRules {
		for _, t := range r.trig {
			k := uint16(t[0])<<8 | uint16(t[1])
			rdBigLow[k>>6] |= 1 << (k & 63)
			rdTrigLow[k] = append(rdTrigLow[k], rdTrig{t, ri})
		}
		for _, t := range r.trigC {
			k := uint16(t[0])<<8 | uint16(t[1])
			rdBigCS[k>>6] |= 1 << (k & 63)
			rdTrigCS[k] = append(rdTrigCS[k], rdTrig{t, ri})
		}
	}
}

// rdTriggerHits returns, per rule, the sorted positions of its triggers.
func rdTriggerHits(s, low string) [][]int {
	pos := make([][]int, len(rdRules))
	for i := 0; i+1 < len(s); i++ {
		if k := uint16(low[i])<<8 | uint16(low[i+1]); rdBigLow[k>>6]&(1<<(k&63)) != 0 {
			for _, t := range rdTrigLow[k] {
				if strings.HasPrefix(low[i:], t.lit) {
					pos[t.rule] = append(pos[t.rule], i)
				}
			}
		}
		if k := uint16(s[i])<<8 | uint16(s[i+1]); rdBigCS[k>>6]&(1<<(k&63)) != 0 {
			for _, t := range rdTrigCS[k] {
				if strings.HasPrefix(s[i:], t.lit) {
					pos[t.rule] = append(pos[t.rule], i)
				}
			}
		}
	}
	return pos
}

// rdKVWindow narrows a line window to the key name around p and what follows it.
func rdKVWindow(s string, p, ws, we int) (int, int) {
	k := p
	for k > ws && p-k < 128 {
		c := s[k-1]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-') {
			break
		}
		k--
	}
	return k, min(we, p+400)
}

// rdWindow is the line around p (capped), or for a PEM block, up to its END marker.
func rdWindow(s string, p int, pem bool) (int, int) {
	ws := strings.LastIndexByte(s[:p], '\n') + 1
	if p-ws > rdWinBefore {
		ws = p - rdWinBefore
	}
	if pem {
		// the block ends at the first END marker (or the cap: a cut key is still hidden)
		lim := min(len(s), p+rdPEMMax)
		if e := strings.Index(s[p:lim], "-----END"); e >= 0 {
			if k := strings.Index(s[p+e+8:lim], "-----"); k >= 0 {
				return ws, p + e + 8 + k + 5
			}
		}
		return ws, lim
	}
	we := len(s)
	if j := strings.IndexByte(s[p:], '\n'); j >= 0 {
		we = p + j
	}
	if we-p > rdWinAfter {
		we = p + rdWinAfter
	}
	return ws, we
}

func rdApply(r *rdRule, s, low string, ws, we int, minConf rdConf, hits []rdHit) []rdHit {
	win := s[ws:we]
	src := win
	if r.low {
		src = low[ws:we]
	}
	for _, m := range r.re.FindAllStringSubmatchIndex(src, -1) {
		g := r.grp
		if 2*g+1 >= len(m) || m[2*g] < 0 {
			continue
		}
		a, b := m[2*g], m[2*g+1]
		v := win[a:b]
		conf := r.conf
		if r.check != nil {
			key := ""
			if r.keyG > 0 && m[2*r.keyG] >= 0 {
				key = win[m[2*r.keyG]:m[2*r.keyG+1]]
			}
			conf = r.check(key, v)
		}
		if conf < minConf || conf == rdNone {
			continue
		}
		if rdFake(v) {
			continue
		}
		kind := r.kind
		if kind == "url-credencial" && r.keyG > 0 && rdDBSchemes[strings.ToLower(win[m[2]:m[3]])] {
			kind = "db-conexao"
		}
		hits = append(hits, rdHit{start: ws + a, end: ws + b, kind: kind, prio: r.prio, conf: conf})
	}
	return hits
}

// rdGeneric (strict only): long random-looking runs with no key name nearby.
func rdGeneric(s string) []rdHit {
	var hits []rdHit
	isTok := func(c byte) bool {
		return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '+' || c == '/' || c == '_' || c == '-'
	}
	for i := 0; i < len(s); {
		if !isTok(s[i]) {
			i++
			continue
		}
		j := i
		for j < len(s) && isTok(s[j]) {
			j++
		}
		for j < len(s) && j-i < 515 && s[j] == '=' { // base64 padding
			j++
		}
		if t := s[i:j]; j-i >= 32 && j-i <= 512 && rdGenericOK(t) {
			hits = append(hits, rdHit{start: i, end: j, kind: "alta-entropia", prio: 1, conf: rdMedium})
		}
		i = j
	}
	return hits
}

func rdGenericOK(t string) bool {
	lt := strings.ToLower(t)
	if rdFake(t) || strings.HasPrefix(t, "-") || strings.HasPrefix(t, "/") || strings.HasPrefix(lt, "sha1-") || strings.HasPrefix(lt, "sha256-") ||
		strings.HasPrefix(lt, "sha384-") || strings.HasPrefix(lt, "sha512-") || strings.HasPrefix(lt, "pk_") {
		return false
	}
	if strings.Contains(t, "/") {
		for _, seg := range strings.Split(t, "/") {
			if len(seg) > 0 && len(seg) < 6 {
				return false // a path, not base64
			}
		}
	}
	isHex := true
	for i := 0; i < len(t); i++ {
		c := t[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			isHex = false
			break
		}
	}
	if isHex {
		return false // commit shas and digests
	}
	if strings.Count(t, "_")+strings.Count(t, "-") > len(t)/8 {
		return false // snake/kebab identifiers
	}
	return rdClasses(strings.Trim(t, "=")) >= 3 && rdEntropy(t) >= 4.2
}

// rdDropPlaceholders drops hits that touch a placeholder already in s.
func rdDropPlaceholders(s string, hits []rdHit) []rdHit {
	if len(hits) == 0 || !strings.Contains(s, "secreto:") {
		return hits
	}
	ph := rdPHRe.FindAllStringIndex(s, -1)
	out := hits[:0]
	for _, h := range hits {
		keep := true
		for _, p := range ph {
			if h.start < p[1] && p[0] < h.end {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, h)
		}
	}
	return out
}

// rdResolve sorts hits and drops overlaps (higher prio, then longer, wins).
func rdResolve(h []rdHit) []rdHit {
	if len(h) < 2 {
		return h
	}
	sort.Slice(h, func(i, j int) bool {
		if h[i].start != h[j].start {
			return h[i].start < h[j].start
		}
		return h[i].end > h[j].end
	})
	out := h[:0:0]
	for _, x := range h {
		if n := len(out); n > 0 && x.start < out[n-1].end {
			last := &out[n-1]
			if x.prio > last.prio || x.prio == last.prio && x.end-x.start > last.end-last.start {
				*last = x
			}
			continue
		}
		out = append(out, x)
	}
	return out
}

// ---------- session redactor ----------

// Redactor keeps the value <-> placeholder maps of one jazz process (the main
// agent and its subagents share it, so placeholders round-trip between them).
type Redactor struct {
	mu      sync.Mutex
	mode    rdMode
	loaded  bool
	byVal   map[string]string
	byPh    map[string]string
	seq     map[string]int
	kindOf  map[string]string // placeholder -> kind
	cache   map[string]string
	cacheN  int
	allow   map[string]bool // HMACs of values the user allowed for this project
	allowAt string          // project root the allowlist was loaded for
	turnNew int             // distinct secrets first redacted since the last rdTakeTurn
	total   int
	noticed bool
	pending bool // a first redaction happened and the notice is not shown yet
	hinted  bool // rdHint already went to the model
}

var rdSess = &Redactor{}

var rdPHRe = regexp.MustCompile(`(?:<|\\u003c)secreto:[a-z0-9-]+#[0-9]+(?:>|\\u003e)`)

func (r *Redactor) init() {
	if r.byVal == nil {
		r.byVal, r.byPh, r.seq, r.kindOf, r.cache = map[string]string{}, map[string]string{}, map[string]int{}, map[string]string{}, map[string]string{}
	}
	if !r.loaded {
		r.loaded = true
		r.mode = rdLoadMode(filepath.Join(configDir(), "config.json"), os.Getenv)
	}
}

// Mode is the effective mode.
func (r *Redactor) Mode() rdMode {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.init()
	return r.mode
}

// SetMode overrides config/env (flag, /redact) and returns the previous mode.
func (r *Redactor) SetMode(m rdMode) rdMode {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.init()
	prev := r.mode
	r.mode = m
	r.cache, r.cacheN = map[string]string{}, 0
	return prev
}

const rdCacheMax = 32 << 20 // bytes of cached inputs (the history is re-sent every step)

// Text redacts s (where names the part of the request, for events).
func (r *Redactor) Text(s, where string) string {
	if s == "" {
		return s
	}
	r.mu.Lock()
	r.init()
	mode := r.mode
	if mode == rdOff {
		r.mu.Unlock()
		return s
	}
	if out, ok := r.cache[s]; ok {
		stale := false
		for value := range r.byVal {
			if strings.Contains(s, value) {
				stale = true
				break
			}
		}
		if !stale {
			r.mu.Unlock()
			return out
		}
	}
	r.loadAllowLocked()
	known := make(map[string]string, len(r.byVal))
	for value, placeholder := range r.byVal {
		known[value] = placeholder
	}
	r.mu.Unlock()

	minConf := rdHigh
	if mode == rdStrict {
		minConf = rdMedium
	}
	hits := rdScan(s, minConf)
	hits = append(hits, rdKnownValues(s)...)
	hits = append(hits, rdSessionValues(s, known)...)
	hits = rdResolve(hits)

	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	last := 0
	for _, h := range hits {
		v := s[h.start:h.end]
		if r.allow[r.macLocked(v)] {
			continue
		}
		ph, ok := r.byVal[v]
		if !ok {
			r.seq[h.kind]++
			ph = fmt.Sprintf("<secreto:%s#%d>", h.kind, r.seq[h.kind])
			r.byVal[v], r.byPh[ph], r.kindOf[ph] = ph, v, h.kind
			// Earlier context-free copies may have been cached before the
			// value was recognized. Invalidate those entries immediately.
			for cachedInput, cachedOutput := range r.cache {
				if strings.Contains(cachedInput, v) || strings.Contains(cachedOutput, v) {
					r.cacheN -= len(cachedInput)
					delete(r.cache, cachedInput)
				}
			}
			r.turnNew++
			r.total++
			if !r.noticed {
				r.pending = true
			}
			rdEvent(h.kind, where)
		}
		if b.Len() == 0 {
			b.Grow(len(s))
		}
		b.WriteString(s[last:h.start])
		b.WriteString(ph)
		last = h.end
	}
	out := s
	if last > 0 {
		b.WriteString(s[last:])
		out = b.String()
	}
	if r.cacheN+len(s) > rdCacheMax {
		r.cache, r.cacheN = map[string]string{}, 0
	}
	r.cache[s] = out
	r.cacheN += len(s)
	return out
}

// rdSessionValues keeps previously identified values redacted even when they
// reappear without the context or token shape that first identified them.
func rdSessionValues(s string, known map[string]string) []rdHit {
	var hits []rdHit
	for value := range known {
		if value == "" {
			continue
		}
		for off := 0; off < len(s); {
			i := strings.Index(s[off:], value)
			if i < 0 {
				break
			}
			start := off + i
			hits = append(hits, rdHit{start: start, end: start + len(value), kind: "credencial-local", prio: 11, conf: rdHigh})
			off = start + len(value)
		}
	}
	return hits
}

// rdKnownValues: exact values of local credentials (provider keys in the
// environment, auth.json) collected by refreshSecrets (safety.go).
func rdKnownValues(s string) []rdHit {
	secretMu.Lock()
	vals := secretValues
	secretMu.Unlock()
	var hits []rdHit
	for _, v := range vals {
		if len(v) < 12 || rdFake(v) {
			continue
		}
		for off := 0; ; {
			i := strings.Index(s[off:], v)
			if i < 0 {
				break
			}
			hits = append(hits, rdHit{start: off + i, end: off + i + len(v), kind: "credencial-local", prio: 10, conf: rdHigh})
			off += i + len(v)
		}
	}
	return hits
}

// rdEvent records a first sighting: kind and where only, never the value.
var rdEvent = func(kind, where string) {
	f := map[string]any{"kind": kind, "where": where}
	obEmit("redact.hit", f)
	auditRec("redact", map[string]any{"kind": kind, "where": where})
	dbg("redact", f)
}

// Restore replaces known placeholders in a tool call's JSON arguments by the
// JSON-escaped real values. Unknown placeholders stay as they are.
func (r *Redactor) Restore(args string) (string, bool) {
	if !strings.Contains(args, "secreto:") {
		return args, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	changed := false
	out := rdPHRe.ReplaceAllStringFunc(args, func(ph string) string {
		key := strings.ReplaceAll(strings.ReplaceAll(ph, `\u003c`, "<"), `\u003e`, ">")
		v, ok := r.byPh[key]
		if !ok {
			return ph
		}
		changed = true
		j, _ := json.Marshal(v)
		return string(j[1 : len(j)-1])
	})
	return out, changed
}

// rdToolText is what the model, the session log and the screen get of a tool
// output: secrets become session placeholders (so the model can still use
// them in a command), then the older one-way redaction (redactSecrets,
// safety.go) hides whatever shape is left as [REDACTED].
func rdToolText(s, where string) string {
	if rdSess.Mode() != rdOff {
		s = rdSess.Text(s, where)
	}
	return redactSecrets(s)
}

// rdHint tells the model, once per session, what a placeholder is and that it
// may use one (models otherwise refuse to "use a secret they cannot see").
const rdHint = "\n[jazz: <secreto:…> marks a secret that stays on this machine. To use it, write the marker verbatim in a bash, write or edit call; jazz substitutes the real value locally and the value is never shown to you. Do not ask the user for it.]"

// rdToolOut is rdToolText for the model-facing text of a tool result, plus
// rdHint the first time a placeholder reaches the model.
func rdToolOut(s, where string) string {
	s = rdToolText(s, where)
	if !strings.Contains(s, "<secreto:") {
		return s
	}
	rdSess.mu.Lock()
	first := !rdSess.hinted
	rdSess.hinted = true
	rdSess.mu.Unlock()
	if first {
		s += rdHint
	}
	return s
}

// rdReplaceKeepPH is re.ReplaceAllString(s, tmpl) except for matches that
// contain a placeholder: those are already safe and must stay restorable.
func rdReplaceKeepPH(re *regexp.Regexp, s, tmpl string) string {
	if !strings.Contains(s, "secreto:") {
		return re.ReplaceAllString(s, tmpl)
	}
	return re.ReplaceAllStringFunc(s, func(m string) string {
		if strings.Contains(m, "secreto:") {
			return m
		}
		return re.ReplaceAllString(m, tmpl)
	})
}

// rdRestoreTools are the calls that run locally and may carry a placeholder
// the model wants to use: the restored value never goes back to the model
// (the tool output is redacted again on the way out).
var rdRestoreTools = map[string]bool{"bash": true, "write": true, "edit": true}

// rdRestoreCalls returns calls with placeholders restored (a copy when any
// changed, so the history keeps the placeholder form the model produced).
func rdRestoreCalls(calls []ToolCall) []ToolCall {
	var out []ToolCall
	for i, tc := range calls {
		if !rdRestoreTools[tc.Name] {
			continue
		}
		if a, ok := rdSess.Restore(tc.Args); ok {
			if out == nil {
				out = append([]ToolCall(nil), calls...)
			}
			out[i].Args = a
		}
	}
	if out == nil {
		return calls
	}
	return out
}

// rdLocalBase: a provider on this machine (ollama, llama.cpp, LM Studio) gets
// the request unredacted, since nothing leaves the machine.
func rdLocalBase(base string) bool {
	if base == "" {
		return false
	}
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "localhost", "127.0.0.1", "::1", "0.0.0.0":
		return true
	}
	return false
}

// rdLocal is rdLocalBase (tests swap it: their fake providers listen on 127.0.0.1).
var rdLocal = rdLocalBase

// rdOutbound is the provider-boundary hook: it returns req with every text
// part that leaves the machine redacted. base is the provider URL ("" = remote).
func rdOutbound(req ChatRequest, base string) ChatRequest {
	if rdLocal(base) || rdSess.Mode() == rdOff {
		return req
	}
	req.System = rdSess.Text(req.System, "system")
	if len(req.Tools) > 0 {
		tools := make([]ToolSpec, len(req.Tools))
		for i, tool := range req.Tools {
			tool.Description = rdSess.Text(tool.Description, "tool_description:"+tool.Name)
			tool.Params, _ = rdOutboundSchema(tool.Params, "tool_schema:"+tool.Name).(map[string]any)
			tools[i] = tool
		}
		req.Tools = tools
	}
	msgs := make([]Message, len(req.Messages))
	for i, m := range req.Messages {
		where := m.Role
		if m.Role == "tool" && m.ToolName != "" {
			where = "tool:" + m.ToolName
		}
		m.Content = rdSess.Text(m.Content, where)
		m.Reasoning = rdSess.Text(m.Reasoning, "reasoning")
		if len(m.ToolCalls) > 0 {
			tcs := make([]ToolCall, len(m.ToolCalls))
			for j, tc := range m.ToolCalls {
				tc.Args = rdSess.Text(tc.Args, "tool_call:"+tc.Name)
				tcs[j] = tc
			}
			m.ToolCalls = tcs
		}
		msgs[i] = m
	}
	req.Messages = msgs
	return req
}

func rdOutboundSchema(v any, where string) any {
	switch x := v.(type) {
	case string:
		return rdSess.Text(x, where)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, value := range x {
			out[k] = rdOutboundSchema(value, where)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, value := range x {
			out[i] = rdOutboundSchema(value, where)
		}
		return out
	case []string:
		out := make([]string, len(x))
		for i, value := range x {
			out[i], _ = rdOutboundSchema(value, where).(string)
		}
		return out
	case []map[string]any:
		out := make([]map[string]any, len(x))
		for i, value := range x {
			out[i], _ = rdOutboundSchema(value, where).(map[string]any)
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(x))
		for k, value := range x {
			out[k], _ = rdOutboundSchema(value, where).(string)
		}
		return out
	default:
		return v
	}
}

// ---------- notices and counts ----------

// rdTakeTurn returns how many distinct secrets were first redacted since the
// last call (end-of-turn totals).
func rdTakeTurn() int {
	rdSess.mu.Lock()
	defer rdSess.mu.Unlock()
	n := rdSess.turnNew
	rdSess.turnNew = 0
	return n
}

// rdNoticeText is the one-time explanation, or "" when already shown / nothing redacted.
func rdNoticeText() string {
	rdSess.mu.Lock()
	defer rdSess.mu.Unlock()
	if !rdSess.pending || rdSess.noticed {
		return ""
	}
	rdSess.noticed, rdSess.pending = true, false
	n := rdSess.total
	return fmt.Sprintf("%s: o modelo vê marcadores como <secreto:tipo#1> e o jazz devolve o valor real só nos comandos locais (redact: off desliga · /redact allow <marcador> libera um falso positivo)", rdCountText(n))
}

func rdCountText(n int) string {
	if n == 1 {
		return "1 segredo não saiu da máquina"
	}
	return fmt.Sprintf("%d segredos não saíram da máquina", n)
}

// rdTurnCount is the end-of-turn count (main agent only: a subagent's
// secrets are counted in the parent's turn).
func (a *Agent) rdTurnCount() int {
	if a.sub {
		return 0
	}
	return rdTakeTurn()
}

// rdTotalsText is the end-of-turn totals part.
func rdTotalsText(n int) string {
	if n == 1 {
		return "1 segredo retido"
	}
	return fmt.Sprintf("%d segredos retidos", n)
}

// rdNotice shows the one-time notice (main agent only).
func (a *Agent) rdNotice() {
	if a.sub || a.ui == nil {
		return
	}
	if t := rdNoticeText(); t != "" {
		a.ui.Notice(t)
	}
}

// ---------- per-project allowlist ----------

// The allowlist (.jazz/redact-allow.json) holds HMAC-SHA256 digests of the
// allowed values keyed by a per-machine secret (configDir()/redact.key, a
// secret path the agent cannot read). A hostile repository cannot ship a
// working allowlist and the model cannot add entries (it never sees the
// values nor the key); only /redact allow, typed by the user, writes it.

type rdAllowFile struct {
	Allow []rdAllowEntry `json:"allow"`
}

type rdAllowEntry struct {
	MAC  string `json:"hmac"`
	Kind string `json:"kind,omitempty"`
}

func rdAllowPath(root string) string { return filepath.Join(root, ".jazz", "redact-allow.json") }
func rdKeyPath() string              { return filepath.Join(configDir(), "redact.key") }

// rdKey reads (create=true: creates) the per-machine HMAC key.
func rdKey(create bool) []byte {
	if b, err := os.ReadFile(rdKeyPath()); err == nil {
		if k, err := hex.DecodeString(strings.TrimSpace(string(b))); err == nil && len(k) >= 16 {
			return k
		}
	}
	if !create {
		return nil
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil
	}
	_ = os.MkdirAll(configDir(), 0o700)
	if writeFileAtomic(rdKeyPath(), []byte(hex.EncodeToString(k)+"\n"), 0o600) != nil {
		return nil
	}
	return k
}

var rdMacKey []byte

func (r *Redactor) macLocked(v string) string {
	if len(r.allow) == 0 || rdMacKey == nil {
		return ""
	}
	m := hmac.New(sha256.New, rdMacKey)
	m.Write([]byte(v))
	return hex.EncodeToString(m.Sum(nil))
}

// rdRoot is the project root the allowlist belongs to (tests swap it).
var rdRoot = func() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return lsProjectRoot(wd)
}

func (r *Redactor) loadAllowLocked() {
	root := rdRoot()
	if root == r.allowAt && r.allow != nil {
		return
	}
	r.allowAt, r.allow = root, map[string]bool{}
	if root == "" {
		return
	}
	b, err := os.ReadFile(rdAllowPath(root))
	if err != nil {
		return
	}
	var f rdAllowFile
	if json.Unmarshal(b, &f) != nil {
		return
	}
	if rdMacKey == nil {
		rdMacKey = rdKey(false)
	}
	if rdMacKey == nil {
		return
	}
	for _, e := range f.Allow {
		if e.MAC != "" {
			r.allow[strings.ToLower(e.MAC)] = true
		}
	}
}

// AllowPlaceholder adds the value behind ph to the project allowlist.
func (r *Redactor) AllowPlaceholder(root, ph string) (kind string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.init()
	v, ok := r.byPh[ph]
	if !ok {
		return "", fmt.Errorf("marcador desconhecido nesta sessão: %s", ph)
	}
	if rdMacKey == nil {
		rdMacKey = rdKey(true)
	}
	if rdMacKey == nil {
		return "", errors.New("não consegui criar " + rdKeyPath())
	}
	var f rdAllowFile
	if b, err := os.ReadFile(rdAllowPath(root)); err == nil {
		_ = json.Unmarshal(b, &f)
	}
	m := hmac.New(sha256.New, rdMacKey)
	m.Write([]byte(v))
	mac := hex.EncodeToString(m.Sum(nil))
	kind = r.kindOf[ph]
	dup := false
	for _, e := range f.Allow {
		dup = dup || strings.EqualFold(e.MAC, mac)
	}
	if !dup {
		f.Allow = append(f.Allow, rdAllowEntry{MAC: mac, Kind: kind})
	}
	b, _ := json.MarshalIndent(f, "", "  ")
	if err := os.MkdirAll(filepath.Join(root, ".jazz"), 0o755); err != nil {
		return "", err
	}
	if err := writeFileAtomic(rdAllowPath(root), append(b, '\n'), 0o644); err != nil {
		return "", err
	}
	if r.allow == nil || r.allowAt != root {
		r.allow, r.allowAt = map[string]bool{}, root
	}
	r.allow[mac] = true
	r.cache, r.cacheN = map[string]string{}, 0
	delete(r.byVal, v) // from now on the value goes out as is
	auditRec("redact.allow", map[string]any{"kind": kind})
	return kind, nil
}

// redactCmd is /redact: status · on|off|strict (this session) · allow <marcador>.
func (a *App) redactCmd(args []string) {
	ui := a.ui
	sub := ""
	if len(args) > 0 {
		sub = strings.ToLower(args[0])
	}
	switch sub {
	case "", "status":
		rdSess.mu.Lock()
		n, kinds := rdSess.total, map[string]int{}
		for _, k := range rdSess.kindOf {
			kinds[k]++
		}
		rdSess.mu.Unlock()
		var ks []string
		for k, c := range kinds {
			ks = append(ks, fmt.Sprintf("%s ×%d", k, c))
		}
		sort.Strings(ks)
		line := "redação de segredos: " + rdSess.Mode().String()
		if n > 0 {
			line += " · " + rdCountText(n) + " (" + strings.Join(ks, ", ") + ")"
		}
		ui.Notice(line + "\n/redact on|off|strict muda nesta sessão · /redact allow <secreto:tipo#n> libera um falso positivo neste projeto")
	case "on", "off", "strict":
		m, _ := rdParse(sub)
		rdSess.SetMode(m)
		ui.Notice("redação de segredos nesta sessão: " + m.String())
	case "allow", "permitir", "liberar":
		if len(args) < 2 {
			ui.Error("uso: /redact allow <secreto:tipo#n>")
			return
		}
		kind, err := rdSess.AllowPlaceholder(lsProjectRoot(a.root), strings.TrimSpace(strings.Join(args[1:], " ")))
		if err != nil {
			ui.Error(err.Error())
			return
		}
		ui.Notice(fmt.Sprintf("liberado neste projeto (%s): esse valor passa a ir para o modelo sem marcador · %s", kind, rdAllowPath(lsProjectRoot(a.root))))
	default:
		ui.Error("uso: /redact [status|on|off|strict|allow <marcador>]")
	}
}
