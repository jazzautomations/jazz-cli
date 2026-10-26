package main

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// Session: append-only JSONL, one file per conversation, grouped by project dir.
//
// Format v2 (after pi's session-manager, see docs/research/pi-sessions.md):
// every entry carries an id and the id of its parent entry, so the file is a
// tree. The current position (leaf) is the last entry written; /rewind and
// /tree move the leaf back and the next entry becomes a new branch. Rewind
// entries are still written, so a linear replay (older jazz) and a tree walk
// (this one) rebuild the same conversation. Files without ids (v1) are
// migrated on read: each entry's parent is the line before it.
type Session struct {
	ID   string
	Path string
	f    *os.File

	mu     sync.Mutex
	leaf   string   // id of the last entry on the active branch
	base   string   // id of the meta or newest compact entry on the active branch
	msgIDs []string // entry ids of the live messages (index = position in Agent.Msgs)
	// unaligned: msgIDs does not match Agent.Msgs (a load repaired missing
	// tool results); rewinds then rely on the N of the rewind entry alone
	unaligned bool
	Tampered  int    // lines that failed the HMAC on load (hardening2.go)
	Legacy    int    // unsigned lines from before signing
	title     string // newest title entry (sessions_ui.go); "" = none yet
}

type sessEntry struct {
	T        string   `json:"t"` // meta | msg | compact | model | rewind | goal | recover | branch | tool_start | tool_result | end | title
	ID       string   `json:"id,omitempty"`
	ParentID string   `json:"parentId,omitempty"` // "" = root
	TS       int64    `json:"ts,omitempty"`       // unix ms when the entry was written
	V        int      `json:"v,omitempty"`        // meta: format version (2 = tree)
	Time     int64    `json:"time,omitempty"`
	Cwd      string   `json:"cwd,omitempty"`
	Model    string   `json:"model,omitempty"`
	System   string   `json:"system,omitempty"`
	Msg      *Message `json:"msg,omitempty"`
	Summary  string   `json:"summary,omitempty"`
	N        int      `json:"n,omitempty"`      // rewind: keep Msgs[:N]
	Parent   string   `json:"parent,omitempty"` // meta of a fork: the session it came from
	// meta of a subagent's session (subagent.go): its id in the parent's events
	// and the parent session entry it branched from
	Sub         string `json:"sub,omitempty"`
	ParentEntry string `json:"parent_entry,omitempty"`
	// assistant msg: what produced it (pi records the same per message)
	Provider  string    `json:"provider,omitempty"`
	Usage     *Usage    `json:"usage,omitempty"`
	Stop      string    `json:"stop,omitempty"`
	LatencyMS int64     `json:"latency_ms,omitempty"`
	Target    string    `json:"target,omitempty"` // branch: the entry the leaf moved to
	Tool      *sessTool `json:"tool,omitempty"`   // tool_start / tool_result (write-ahead)
	End       *sessEnd  `json:"end,omitempty"`    // end of a headless run (obs.go)
	Title     string    `json:"title,omitempty"`  // title: the session's name (auto from the first prompt, or /rename)
	// hardening2.go: set on read only. Tampered = the line failed its HMAC (or
	// was unsigned after signed ones); Legacy = written before signing existed.
	Tampered bool `json:"-"`
	Legacy   bool `json:"-"`
}

// sessTool is a write-ahead record of one tool call: tool_start is written
// before the tool runs, tool_result the moment it returns (before any harness
// note is appended), so a crash mid-turn never loses a finished result.
type sessTool struct {
	CallID  string `json:"call_id"`
	Name    string `json:"name"`
	Step    int    `json:"step,omitempty"`
	ArgsSHA string `json:"args_sha,omitempty"`
	Result  string `json:"result,omitempty"`
	IsErr   bool   `json:"is_err,omitempty"`
	DurMS   int64  `json:"dur_ms,omitempty"`
}

// sessEnd closes a headless run: how it ended and where the work is.
type sessEnd struct {
	Status     string `json:"status"`  // StopReason
	Outcome    string `json:"outcome"` // completed | incomplete | denied
	Checkpoint string `json:"checkpoint,omitempty"`
	Handoff    string `json:"handoff,omitempty"` // path of the handoff note
	Note       string `json:"note,omitempty"`    // the handoff note itself (for --resume)
}

// sessWALMax caps the result kept in a tool_result entry.
const sessWALMax = 64 << 10

func sessNewID(seen func(string) bool) string {
	for {
		b := make([]byte, 4)
		_, _ = rand.Read(b)
		id := hex.EncodeToString(b)
		if seen == nil || !seen(id) {
			return id
		}
	}
}

func projectSlug(root string) string {
	s := root
	if runtime.GOOS == "windows" {
		// "C:\\x" -> "C-x": ':' is not valid inside a Windows path element
		s = strings.ReplaceAll(strings.ReplaceAll(s, ":", ""), "/", "-")
	}
	s = strings.Trim(strings.ReplaceAll(s, string(filepath.Separator), "-"), "-")
	if s == "" {
		s = "root"
	}
	if len(s) > 80 {
		s = s[len(s)-80:]
	}
	return s
}

func sessionsDir(root string) string {
	return filepath.Join(dataDir(), "sessions", projectSlug(root))
}

func NewSession(root, model, system string) (*Session, error) {
	return NewSessionFrom(root, model, system, "")
}

// NewSessionFrom creates a session whose meta records parent (a /fork).
func NewSessionFrom(root, model, system, parent string) (*Session, error) {
	d := sessionsDir(root)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return nil, err
	}
	rb := make([]byte, 2)
	_, _ = rand.Read(rb)
	id := time.Now().Format("060102-150405") + "-" + hex.EncodeToString(rb)
	p := filepath.Join(d, id+".jsonl")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	s := &Session{ID: id, Path: p, f: f}
	s.write(sessEntry{T: "meta", V: 2, Time: time.Now().Unix(), Cwd: root, Model: model, System: system, Parent: parent})
	return s, nil
}

// sessSubDir holds the sessions of the subagents a session started. It is
// not a .jsonl file, so ListSessions, -c and /resume never pick them.
func sessSubDir(parentPath string) string {
	return strings.TrimSuffix(parentPath, ".jsonl") + ".sub"
}

// NewSubSession creates the session of a subagent next to its parent's
// (<parent>.sub/<parent id>.<sub>-<rand>.jsonl): meta records the parent
// session, the entry it branched from and the subagent id.
func NewSubSession(parent *Session, model, system, cwd, sub string) (*Session, error) {
	if parent == nil || parent.Path == "" {
		return nil, fmt.Errorf("no parent session")
	}
	d := sessSubDir(parent.Path)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return nil, err
	}
	rb := make([]byte, 2)
	_, _ = rand.Read(rb)
	id := parent.ID + "." + sub + "-" + hex.EncodeToString(rb)
	p := filepath.Join(d, id+".jsonl")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	s := &Session{ID: id, Path: p, f: f}
	s.write(sessEntry{T: "meta", V: 2, Time: time.Now().Unix(), Cwd: cwd, Model: model, System: system, Parent: parent.ID, Sub: sub, ParentEntry: parent.Leaf()})
	return s, nil
}

// write appends one entry as a child of the current leaf and makes it the
// new leaf. The line goes straight to the kernel (no user-space buffer), so
// a crash of jazz never loses it; Sync is only called on an abnormal end.
func (s *Session) write(e sessEntry) string {
	if s == nil || s.f == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeLocked(e)
}

func (s *Session) writeLocked(e sessEntry) string {
	if e.ID == "" {
		e.ID = sessNewID(nil)
	}
	if e.T != "branch" {
		e.ParentID = s.leaf
	}
	if e.TS == 0 {
		e.TS = time.Now().UnixMilli()
	}
	b, _ := json.Marshal(e)
	b = h2SignLine(b) // HMAC trailer (hardening2.go)
	s.f.Write(append(b, '\n'))
	s.leaf = e.ID
	switch e.T {
	case "meta", "compact":
		s.base, s.msgIDs, s.unaligned = e.ID, nil, false
	case "msg":
		s.msgIDs = append(s.msgIDs, e.ID)
	}
	return e.ID
}

// Sync flushes the file to disk (abnormal end only: the Dell has a slow disk).
func (s *Session) Sync() {
	if s != nil && s.f != nil {
		_ = s.f.Sync()
	}
}

// Leaf is the id of the newest entry on the active branch.
func (s *Session) Leaf() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leaf
}

// ToolStart and ToolResult are the write-ahead records of a tool call.
func (s *Session) ToolStart(tc ToolCall, step int) {
	sum := sha256.Sum256([]byte(tc.Args))
	s.write(sessEntry{T: "tool_start", Tool: &sessTool{CallID: tc.ID, Name: tc.Name, Step: step, ArgsSHA: hex.EncodeToString(sum[:8])}})
}

func (s *Session) ToolResult(tc ToolCall, step int, out ToolOutput, d time.Duration) {
	txt := redactSecrets(out.Text)
	if len(txt) > sessWALMax {
		txt = truncateHeadTail(txt, sessWALMax)
	}
	s.write(sessEntry{T: "tool_result", Tool: &sessTool{CallID: tc.ID, Name: tc.Name, Step: step, Result: txt, IsErr: out.IsErr, DurMS: d.Milliseconds()}})
}

// End records how a headless run ended (obs.go).
func (s *Session) End(e sessEnd) { s.write(sessEntry{T: "end", End: &e}) }

// Append logs a message. Images go to dataDir()/blobs and only a reference
// ("blob:<sha>.<ext>" in Data) is written to the JSONL.
func (s *Session) Append(m Message) { s.AppendMeta(m, nil) }

// AppendMeta logs a message; for assistant messages meta says what produced it.
func (s *Session) AppendMeta(m Message, meta *sessMsgMeta) {
	if s == nil || s.f == nil {
		return
	}
	// bug 61: keys pasted by the user or printed by tools never reach the disk.
	m.Content, m.Reasoning = redactSecrets(m.Content), redactSecrets(m.Reasoning)
	if len(m.ToolCalls) > 0 {
		tcs := make([]ToolCall, len(m.ToolCalls))
		for i, tc := range m.ToolCalls {
			tc.Args = redactSecrets(tc.Args)
			tcs[i] = tc
		}
		m.ToolCalls = tcs
	}
	if len(m.PartialCalls) > 0 {
		tcs := make([]ToolCall, len(m.PartialCalls))
		for i, tc := range m.PartialCalls {
			tc.Args = redactSecrets(tc.Args)
			tcs[i] = tc
		}
		m.PartialCalls = tcs
	}
	if len(m.Images) > 0 {
		imgs := make([]ImagePart, len(m.Images))
		for i, im := range m.Images {
			imgs[i] = im
			if ref, err := saveBlob(im); err == nil {
				imgs[i].Data = ref
			}
		}
		m.Images = imgs
	}
	e := sessEntry{T: "msg", Msg: &m}
	if meta != nil && m.Role == "assistant" {
		e.Model, e.Provider, e.Stop, e.LatencyMS = meta.Model, meta.Provider, meta.Stop, meta.LatencyMS
		if meta.Usage != (Usage{}) {
			u := meta.Usage
			e.Usage = &u
		}
	}
	s.write(e)
}

// sessMsgMeta is what produced an assistant message (model, tokens, stop).
type sessMsgMeta struct {
	Model, Provider, Stop string
	Usage                 Usage
	LatencyMS             int64
}

func (s *Session) Model(model string) { s.write(sessEntry{T: "model", Model: model}) }

// Rewind records that the conversation was cut back to its first n messages.
// The leaf moves to the n-th message first, so the rewind entry (and what
// follows) start a new branch: the abandoned messages stay in the file.
func (s *Session) Rewind(n int) {
	if s == nil || s.f == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.unaligned && n >= 0 && n <= len(s.msgIDs) {
		if n == 0 {
			s.leaf = s.base
		} else {
			s.leaf = s.msgIDs[n-1]
		}
		s.msgIDs = s.msgIDs[:n]
	}
	s.writeLocked(sessEntry{T: "rewind", N: n})
}

func blobsDir() string { return filepath.Join(dataDir(), "blobs") }

var blobExt = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}

// saveBlob stores an image once (content-addressed) and returns its reference.
func saveBlob(im ImagePart) (string, error) {
	if strings.HasPrefix(im.Data, "blob:") {
		return im.Data, nil
	}
	raw, err := base64.StdEncoding.DecodeString(im.Data)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	ext := blobExt[im.Mime]
	if ext == "" {
		ext = ".bin"
	}
	name := hex.EncodeToString(sum[:16]) + ext
	p := filepath.Join(blobsDir(), name)
	if _, err := os.Stat(p); err != nil {
		if err := os.MkdirAll(blobsDir(), 0o700); err != nil {
			return "", err
		}
		if err := os.WriteFile(p, raw, 0o600); err != nil {
			return "", err
		}
	}
	return "blob:" + name, nil
}

// loadBlobs resolves blob references back to base64 data; missing blobs turn
// into a text note so the history stays usable.
func loadBlobs(m *Message) {
	if len(m.Images) == 0 {
		return
	}
	var keep []ImagePart
	missing := 0
	for _, im := range m.Images {
		if ref, ok := strings.CutPrefix(im.Data, "blob:"); ok {
			b, err := os.ReadFile(filepath.Join(blobsDir(), filepath.Base(ref)))
			if err != nil {
				missing++
				continue
			}
			im.Data = base64.StdEncoding.EncodeToString(b)
		}
		keep = append(keep, im)
	}
	m.Images = keep
	if missing > 0 {
		m.Content = strings.TrimSpace(m.Content + fmt.Sprintf("\n[%d image(s) no longer available]", missing))
	}
}
func (s *Session) Compact(summary string, kept []Message) {
	s.write(sessEntry{T: "compact", Summary: redactSecrets(summary)})
	for _, m := range kept {
		s.Append(m)
	}
}
func (s *Session) Close() {
	if s != nil && s.f != nil {
		s.f.Close()
	}
}

type SessionInfo struct {
	ID, Path, Title, Model string
	Last                   string // the last answer, one line (pickers)
	Mod                    time.Time
	Msgs                   int
	Cwd                    string // project dir from the meta entry
	Usage                  Usage  // summed over the assistant replies in the file
	Tools                  int    // tool calls on the active messages
}

func ListSessions(root string) []SessionInfo { return sessListDir(sessionsDir(root)) }

// ListAllSessions lists the sessions of every project, newest first.
func ListAllSessions() []SessionInfo {
	base := filepath.Join(dataDir(), "sessions")
	dirs, _ := os.ReadDir(base)
	var out []SessionInfo
	for _, d := range dirs {
		if d.IsDir() {
			out = append(out, sessListDir(filepath.Join(base, d.Name()))...)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Mod.After(out[j].Mod) })
	return out
}

func sessListDir(d string) []SessionInfo {
	ents, _ := os.ReadDir(d)
	var out []SessionInfo
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".jsonl") || strings.HasSuffix(e.Name(), obEventsSuffix) {
			continue
		}
		p := filepath.Join(d, e.Name())
		fi, err := e.Info()
		if err != nil {
			continue
		}
		info := SessionInfo{ID: strings.TrimSuffix(e.Name(), ".jsonl"), Path: p, Mod: fi.ModTime()}
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		first, named := "", ""
		for sc.Scan() {
			var se sessEntry
			if json.Unmarshal(sc.Bytes(), &se) != nil {
				continue
			}
			if se.T == "meta" || se.T == "model" {
				info.Model = se.Model
			}
			if se.T == "meta" && info.Cwd == "" {
				info.Cwd = se.Cwd
			}
			if se.T == "title" && strings.TrimSpace(se.Title) != "" {
				named = se.Title // the newest one wins (/rename)
			}
			if se.T == "rewind" {
				info.Msgs = min(info.Msgs, se.N)
			}
			if se.T == "msg" && se.Msg != nil {
				info.Msgs++
				info.Tools += len(se.Msg.ToolCalls)
				if se.Usage != nil {
					info.Usage.Add(*se.Usage)
				}
				if first == "" && se.Msg.Role == "user" && !strings.HasPrefix(se.Msg.Content, "<summary>") {
					// harness reminders lead the first message: the title is the user's text
					first = se.Msg.Content
				}
				if se.Msg.Role == "assistant" && strings.TrimSpace(se.Msg.Content) != "" {
					info.Last = oneLine(se.Msg.Content, 100)
				}
			}
		}
		f.Close()
		info.Title = oneLine(sanitizeTerm(named), 70)
		if info.Title == "" {
			info.Title = sessAutoTitle(first) // sessions from before titles: same rule, never stored
		}
		if info.Msgs > 0 {
			out = append(out, info)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mod.After(out[j].Mod) })
	return out
}

// sessReadEntries parses a session file, tolerating a torn last line, and
// migrates v1 entries (no id) in place: each gets a stable synthetic id from
// its line number and the previous entry as parent (pi's v1->v2 migration).
func sessReadEntries(path string) ([]sessEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []sessEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	line, prev := 0, ""
	ver := newH2Verifier() // HMAC check per line (hardening2.go)
	for sc.Scan() {
		line++
		var se sessEntry
		if json.Unmarshal(sc.Bytes(), &se) != nil {
			continue // tolerate a torn line after a crash
		}
		se.Tampered, se.Legacy = ver.check(sc.Bytes())
		if se.ID == "" {
			se.ID = fmt.Sprintf("L%d", line)
			if se.T != "meta" {
				se.ParentID = prev
			}
		}
		prev = se.ID
		out = append(out, se)
	}
	return out, nil
}

// sessPath returns the entries from the root to leaf ("" = last entry), in
// order. An unknown leaf or a broken parent chain falls back to the file order.
func sessPath(ents []sessEntry, leaf string) []sessEntry {
	if len(ents) == 0 {
		return nil
	}
	if leaf == "" {
		leaf = ents[len(ents)-1].ID
	}
	byID := make(map[string]int, len(ents))
	for i, e := range ents {
		byID[e.ID] = i
	}
	i, ok := byID[leaf]
	if !ok {
		return ents
	}
	var rev []sessEntry
	seen := map[string]bool{}
	for {
		e := ents[i]
		if seen[e.ID] {
			return ents // cycle: never trust it
		}
		seen[e.ID] = true
		rev = append(rev, e)
		if e.ParentID == "" {
			break
		}
		j, ok := byID[e.ParentID]
		if !ok {
			break // parent lost (torn line): the path starts here
		}
		i = j
	}
	out := make([]sessEntry, len(rev))
	for k := range rev {
		out[k] = rev[len(rev)-1-k]
	}
	// the meta entry is always first, even when a branch made a new root
	if out[0].T != "meta" && ents[0].T == "meta" {
		out = append([]sessEntry{ents[0]}, out...)
	}
	return out
}

// sessReplay rebuilds the live messages along a path (honouring compactions
// and rewinds) and the ids of those messages.
type sessReplayed struct {
	msgs          []Message
	ids           []string
	base          string
	model, system string
	wal           map[string]sessTool // tool_result by call id
	end           *sessEnd            // last end record on the path
}

func sessReplay(path []sessEntry) sessReplayed {
	r := sessReplayed{wal: map[string]sessTool{}}
	anyTampered := false
	for _, se := range path {
		if se.Tampered { // altered outside jazz: only messages survive, as untrusted data (hardening2.go)
			anyTampered = true
			if se.T == "msg" && se.Msg != nil {
				r.msgs = append(r.msgs, h2UntrustedMsg(*se.Msg))
				r.ids = append(r.ids, se.ID)
			}
			continue
		}
		switch se.T {
		case "meta":
			r.model, r.system, r.base = se.Model, se.System, se.ID
		case "model":
			r.model = se.Model
		case "compact":
			r.msgs, r.ids, r.base = nil, nil, se.ID
		case "rewind":
			if se.N >= 0 && se.N <= len(r.msgs) {
				r.msgs, r.ids = r.msgs[:se.N], r.ids[:se.N]
			}
		case "msg":
			if se.Msg != nil {
				loadBlobs(se.Msg)
				r.msgs = append(r.msgs, *se.Msg)
				r.ids = append(r.ids, se.ID)
			}
		case "tool_result":
			if se.Tool != nil {
				r.wal[se.Tool.CallID] = *se.Tool
			}
		case "end":
			r.end = se.End
		}
	}
	if anyTampered {
		r.msgs, r.ids = h2DropOrphanTools(r.msgs, r.ids)
	}
	return r
}

// LoadSession rebuilds the live message list (honouring compactions) and reopens for append.
func LoadSession(path string) (*Session, []Message, string, string, error) {
	return LoadSessionAt(path, "")
}

// LoadSessionAt is LoadSession with the leaf moved to entry at ("" = the
// newest entry). Moving the leaf writes a branch entry, so the choice
// survives a restart and the next message starts a new branch there.
func LoadSessionAt(path, at string) (*Session, []Message, string, string, error) {
	ents, err := sessReadEntries(path)
	if err != nil {
		return nil, nil, "", "", err
	}
	if at != "" {
		found := false
		for _, e := range ents {
			if e.ID == at || (len(at) >= 4 && strings.HasPrefix(e.ID, at)) {
				at, found = e.ID, true
				break
			}
		}
		if !found {
			return nil, nil, "", "", fmt.Errorf("entrada %s não existe nesta sessão", at)
		}
	}
	r := sessReplay(sessPath(ents, at))
	msgs := repairHistoryWAL(r.msgs, r.wal)
	af, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, "", "", err
	}
	// a torn last line would swallow the next entry: start on a fresh line
	if fi, err := af.Stat(); err == nil && fi.Size() > 0 {
		last := make([]byte, 1)
		if _, err := af.ReadAt(last, fi.Size()-1); err == nil && last[0] != '\n' {
			af.Write([]byte{'\n'})
		}
	}
	id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	s := &Session{ID: id, Path: path, f: af, base: r.base}
	if len(ents) > 0 {
		s.leaf = ents[len(ents)-1].ID
	}
	// repaired tool results are not in the file: msgIDs only covers real entries
	if at != "" && at != s.leaf {
		s.mu.Lock()
		s.writeLocked(sessEntry{T: "branch", ParentID: at, Target: at})
		s.base = r.base
		s.mu.Unlock()
	}
	s.msgIDs = append([]string(nil), r.ids...)
	s.unaligned = len(msgs) != len(r.msgs)
	s.Tampered, s.Legacy = h2TamperStats(ents)
	for _, e := range ents {
		if e.T == "title" && !e.Tampered && strings.TrimSpace(e.Title) != "" {
			s.title = e.Title
		}
	}
	return s, msgs, r.model, r.system, nil
}

// sessLastEnd returns the end record on the active branch of a session file (nil = none).
func sessLastEnd(path string) *sessEnd {
	ents, err := sessReadEntries(path)
	if err != nil {
		return nil
	}
	return sessReplay(sessPath(ents, "")).end
}

// repairHistoryWAL is repairHistory that first uses the write-ahead result
// of a tool call that finished but whose tool message never got written.
func repairHistoryWAL(msgs []Message, wal map[string]sessTool) []Message {
	if len(wal) == 0 {
		return repairHistory(msgs)
	}
	var out []Message
	for i := 0; i < len(msgs); i++ {
		m := msgs[i]
		out = append(out, m)
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			continue
		}
		have := map[string]bool{}
		j := i + 1
		for ; j < len(msgs) && msgs[j].Role == "tool"; j++ {
			have[msgs[j].ToolCallID] = true
			out = append(out, msgs[j])
		}
		for _, tc := range m.ToolCalls {
			if w, ok := wal[tc.ID]; ok && !have[tc.ID] {
				out = append(out, Message{Role: "tool", ToolCallID: tc.ID, ToolName: tc.Name, Content: w.Result + "\n[recovered from the write-ahead log: the session ended before this result was delivered]"})
				have[tc.ID] = true
			}
		}
		i = j - 1
	}
	return repairHistory(out)
}

// repairHistory guarantees every assistant tool call has a result (crash safety).
func repairHistory(msgs []Message) []Message {
	var out []Message
	for i := 0; i < len(msgs); i++ {
		m := msgs[i]
		out = append(out, m)
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			continue
		}
		have := map[string]bool{}
		j := i + 1
		for ; j < len(msgs) && msgs[j].Role == "tool"; j++ {
			have[msgs[j].ToolCallID] = true
			out = append(out, msgs[j])
		}
		for _, tc := range m.ToolCalls {
			if !have[tc.ID] {
				out = append(out, Message{Role: "tool", ToolCallID: tc.ID, ToolName: tc.Name, Content: "TOOL_OUTCOME_UNKNOWN: the session ended while this tool was running. Check the current state before retrying."})
			}
		}
		i = j - 1
	}
	return out
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func fmtAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "agora"
	case d < time.Hour:
		return fmt.Sprintf("%dmin", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// ---------- export (G13) ----------

// acFence returns a backtick fence longer than any run inside s.
func acFence(s string) string {
	longest, run := 0, 0
	for _, r := range s {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return strings.Repeat("`", max(3, longest+1))
}

func acClip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return truncateHeadTail(s, n)
}

// exportMarkdown renders the conversation for sharing: user and assistant
// text, tool calls as fenced blocks and clipped tool output.
func exportMarkdown(id, model string, msgs []Message) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Sessão jazz %s\n\n", id)
	fmt.Fprintf(&b, "- modelo: `%s`\n- exportada em: %s\n\n", model, time.Now().Format("2006-01-02 15:04"))
	for _, m := range msgs {
		switch m.Role {
		case "user":
			t := m.Content
			if strings.HasPrefix(t, "<summary>") {
				b.WriteString("---\n\n_(resumo da conversa anterior)_\n\n")
				t = strings.TrimPrefix(t, "<summary>")
				if i := strings.Index(t, "</summary>"); i >= 0 {
					t = t[:i]
				}
				b.WriteString(strings.TrimSpace(t) + "\n\n---\n\n")
				continue
			}
			t = strings.TrimSpace(acStripReminders(t))
			if t == "" && len(m.Images) == 0 {
				continue
			}
			b.WriteString("## Você\n\n")
			if t != "" {
				b.WriteString(t + "\n\n")
			}
			if len(m.Images) > 0 {
				fmt.Fprintf(&b, "_[%d imagem(ns) anexada(s)]_\n\n", len(m.Images))
			}
		case "assistant":
			t := strings.TrimSpace(m.Content)
			if t == "" && len(m.ToolCalls) == 0 {
				continue
			}
			b.WriteString("## Jazz\n\n")
			if t != "" {
				b.WriteString(t + "\n\n")
			}
			for _, tc := range m.ToolCalls {
				body, lang := tc.Args, "json"
				if CanonicalTool(tc.Name) == "bash" {
					if c := acBashCmd(tc.Args); c != "" {
						body, lang = c, "bash"
					}
				} else {
					var v any
					if json.Unmarshal([]byte(tc.Args), &v) == nil {
						if pb, err := json.MarshalIndent(v, "", "  "); err == nil {
							body = string(pb)
						}
					}
				}
				body = acClip(body, 4000)
				f := acFence(body)
				fmt.Fprintf(&b, "**%s**\n\n%s%s\n%s\n%s\n\n", tc.Name, f, lang, body, f)
			}
		case "tool":
			out := acClip(strings.TrimRight(m.Content, "\n"), 2000)
			f := acFence(out)
			fmt.Fprintf(&b, "<details><summary>resultado de %s</summary>\n\n%s\n%s\n%s\n\n</details>\n\n", m.ToolName, f, out, f)
		}
	}
	return b.String()
}

// acBashCmd extracts the bash command from raw args ("" when absent).
func acBashCmd(raw string) string {
	m, _, err := parseArgs(raw)
	if err != nil {
		return ""
	}
	c, _ := argStr(normalizeArgs("bash", m), "command")
	return c
}

// exportHTML wraps the markdown in a minimal standalone page.
func exportHTML(id, md string) string {
	return `<!doctype html>
<html lang="pt-BR"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sessão jazz ` + html.EscapeString(id) + `</title>
<style>
:root{color-scheme:light dark;--bg:#fbfaf7;--fg:#1f1f1f}
@media (prefers-color-scheme:dark){:root{--bg:#161616;--fg:#e8e8e8}}
body{margin:0;background:var(--bg);color:var(--fg);font:15px/1.55 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
pre{white-space:pre-wrap;overflow-wrap:anywhere;max-width:100ch;margin:2rem auto;padding:0 16px}
</style></head>
<body><pre>` + html.EscapeString(md) + `</pre></body></html>
`
}

// sessTreeRow is one message of the session tree (for /tree).
type sessTreeRow struct {
	ID, Who, Text string
	Depth         int
	Active        bool // on the active path
	Leaf          bool // the newest message of the active path
}

// line is the row as /tree prints it without the picker.
func (r sessTreeRow) line() string {
	mark := "○"
	if r.Active {
		mark = "●"
	}
	line := fmt.Sprintf("%s%s %-8s %-4s %s", strings.Repeat("  ", r.Depth), mark, r.ID, r.Who, r.Text)
	if r.Leaf {
		line += "  ← atual"
	}
	return line
}

// sessTreeLines renders the message entries of a session as a tree (for
// /tree): branches indent, ● marks the active path, the leaf is flagged.
func sessTreeLines(path string) ([]string, error) {
	rows, err := sessTreeRows(path)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.line())
	}
	return out, nil
}

// sessTreeRows walks the session tree depth-first (user and assistant messages).
func sessTreeRows(path string) ([]sessTreeRow, error) {
	ents, err := sessReadEntries(path)
	if err != nil {
		return nil, err
	}
	onPath := map[string]bool{}
	active := sessPath(ents, "")
	for _, e := range active {
		onPath[e.ID] = true
	}
	leafMsg := ""
	for _, e := range active {
		if e.T == "msg" {
			leafMsg = e.ID
		}
	}
	kids := map[string][]int{}
	var roots []int
	ids := map[string]bool{}
	for _, e := range ents {
		ids[e.ID] = true
	}
	for i, e := range ents {
		if e.ParentID == "" || !ids[e.ParentID] {
			roots = append(roots, i)
		} else {
			kids[e.ParentID] = append(kids[e.ParentID], i)
		}
	}
	type item struct {
		i, depth int
	}
	var stack []item
	for k := len(roots) - 1; k >= 0; k-- {
		stack = append(stack, item{roots[k], 0})
	}
	var out []sessTreeRow
	for len(stack) > 0 {
		it := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		e := ents[it.i]
		if e.T == "msg" && e.Msg != nil && (e.Msg.Role == "user" || e.Msg.Role == "assistant") {
			text := strings.TrimSpace(acStripReminders(e.Msg.Content))
			if e.Msg.Role == "assistant" && len(e.Msg.ToolCalls) > 0 {
				var names []string
				for _, tc := range e.Msg.ToolCalls {
					names = append(names, tc.Name)
				}
				text = strings.TrimSpace(text + " [" + strings.Join(names, ", ") + "]")
			}
			who := "você"
			if e.Msg.Role == "assistant" {
				who = "jazz"
			}
			out = append(out, sessTreeRow{ID: e.ID, Who: who, Text: oneLine(text, 70), Depth: it.depth, Active: onPath[e.ID], Leaf: e.ID == leafMsg})
		}
		ch := kids[e.ID]
		d := it.depth
		if len(ch) > 1 {
			d++
		}
		for k := len(ch) - 1; k >= 0; k-- {
			stack = append(stack, item{ch[k], d})
		}
	}
	return out, nil
}
