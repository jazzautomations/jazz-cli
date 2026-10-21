package main

// audit.go: append-only, hash-chained audit log (prefix "au").
//
// dataDir()/audit/audit.jsonl, one record per line:
//
//	{"prev":"<sha256 hex>","hash":"<sha256 hex>","rec":{"seq":1,"ts":"…","kind":"config","sid":"…","step":0,"pid":123,"data":{…}}}
//
// hash = sha256(prev + "\n" + rec), over the exact bytes of rec as written.
// The first record has prev = 64 zeros. Every jazz process on the machine
// appends to the same chain under a file lock (audit.lock), so the order is
// global. Past auMaxBytes the file is renamed to audit-<last seq>.jsonl and the
// chain continues in a new audit.jsonl (verify walks them all, oldest first).
// Nothing is ever deleted by jazz. Records are small (shell commands are
// clipped to 200 chars and redacted; contents are only hashed).
//
// What is recorded: see docs/governance.md.

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const auGenesis = "0000000000000000000000000000000000000000000000000000000000000000"

var auMaxBytes int64 = 16 << 20

// auRec is the hashed part of a record.
type auRec struct {
	Seq  int64          `json:"seq"`
	TS   string         `json:"ts"`
	Kind string         `json:"kind"`
	SID  string         `json:"sid,omitempty"`
	Step int            `json:"step,omitempty"`
	PID  int            `json:"pid"`
	Data map[string]any `json:"data,omitempty"`
}

type auLine struct {
	Prev string          `json:"prev"`
	Hash string          `json:"hash"`
	Rec  json.RawMessage `json:"rec"`
}

var auState struct {
	mu    sync.Mutex
	sid   string
	step  int             // step of the main agent (obStep)
	hosts map[string]bool // net egress already recorded by this process
}

func auDir() string  { return filepath.Join(dataDir(), "audit") }
func auPath() string { return filepath.Join(auDir(), "audit.jsonl") }

// auSetSession tags the following records with a session id.
func auSetSession(sid string) {
	auState.mu.Lock()
	auState.sid, auState.step = sid, 0
	auState.mu.Unlock()
}

func auSetStep(step int) {
	auState.mu.Lock()
	auState.step = step
	auState.mu.Unlock()
}

func auHash(prev string, rec []byte) string {
	h := sha256.New()
	h.Write([]byte(prev))
	h.Write([]byte{'\n'})
	h.Write(rec)
	return hex.EncodeToString(h.Sum(nil))
}

// auditRec appends one record to the chain. Errors are reported once on
// stderr: the audit log must not take a run down, but must not fail silently.
func auditRec(kind string, data map[string]any) {
	if os.Getenv("JAZZ_AUDIT") == "0" {
		return
	}
	auState.mu.Lock()
	sid, step := auState.sid, auState.step
	auState.mu.Unlock()
	if err := auAppend(auRec{Kind: kind, SID: sid, Step: step, PID: os.Getpid(), Data: data}); err != nil {
		auWarnOnce(err)
	}
}

var auWarned sync.Once

func auWarnOnce(err error) {
	auWarned.Do(func() { fmt.Fprintln(os.Stderr, "jazz: log de auditoria indisponível:", err) })
}

// auAppend writes r at the end of the chain (process lock + file lock).
func auAppend(r auRec) error {
	auState.mu.Lock()
	defer auState.mu.Unlock()
	if err := os.MkdirAll(auDir(), 0o700); err != nil {
		return err
	}
	lk, err := os.OpenFile(filepath.Join(auDir(), "audit.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lk.Close()
	for i := 0; ; i++ {
		busy, err := pfTryLock(lk)
		if err != nil {
			return err
		}
		if !busy {
			break
		}
		if i > 400 { // ~2 s: another jazz is stuck; write anyway, verify will show a fork
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	defer pfUnlock(lk)
	p := auPath()
	prev, seq := auGenesis, int64(0)
	if last := auLastLine(); last != nil {
		prev, seq = last.Hash, auSeq(last.Rec)
	}
	if fi, err := os.Stat(p); err == nil && fi.Size() > auMaxBytes {
		// named by the last seq it holds: unique and sorts in chain order
		_ = os.Rename(p, filepath.Join(auDir(), fmt.Sprintf("audit-%012d.jsonl", seq)))
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() > 0 {
		b := make([]byte, 1)
		if _, err := f.ReadAt(b, fi.Size()-1); err == nil && b[0] != '\n' {
			_, _ = f.Write([]byte{'\n'}) // torn record from a crash stays visible to verify
		}
	}
	r.Seq = seq + 1
	r.TS = time.Now().UTC().Format(time.RFC3339Nano)
	rec, err := json.Marshal(r)
	if err != nil {
		return err
	}
	line, _ := json.Marshal(auLine{Prev: prev, Hash: auHash(prev, rec), Rec: rec})
	_, err = f.Write(append(line, '\n'))
	return err
}

func auSeq(rec []byte) int64 {
	var r struct {
		Seq int64 `json:"seq"`
	}
	_ = json.Unmarshal(rec, &r)
	return r.Seq
}

// auLastLine finds the last valid record of the chain (current file, or the
// newest rotated file when the current one is new or empty).
func auLastLine() *auLine {
	files := auFiles()
	for i := len(files) - 1; i >= 0; i-- {
		f, err := os.Open(files[i])
		if err != nil {
			continue
		}
		last := obTailLine(f)
		f.Close()
		if last == nil {
			continue
		}
		var l auLine
		if json.Unmarshal(last, &l) == nil && l.Hash != "" {
			return &l
		}
	}
	return nil
}

// auFiles lists the audit files, oldest first (rotated ones, then current).
func auFiles() []string {
	ents, _ := os.ReadDir(auDir())
	var rot []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "audit-") && strings.HasSuffix(e.Name(), ".jsonl") {
			rot = append(rot, filepath.Join(auDir(), e.Name()))
		}
	}
	sort.Strings(rot)
	if _, err := os.Stat(auPath()); err == nil {
		rot = append(rot, auPath())
	}
	return rot
}

func auSync() {
	if f, err := os.OpenFile(auPath(), os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		_ = f.Sync()
		f.Close()
	}
}

// auNetEgress records the first request of this process to each host.
func auNetEgress(host, via string) {
	if host == "" {
		return
	}
	auState.mu.Lock()
	if auState.hosts == nil {
		auState.hosts = map[string]bool{}
	}
	seen := auState.hosts[host+"\x00"+via]
	auState.hosts[host+"\x00"+via] = true
	auState.mu.Unlock()
	if !seen {
		auditRec("net.egress", map[string]any{"host": host, "via": via})
	}
}

// ---------- config in effect ----------

// auConfigStart records the configuration in effect at the start of a run
// and, when config.json changed since the last start, the change (hashes).
func auConfigStart(cfg *Config, root, model, sandbox string, fields map[string]any) {
	p := filepath.Join(configDir(), "config.json")
	raw, _ := os.ReadFile(p)
	fileSHA := ""
	if raw != nil {
		fileSHA = obSHA(string(raw))
	}
	last := filepath.Join(auDir(), "config.sha256")
	prev, _ := os.ReadFile(last)
	if strings.TrimSpace(string(prev)) != fileSHA {
		auditRec("config.changed", map[string]any{"file": p, "before": strings.TrimSpace(string(prev)), "after": fileSHA})
		_ = os.MkdirAll(auDir(), 0o700)
		_ = writeFileAtomic(last, []byte(fileSHA+"\n"), 0o600)
	}
	d := map[string]any{"version": version, "cwd": root, "model": model, "mode": cfg.EffMode(), "sandbox": sandbox,
		"allow_outside": cfg.AllowOutsideProject || cfg.AllowOutsideRun, "config_sha": fileSHA}
	for k, v := range fields {
		d[k] = v
	}
	eff, _ := json.Marshal(d)
	d["effective_sha"] = obSHA(string(eff))
	auditRec("config", d)
}

// ---------- jazz audit ----------

// auVerifyResult is what verification found.
type auVerifyResult struct {
	Records int
	Torn    int      // unparseable lines (crash mid-write)
	Broken  []string // chain breaks: hash mismatch, prev mismatch, seq gap
}

// auVerify walks every audit file and checks the chain.
func auVerify() auVerifyResult {
	var res auVerifyResult
	prev, seq := auGenesis, int64(0)
	for _, p := range auFiles() {
		f, err := os.Open(p)
		if err != nil {
			res.Broken = append(res.Broken, p+": "+err.Error())
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 8<<20)
		n := 0
		for sc.Scan() {
			n++
			b := sc.Bytes()
			if len(strings.TrimSpace(string(b))) == 0 {
				continue
			}
			var l auLine
			if json.Unmarshal(b, &l) != nil || l.Hash == "" {
				res.Torn++
				continue
			}
			where := fmt.Sprintf("%s:%d", filepath.Base(p), n)
			if got := auHash(l.Prev, l.Rec); got != l.Hash {
				res.Broken = append(res.Broken, where+": hash não confere (registro alterado)")
			}
			if l.Prev != prev {
				res.Broken = append(res.Broken, where+": elo anterior não confere (registro removido, inserido ou reordenado)")
			}
			if s := auSeq(l.Rec); s != seq+1 {
				res.Broken = append(res.Broken, fmt.Sprintf("%s: sequência %d depois de %d", where, s, seq))
				seq = s
			} else {
				seq = s
			}
			prev = l.Hash
			res.Records++
		}
		f.Close()
	}
	return res
}

// cmdAudit: jazz audit [--since D|data] [--verify] [--json] [--kind K]
func cmdAudit(args []string, w io.Writer) int {
	verify, raw := false, false
	var since time.Time
	kind := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--verify" || a == "-verify":
			verify = true
		case a == "--json" || a == "-json":
			raw = true
		case (a == "--since" || a == "-since") && i+1 < len(args):
			i++
			since = auParseSince(args[i])
			if since.IsZero() {
				fmt.Fprintln(os.Stderr, "jazz: --since espera uma duração (24h, 7d) ou data (2026-10-20)")
				return 2
			}
		case (a == "--kind" || a == "-kind") && i+1 < len(args):
			i++
			kind = args[i]
		default:
			fmt.Fprintln(os.Stderr, "uso: jazz audit [--since 24h|7d|2026-10-20] [--kind K] [--json] [--verify]")
			return 2
		}
	}
	if !verify || raw || !since.IsZero() || kind != "" {
		for _, p := range auFiles() {
			f, err := os.Open(p)
			if err != nil {
				continue
			}
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 64<<10), 8<<20)
			for sc.Scan() {
				var l auLine
				if json.Unmarshal(sc.Bytes(), &l) != nil {
					continue
				}
				var r auRec
				if json.Unmarshal(l.Rec, &r) != nil {
					continue
				}
				if !since.IsZero() {
					if t, err := time.Parse(time.RFC3339Nano, r.TS); err == nil && t.Before(since) {
						continue
					}
				}
				if kind != "" && r.Kind != kind && !strings.HasPrefix(r.Kind, kind+".") {
					continue
				}
				if raw {
					fmt.Fprintln(w, sc.Text())
					continue
				}
				fmt.Fprintln(w, auFormat(r))
			}
			f.Close()
		}
	}
	if verify {
		v := auVerify()
		if len(v.Broken) == 0 {
			fmt.Fprintf(w, "✓ cadeia íntegra: %d registros em %d arquivo(s)", v.Records, len(auFiles()))
			if v.Torn > 0 {
				fmt.Fprintf(w, " · %d linha(s) cortada(s) por crash (fora da cadeia)", v.Torn)
			}
			fmt.Fprintln(w)
			return 0
		}
		fmt.Fprintf(w, "✗ cadeia QUEBRADA: %d problema(s) em %d registros\n", len(v.Broken), v.Records)
		for _, b := range v.Broken[:min(len(v.Broken), 20)] {
			fmt.Fprintln(w, "  "+b)
		}
		return 1
	}
	return 0
}

func auParseSince(s string) time.Time {
	if strings.HasSuffix(s, "d") {
		var n int
		if _, err := fmt.Sscanf(s, "%dd", &n); err == nil && n > 0 {
			return time.Now().Add(-time.Duration(n) * 24 * time.Hour)
		}
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return time.Now().Add(-d)
	}
	for _, layout := range []string{"2006-01-02", time.RFC3339, "2006-01-02T15:04"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t
		}
	}
	return time.Time{}
}

// auFormat renders a record as one PT line.
func auFormat(r auRec) string {
	ts := r.TS
	if t, err := time.Parse(time.RFC3339Nano, r.TS); err == nil {
		ts = t.Local().Format("2006-01-02 15:04:05")
	}
	who := ""
	if r.SID != "" {
		who = " sessão " + r.SID
	}
	if r.Step > 0 {
		who += fmt.Sprintf(" passo %d", r.Step)
	}
	d := r.Data
	g := func(k string) string { return fmt.Sprint(d[k]) }
	var what string
	switch r.Kind {
	case "config":
		what = fmt.Sprintf("config em vigor: modelo %s · modo %s · guardas %s · sandbox %s · block_ux %s · fora do projeto %s · hash %s", g("model"), g("mode"), g("guards"), g("sandbox"), g("block_ux"), g("allow_outside"), obShort(g("effective_sha")))
	case "config.changed":
		what = fmt.Sprintf("config.json mudou: %s → %s", obShort(g("before")), obShort(g("after")))
	case "file.change":
		what = fmt.Sprintf("arquivo %s (%s via %s): %s → %s", g("path"), g("op"), g("via"), auShortHash(d["before"]), auShortHash(d["after"]))
	case "shell":
		what = fmt.Sprintf("shell %s: %s", obShort(g("cmd_sha")), g("cmd"))
	case "action.refused":
		what = fmt.Sprintf("recusado [%s] %s %s: %s (%s)", g("guard"), g("tool"), g("target"), g("reason"), g("outcome"))
	case "approval":
		what = fmt.Sprintf("aprovação %s: %s %s", g("answer"), g("tool"), g("target"))
	case "trust":
		what = fmt.Sprintf("confiança %s: %s %v", g("action"), g("root"), d["kinds"])
	case "net.egress":
		what = fmt.Sprintf("rede: %s (%s)", g("host"), g("via"))
	case "update":
		what = fmt.Sprintf("atualização %s → %s · assinatura %s · sha256 %s", g("from"), g("to"), g("signature"), obShort(g("sha256")))
	case "run.end":
		what = fmt.Sprintf("execução terminou: %s (%s) · saída %s", g("status"), g("outcome"), g("exit"))
	default:
		b, _ := json.Marshal(d)
		what = string(b)
	}
	return fmt.Sprintf("%5d %s %-15s%s · %s", r.Seq, ts, r.Kind, who, oneLine(what, 300))
}

func auShortHash(v any) string {
	s, _ := v.(string)
	if s == "" {
		return "∅"
	}
	if i := strings.IndexByte(s, ':'); i >= 0 && len(s) > i+13 {
		return s[:i+13]
	}
	return s
}

// auTransport records network egress (first request per host and kind per
// process) for every request made through rt.
func auTransport(via string, rt http.RoundTripper) http.RoundTripper { return auRT{base: rt, via: via} }

type auRT struct {
	base http.RoundTripper
	via  string
}

func (r auRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL != nil {
		auNetEgress(req.URL.Hostname(), r.via)
	}
	return r.base.RoundTrip(req)
}

// pfSandboxStatus is the sandbox line of the last sbSetup.
func pfSandboxStatus() string {
	s, _ := sbStatus.Load().(string)
	return s
}

// pfAuditFields are the run settings recorded in the config audit record.
func pfAuditFields(ag *Agent, o pfOpts, headless bool) map[string]any {
	guards := "on"
	if !ag.hgOn("tests") {
		guards = "off"
	}
	bux := "on"
	if !ag.blockUX() {
		bux = "off"
	}
	return map[string]any{"headless": headless, "guards": guards, "block_ux": bux, "budget": ag.budgetLevel(),
		"recover": ag.rcMode(), "allow_tools": o.allowTools, "timeout": o.timeout.String(), "until": o.loop.Until != ""}
}
