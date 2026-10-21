package main

// debug.go: debug log, crash recovery helpers (G6, bug 68). Owner: platform.
// Contract surface (CONTRACT.md): dbg, debugOn, safeGo.
//
// With --debug or JAZZ_DEBUG=1 every interesting event is appended as one JSON
// line to dataDir()/logs/<start>-<pid>.log (0600). The log rotates at 20 MB and
// only the 5 newest files are kept. Secrets are redacted (known key values,
// common token shapes, auth headers); the system prompt is logged in full once
// and afterwards only as its sha256, so a long session stays readable.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"
)

// debugOn is set by main from --debug / JAZZ_DEBUG=1.
var debugOn bool

const (
	pfLogMaxBytes = 20 << 20  // rotate the debug log past this size
	pfLogKeep     = 5         // newest log files kept
	pfSSECap      = 256 << 10 // raw SSE bytes logged per response
	pfRingLines   = 50        // dbg lines kept in memory for the crash file
)

var pfDbg struct {
	mu      sync.Mutex
	f       *os.File
	path    string
	size    int64
	sse     int // SSE bytes logged since the last request/status event
	sysSeen map[string]bool
	ring    []string
}

func pfLogDir() string { return filepath.Join(dataDir(), "logs") }

// pfDebugStart turns debug logging on and opens a fresh log file. It returns the path.
func pfDebugStart() (string, error) {
	pfDbg.mu.Lock()
	defer pfDbg.mu.Unlock()
	debugOn = true
	if pfDbg.f != nil {
		return pfDbg.path, nil
	}
	if err := pfOpenLog(); err != nil {
		return "", err
	}
	// Fatal runtime errors (concurrent map writes, out of memory) cannot be
	// recovered; at least keep their trace next to the debug log.
	if f, err := os.OpenFile(strings.TrimSuffix(pfDbg.path, ".log")+".fatal.txt", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600); err == nil {
		_ = debug.SetCrashOutput(f, debug.CrashOptions{})
		f.Close()
	}
	return pfDbg.path, nil
}

// pfOpenLog opens a new log file and prunes old ones. Caller holds pfDbg.mu.
func pfOpenLog() error {
	d := pfLogDir()
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	name := fmt.Sprintf("%s-%d.log", time.Now().Format("20060102-150405.000"), os.Getpid())
	p := filepath.Join(d, name)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if pfDbg.f != nil {
		pfDbg.f.Close()
	}
	pfDbg.f, pfDbg.path, pfDbg.size = f, p, 0
	pfPruneLogs(d)
	return nil
}

// pfPruneLogs keeps only the newest pfLogKeep *.log files.
func pfPruneLogs(d string) {
	ents, err := os.ReadDir(d)
	if err != nil {
		return
	}
	var logs []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".log") {
			logs = append(logs, e.Name())
		}
	}
	sort.Strings(logs) // names start with a sortable timestamp
	for len(logs) > pfLogKeep {
		os.Remove(filepath.Join(d, logs[0]))
		os.Remove(filepath.Join(d, strings.TrimSuffix(logs[0], ".log")+".fatal.txt"))
		logs = logs[1:]
	}
}

// pfDebugPath is the current log file ("" when debug is off).
func pfDebugPath() string {
	pfDbg.mu.Lock()
	defer pfDbg.mu.Unlock()
	return pfDbg.path
}

// pfLatestLog returns the newest debug log, if any (for `jazz bug`).
func pfLatestLog() string {
	ents, err := os.ReadDir(pfLogDir())
	if err != nil {
		return ""
	}
	best := ""
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".log") && e.Name() > best {
			best = e.Name()
		}
	}
	if best == "" {
		return ""
	}
	return filepath.Join(pfLogDir(), best)
}

// dbg records one structured debug event (no-op unless debugOn). Safe for
// concurrent use. Known secrets and auth headers are redacted; system prompts
// are logged once and then replaced by their sha256.
func dbg(kind string, v any) {
	obTee(kind, v) // selected kinds also go to the session event stream (obs.go)
	if !debugOn {
		return
	}
	pfDbg.mu.Lock()
	defer pfDbg.mu.Unlock()
	if pfDbg.sysSeen == nil {
		pfDbg.sysSeen = map[string]bool{}
	}
	switch kind {
	case "request", "status":
		pfDbg.sse = 0
	case "sse":
		if pfDbg.sse >= pfSSECap {
			return
		}
		if s, ok := v.(string); ok {
			pfDbg.sse += len(s)
		} else {
			pfDbg.sse += 256
		}
		if pfDbg.sse >= pfSSECap {
			v = map[string]any{"data": v, "note": "SSE log cap reached for this response"}
		}
	}
	data := pfScrubValue(v, "", pfDbg.sysSeen)
	line, err := json.Marshal(map[string]any{"t": time.Now().Format(time.RFC3339Nano), "kind": kind, "data": data})
	if err != nil {
		line, _ = json.Marshal(map[string]any{"t": time.Now().Format(time.RFC3339Nano), "kind": kind, "data": fmt.Sprint(v)})
	}
	s := redactSecrets(string(line))
	pfDbg.ring = append(pfDbg.ring, s)
	if len(pfDbg.ring) > pfRingLines {
		pfDbg.ring = pfDbg.ring[len(pfDbg.ring)-pfRingLines:]
	}
	if pfDbg.f == nil {
		return
	}
	n, _ := pfDbg.f.WriteString(s + "\n")
	pfDbg.size += int64(n)
	if pfDbg.size > pfLogMaxBytes {
		_ = pfOpenLog()
	}
}

var pfSecretHeaders = map[string]bool{"authorization": true, "x-api-key": true, "api-key": true, "x-goog-api-key": true,
	"proxy-authorization": true, "cookie": true, "set-cookie": true, "chatgpt-account-id": true}

// pfScrubValue turns v into plain JSON values, redacting auth headers and
// replacing already-seen system prompts by their hash.
func pfScrubValue(v any, key string, seen map[string]bool) any {
	switch x := v.(type) {
	case nil, bool, float64, int, int64, string:
		if s, ok := x.(string); ok && (key == "system" || key == "instructions") && len(s) > 200 {
			return pfSysRef(s, seen)
		}
		return x
	case json.RawMessage:
		var y any
		if json.Unmarshal(x, &y) != nil {
			return string(x)
		}
		return pfScrubValue(y, key, seen)
	case []byte:
		var y any
		if json.Unmarshal(x, &y) != nil {
			return string(x)
		}
		return pfScrubValue(y, key, seen)
	case error:
		return x.Error()
	case map[string]any:
		out := make(map[string]any, len(x))
		isSys := x["role"] == "system" || x["role"] == "developer"
		for k, val := range x {
			switch {
			case pfSecretHeaders[strings.ToLower(k)]:
				out[k] = "[REDACTED]"
			case isSys && k == "content":
				if s, ok := val.(string); ok && len(s) > 200 {
					out[k] = pfSysRef(s, seen)
					continue
				}
				out[k] = pfScrubValue(val, k, seen)
			default:
				out[k] = pfScrubValue(val, k, seen)
			}
		}
		return out
	case map[string]string:
		m := make(map[string]any, len(x))
		for k, val := range x {
			m[k] = val
		}
		return pfScrubValue(m, key, seen)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = pfScrubValue(e, key, seen)
		}
		return out
	}
	// structs and other types: round-trip through JSON
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	var y any
	if json.Unmarshal(b, &y) != nil {
		return string(b)
	}
	if _, isStr := y.(string); isStr {
		return y
	}
	return pfScrubValue(y, key, seen)
}

func pfSysRef(s string, seen map[string]bool) any {
	h := sha256.Sum256([]byte(s))
	id := "sha256:" + hex.EncodeToString(h[:8])
	if seen[id] {
		return id
	}
	seen[id] = true
	return map[string]any{"hash": id, "text": s}
}

// pfRecentDbg returns the last dbg lines (for crash files).
func pfRecentDbg() []string {
	pfDbg.mu.Lock()
	defer pfDbg.mu.Unlock()
	return append([]string(nil), pfDbg.ring...)
}

// ---------- crash handling ----------

var pfCrashState struct {
	mu       sync.Mutex
	cleanups []func()
	once     sync.Once
}

// pfOnCrash registers cleanup to run before a crash exit (restore terminal,
// kill the shell process group, stop MCP servers). Cleanups run in reverse order.
func pfOnCrash(f func()) {
	pfCrashState.mu.Lock()
	pfCrashState.cleanups = append(pfCrashState.cleanups, f)
	pfCrashState.mu.Unlock()
}

// pfCrashGuard must be deferred directly: it recovers a panic and crashes cleanly.
func pfCrashGuard() {
	if r := recover(); r != nil {
		pfCrash(r, debug.Stack())
	}
}

// pfExit is os.Exit, replaceable in tests.
var pfExit = os.Exit

// pfCrash restores the terminal, kills children, writes dataDir()/crash-<ts>.txt
// and exits with status 70. Only the first crash does the work.
func pfCrash(r any, stack []byte) {
	did := false
	pfCrashState.once.Do(func() {
		did = true
		pfCrashState.mu.Lock()
		cl := append([]func(){}, pfCrashState.cleanups...)
		pfCrashState.mu.Unlock()
		for i := len(cl) - 1; i >= 0; i-- {
			func() {
				defer func() { _ = recover() }()
				cl[i]()
			}()
		}
		var b strings.Builder
		fmt.Fprintf(&b, "jazz-cli %s crash at %s\n%s/%s %s\n\npanic: %v\n\n%s\n", version, time.Now().Format(time.RFC3339), runtime.GOOS, runtime.GOARCH, runtime.Version(), r, stack)
		if lines := pfRecentDbg(); len(lines) > 0 {
			b.WriteString("\n--- last debug events ---\n")
			b.WriteString(strings.Join(lines, "\n"))
			b.WriteString("\n")
		}
		path := ""
		if os.MkdirAll(dataDir(), 0o700) == nil {
			p := filepath.Join(dataDir(), "crash-"+time.Now().Format("20060102-150405")+".txt")
			if os.WriteFile(p, []byte(redactSecrets(b.String())), 0o600) == nil {
				path = p
			}
		}
		fmt.Fprintf(os.Stderr, "\n\njazz: erro interno (%v)\n", r)
		if path != "" {
			fmt.Fprintf(os.Stderr, "jazz: detalhes salvos em %s (anexe ao reportar: jazz bug)\n", path)
		}
		fmt.Fprintln(os.Stderr, "jazz: se o terminal ficar estranho, rode: stty sane; printf '\\e[?2004l'")
		pfExit(70)
	})
	if did { // only reached when pfExit is stubbed (tests)
		return
	}
	// another goroutine is already crashing: wait for it to exit the process
	select {}
}

// safeGo runs f in a goroutine with the crash handler installed.
func safeGo(f func()) {
	go func() {
		defer pfCrashGuard()
		f()
	}()
}
