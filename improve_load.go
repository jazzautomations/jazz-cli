package main

// improve_load.go: what `jazz improve` learns from — finished runs, read back
// from the files jazz already writes (docs/observability.md):
//   - <id>.jsonl          the session tree (tool calls, results, end record);
//   - <id>.events.jsonl   requests, tools, guards, deadlines, run.end;
//   - eval runner output  (evals/v2/run.sh): summary.txt + <task>.log (-v text).
// Everything is parsed defensively: torn lines, old v1 sessions and logs of
// older versions are fine. Nothing here calls a model.

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// imCall is one tool call of a run with its outcome.
type imCall struct {
	Name   string // canonical tool name
	Target string // toolDetail: path, command…
	Cmd    string // bash: the command
	Path   string // read/write/edit: the path
	Old    string // edit: first old_string (clipped)
	New    string // edit: first new_string (clipped)
	Result string // first imMaxResult bytes of the result
	Err    bool
	Exit   int // bash exit code (-1 = unknown)
}

// imRun is one finished (or abandoned) run.
type imRun struct {
	ID, Path, Cwd, Model, Task string
	Source                     string // session | log
	Calls                      []imCall
	Events                     []obEvent
	End                        *sessEnd
	Status                     string  // StopReason of the last run.end / end record ("" = unknown)
	DurMS                      float64 // active time (events) or wall time (eval summary)
	Requests                   int
	R429                       map[string]int // log runs: HTTP 429 per model
	Eval                       *imEvalTask    // set when the run came from an eval runner
	MsgLat                     []imLat        // per-request latency from session msg metadata (no events)
}

type imLat struct {
	Model string
	MS    float64
	Out   int
	Stop  string
}

// imEvalTask is one line of an eval summary.txt.
type imEvalTask struct {
	RunDir, Task, Model, Version string
	Pass                         bool
	Secs, Tools, RC              int
}

const imMaxResult = 2048

// ---------- sessions ----------

// imLoadSession reads one session file (and its events, when present).
func imLoadSession(path string) (*imRun, error) {
	ents, err := sessReadEntries(path)
	if err != nil {
		return nil, err
	}
	if len(ents) == 0 || ents[0].T != "meta" {
		return nil, os.ErrInvalid
	}
	r := &imRun{ID: strings.TrimSuffix(filepath.Base(path), ".jsonl"), Path: path, Source: "session"}
	wal := map[string]sessTool{}
	for _, e := range ents {
		if e.T == "tool_result" && e.Tool != nil {
			wal[e.Tool.CallID] = *e.Tool
		}
	}
	idx := map[string]int{} // call id -> index in r.Calls
	for _, e := range sessPath(ents, "") {
		switch e.T {
		case "meta":
			r.Cwd, r.Model = e.Cwd, e.Model
		case "model":
			r.Model = e.Model
		case "end":
			if e.End != nil {
				r.End, r.Status = e.End, e.End.Status
			}
		case "msg":
			m := e.Msg
			if m == nil {
				continue
			}
			switch m.Role {
			case "user":
				if r.Task == "" && !strings.HasPrefix(m.Content, "<summary>") {
					r.Task = oneLine(strings.TrimSpace(acStripReminders(m.Content)), 400)
				}
			case "assistant":
				if e.LatencyMS > 0 {
					l := imLat{Model: e.Model, MS: float64(e.LatencyMS), Stop: e.Stop}
					if e.Usage != nil {
						l.Out = e.Usage.Out
					}
					if l.Model == "" {
						l.Model = r.Model
					}
					r.MsgLat = append(r.MsgLat, l)
				}
				for _, tc := range m.ToolCalls {
					idx[tc.ID] = len(r.Calls)
					r.Calls = append(r.Calls, imNewCall(tc))
				}
			case "tool":
				i, ok := idx[m.ToolCallID]
				if !ok {
					continue
				}
				c := &r.Calls[i]
				c.Result = imClip(m.Content, imMaxResult)
				c.Exit = imExit(m.Content)
				if w, ok := wal[m.ToolCallID]; ok {
					c.Err = w.IsErr
				} else {
					c.Err = imLooksErr(c.Name, m.Content)
				}
				if c.Name == "bash" && c.Exit > 0 {
					c.Err = true
				}
			}
		}
	}
	if evs, err := obReadEvents(obEventsPath(path)); err == nil {
		r.Events = evs
		for _, ev := range evs {
			switch ev.str("kind") {
			case "run.end":
				r.Status = ev.str("status")
				r.DurMS += ev.num("dur_ms")
			case "request.end":
				r.Requests++
			}
		}
	}
	if r.Requests == 0 {
		r.Requests = len(r.MsgLat)
	}
	return r, nil
}

// imNewCall turns a tool call into an imCall (outcome filled in later).
func imNewCall(tc ToolCall) imCall {
	name := CanonicalTool(tc.Name)
	c := imCall{Name: name, Target: toolDetail(tc.Name, tc.Args), Exit: -1}
	m, _, err := parseArgs(tc.Args)
	if err != nil {
		return c
	}
	m = normalizeArgs(name, m)
	switch name {
	case "bash":
		c.Cmd, _ = argStr(m, "command")
	case "read", "write", "edit":
		c.Path, _ = argStr(m, "path")
		if name == "edit" {
			c.Old, _ = argStr(m, "old_string")
			c.New, _ = argStr(m, "new_string")
			if c.Old == "" {
				if l, ok := m["edits"].([]any); ok && len(l) > 0 {
					if e0, ok := l[0].(map[string]any); ok {
						c.Old, _ = argStr(e0, "old_string")
						c.New, _ = argStr(e0, "new_string")
					}
				}
			}
			c.Old, c.New = imClip(c.Old, imMaxResult), imClip(c.New, imMaxResult)
		}
	}
	return c
}

var imExitRE = regexp.MustCompile(`\[exit code (-?\d+)\]`)

// imExit is the bash exit code printed near the end of a result (-1 = none);
// a "[cwd: …]" note may follow it.
func imExit(s string) int {
	if len(s) > 600 {
		s = s[len(s)-600:]
	}
	if all := imExitRE.FindAllStringSubmatch(s, -1); len(all) > 0 {
		n, _ := strconv.Atoi(all[len(all)-1][1])
		return n
	}
	return -1
}

// imErrPrefix: how jazz's tools start an error result (tools.go, edit.go,
// budget.go, guard_honest.go, blockux.go). Used for sessions written before
// the write-ahead log recorded is_err.
var imErrPrefix = regexp.MustCompile(`^(Error|error:|Cannot |Could not find|Refused|Validation failed|TOOL_NOT_EXECUTED|TOOL_NOT_STARTED|TOOL_OUTCOME_UNKNOWN|Tool "[^"]*" not found|BLOCKED|Blocked|Denied|denied|old_string must|No changes made|Found \d+ occurrences|edits\[\d+\]|offset \d+ is past|\S+ (does not exist|changed since you last read|already exists\. Read it first|is a directory))`)

func imLooksErr(tool, s string) bool {
	if tool == "bash" {
		return imExit(s) > 0
	}
	return imErrPrefix.MatchString(strings.TrimSpace(s))
}

func imClip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

// ---------- eval runner output ----------

var (
	imSumHeadRE = regexp.MustCompile(`^jazz:\s*(.*?)\s+modelo:\s*(\S+)`)
	imSumLineRE = regexp.MustCompile(`^(\S+)\s+(PASS|FAIL)\s+(\d+)s\s+tools=(\S+)\s+rc=(\S+)`)
)

// imParseSummary reads an eval summary.txt.
func imParseSummary(path string) []imEvalTask {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	dir := filepath.Base(filepath.Dir(path))
	ver, model := "", ""
	var out []imEvalTask
	for _, line := range strings.Split(string(b), "\n") {
		if m := imSumHeadRE.FindStringSubmatch(line); m != nil {
			ver, model = m[1], m[2]
			continue
		}
		m := imSumLineRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		t := imEvalTask{RunDir: dir, Task: m[1], Model: model, Version: ver, Pass: m[2] == "PASS"}
		t.Secs, _ = strconv.Atoi(m[3])
		t.Tools, _ = strconv.Atoi(m[4])
		t.RC, _ = strconv.Atoi(m[5])
		out = append(out, t)
	}
	return out
}

var (
	imLogSessRE  = regexp.MustCompile(`^· sessão (\S+) · (\S+)`)
	imLogCallRE  = regexp.MustCompile(`^● (\S+)\s?(.*)$`)
	imLogOKRE    = regexp.MustCompile(`^\s+⎿ (\S+)\s?(.*)$`)
	imLogErrRE   = regexp.MustCompile(`^\s+✗ (\S+)\s?(.*)$`)
	imLog429RE   = regexp.MustCompile(`^· (\S+): HTTP 429`)
	imLogDoneRE  = regexp.MustCompile(`^· (\S+) · (\d+) requisiç`)
	imLogBashRE  = regexp.MustCompile(`^bash exit (-?\d+)`)
	imLogTimeRE  = regexp.MustCompile(`\s*\(\d+(\.\d+)?(ms|s|µs|m\d*s?)\)\s*$`)
	imLogTimeout = "tempo esgotado"
)

// imLoadLog parses the -v text log of one eval task.
func imLoadLog(path string) *imRun {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	r := &imRun{Path: path, Source: "log", R429: map[string]int{}, Task: strings.TrimSuffix(filepath.Base(path), ".log")}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	pending := -1
	for sc.Scan() {
		line := sc.Text()
		switch {
		case r.ID == "" && imLogSessRE.MatchString(line):
			m := imLogSessRE.FindStringSubmatch(line)
			r.ID, r.Model = m[1], m[2]
		case imLogCallRE.MatchString(line):
			m := imLogCallRE.FindStringSubmatch(line)
			c := imCall{Name: CanonicalTool(m[1]), Target: strings.TrimSpace(m[2]), Exit: -1}
			switch c.Name {
			case "bash":
				c.Cmd = c.Target
			case "read", "write", "edit":
				c.Path = c.Target
			}
			r.Calls = append(r.Calls, c)
			pending = len(r.Calls) - 1
		case pending >= 0 && imLogOKRE.MatchString(line):
			m := imLogOKRE.FindStringSubmatch(line)
			c := &r.Calls[pending]
			if b := imLogBashRE.FindStringSubmatch(m[1] + " " + m[2]); b != nil {
				c.Exit, _ = strconv.Atoi(b[1])
				c.Err = c.Exit > 0 // the -v line has no error text: only the exit code counts
			} else {
				c.Result = m[2]
			}
			pending = -1
		case pending >= 0 && imLogErrRE.MatchString(line):
			m := imLogErrRE.FindStringSubmatch(line)
			c := &r.Calls[pending]
			c.Result, c.Err = imLogTimeRE.ReplaceAllString(m[2], ""), true
			pending = -1
		case imLog429RE.MatchString(line):
			r.R429[imLog429RE.FindStringSubmatch(line)[1]]++
		case imLogDoneRE.MatchString(line):
			m := imLogDoneRE.FindStringSubmatch(line)
			r.Status = m[1]
			r.Requests, _ = strconv.Atoi(m[2])
		case strings.Contains(line, imLogTimeout) && strings.HasPrefix(line, "jazz:"):
			r.Status = string(StopTimeout)
		}
	}
	return r
}

// ---------- discovery ----------

// imSources is everything found under a set of roots.
type imSources struct {
	Runs  []*imRun
	Evals []imEvalTask
}

// imScan walks dir for sessions, events, eval summaries and eval logs. A run
// that has both a session file and an eval log keeps the session (richer)
// and takes the 429 counts and the eval result from the log.
func imScan(dirs ...string) imSources {
	var src imSources
	bySess := map[string]*imRun{}
	var logs []*imRun
	evalByKey := map[string]*imEvalTask{} // runDir/task
	for _, d := range dirs {
		_ = filepath.WalkDir(d, func(p string, de os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if de.IsDir() {
				switch de.Name() {
				case "work", "checkpoints", "blobs", "spill", "jobs", ".git", "node_modules", "__pycache__":
					if p != d {
						return filepath.SkipDir
					}
				}
				return nil
			}
			name := de.Name()
			switch {
			case name == "summary.txt":
				for _, t := range imParseSummary(p) {
					src.Evals = append(src.Evals, t)
				}
			case strings.HasSuffix(name, obEventsSuffix):
			case strings.HasSuffix(name, ".jsonl") && strings.Contains(filepath.ToSlash(p), "/sessions/"):
				if r, err := imLoadSession(p); err == nil && bySess[r.ID] == nil {
					bySess[r.ID] = r
					src.Runs = append(src.Runs, r)
				}
			case strings.HasSuffix(name, ".log") && pfExists(filepath.Join(filepath.Dir(p), "summary.txt")):
				if r := imLoadLog(p); r != nil {
					r.Cwd = filepath.Base(filepath.Dir(p)) + "/" + r.Task
					logs = append(logs, r)
				}
			}
			return nil
		})
	}
	for i := range src.Evals {
		t := &src.Evals[i]
		evalByKey[t.RunDir+"/"+t.Task] = t
	}
	for _, l := range logs {
		if t := evalByKey[l.Cwd]; t != nil {
			l.Eval = t
			l.DurMS = float64(t.Secs) * 1000
			l.Model = orDefault(l.Model, t.Model)
		}
		if s := bySess[l.ID]; l.ID != "" && s != nil {
			s.R429, s.Eval = l.R429, l.Eval
			if s.Status == "" {
				s.Status = l.Status
			}
			if s.Requests == 0 {
				s.Requests = l.Requests
			}
			continue
		}
		src.Runs = append(src.Runs, l)
	}
	// a session of an eval task dir (…/<runDir>/work/<task>) gets its result
	for _, r := range src.Runs {
		if r.Eval != nil || r.Cwd == "" {
			continue
		}
		task := filepath.Base(r.Cwd)
		runDir := filepath.Base(filepath.Dir(filepath.Dir(r.Cwd)))
		if filepath.Base(filepath.Dir(r.Cwd)) == "work" {
			if t := evalByKey[runDir+"/"+task]; t != nil {
				r.Eval = t
			}
		}
	}
	sort.SliceStable(src.Runs, func(i, j int) bool { return src.Runs[i].ID < src.Runs[j].ID })
	return src
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// imProjectRuns loads the newest n sessions of root (or the given ids).
func imProjectRuns(root string, n int, ids []string) []*imRun {
	list := ListSessions(root)
	var out []*imRun
	for _, si := range list {
		if len(ids) > 0 {
			hit := false
			for _, id := range ids {
				if si.ID == id || (len(id) >= 4 && strings.HasPrefix(si.ID, id)) {
					hit = true
				}
			}
			if !hit {
				continue
			}
		} else if n > 0 && len(out) >= n {
			break
		}
		if r, err := imLoadSession(si.Path); err == nil {
			out = append(out, r)
		}
	}
	return out
}

// imJSON is a small helper for --json output.
func imJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}
