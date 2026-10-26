package main

// Message queue: the user keeps typing while the agent works (Devin CLI,
// pi's steer/followUp). Enter during a turn never blocks and never interrupts:
// the message goes into a visible queue under the live region.
//
//   - steer ("agora"): injected at the next tool boundary of the current turn
//     as a user message with a clear marker, so the model adjusts course
//     without losing work. If the model is about to finish, the turn goes on
//     with it (same as the old steering, bug 39).
//   - follow-up ("depois"): runs as the next turn once the current one ends
//     (each turn keeps its own honest-done gate).
//
// Injections are user-role messages appended after the tool results: the
// system prompt is never touched, so the prompt cache holds. They go through
// Agent.append, which writes the session entry before the next request
// (write-ahead). Events: queue.add, queue.edit, queue.consume, queue.drop.
//
// Headless (-p): --follow-up "msg" (repeatable) queues follow-up turns, and
// JAZZ_STEER_FILE names a file whose lines are steer messages, read at every
// tool boundary (deterministic for evals: no watcher goroutine).

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

type qMode int

const (
	qSteer  qMode = iota // delivered at the next tool boundary
	qFollow              // delivered as the next turn
)

func (m qMode) String() string {
	if m == qFollow {
		return "follow-up"
	}
	return "steer"
}

// label is what the queue shows per item.
func (m qMode) label() string {
	if m == qFollow {
		return "depois"
	}
	return "agora"
}

// qSteerMax: messages up to this many characters steer by default.
const qSteerMax = 200

// qSteerMarker leads each steer message in the conversation.
const qSteerMarker = "[mensagem do usuário durante a execução]: "

type qItem struct {
	ID   int
	Text string
	Mode qMode
	Src  string // tui | flag | file
}

// MsgQueue holds the messages typed while the agent works. Safe for
// concurrent use (the key watcher adds, the agent consumes).
type MsgQueue struct {
	mu    sync.Mutex
	items []qItem
	seq   int
	ev    func(kind string, f map[string]any) // event sink (Agent.obEv)

	// outcome of the last turn (qTurnEnd): follow-ups start only after a
	// turn that was not interrupted and did not fail
	ran     bool
	stop    StopReason
	failed  bool
	steerFn string // JAZZ_STEER_FILE (headless)
	steerAt int64  // bytes of the steer file already read
}

// qFollowPrefixes force follow-up mode (and are stripped).
var qFollowPrefixes = []string{"depois:", "+"}

// qSteerWords force steer mode when they open the message (kept in the text).
var qSteerWords = []string{"na verdade", "espera", "opa", "ei"}

// qClassify picks the delivery mode of a typed message and returns the text
// without the mode prefix. Explicit follow-up prefixes win, then "!" and the
// interjections, then the length (≤ 200 characters steers).
func qClassify(s string) (qMode, string) {
	s = strings.TrimSpace(s)
	low := strings.ToLower(s)
	for _, p := range qFollowPrefixes {
		if strings.HasPrefix(low, p) {
			return qFollow, strings.TrimSpace(s[len(p):])
		}
	}
	if strings.HasPrefix(s, "!") {
		return qSteer, strings.TrimSpace(s[1:])
	}
	for _, w := range qSteerWords {
		if strings.HasPrefix(low, w) {
			r, _ := utf8.DecodeRuneInString(low[len(w):])
			if len(low) == len(w) || !unicode.IsLetter(r) {
				return qSteer, s
			}
		}
	}
	if utf8.RuneCountInString(s) <= qSteerMax {
		return qSteer, s
	}
	return qFollow, s
}

func (q *MsgQueue) emit(kind string, f map[string]any) {
	if q.ev != nil {
		q.ev(kind, f)
	}
}

// qPreview is the redacted one-line form of a message for events.
func qPreview(s string) string { return oneLine(redactSecrets(s), 120) }

// Add classifies text and queues it. An empty message is ignored.
func (q *MsgQueue) Add(text, src string) (qItem, bool) {
	m, t := qClassify(text)
	if t == "" {
		return qItem{}, false
	}
	return q.AddMode(t, m, src), true
}

// AddMode queues text with an explicit mode.
func (q *MsgQueue) AddMode(text string, m qMode, src string) qItem {
	q.mu.Lock()
	q.seq++
	it := qItem{ID: q.seq, Text: text, Mode: m, Src: src}
	q.items = append(q.items, it)
	n := len(q.items)
	q.mu.Unlock()
	q.emit("queue.add", map[string]any{"id": it.ID, "mode": m.String(), "src": src, "chars": utf8.RuneCountInString(text), "pending": n, "text": qPreview(text)})
	return it
}

// Items returns a copy of the pending messages, oldest first.
func (q *MsgQueue) Items() []qItem {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]qItem(nil), q.items...)
}

func (q *MsgQueue) Len() int {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

func (q *MsgQueue) find(id int) int {
	for i, it := range q.items {
		if it.ID == id {
			return i
		}
	}
	return -1
}

// Edit replaces the text of a pending item; false if it was already consumed.
func (q *MsgQueue) Edit(id int, text string) bool {
	q.mu.Lock()
	i := q.find(id)
	if i >= 0 {
		q.items[i].Text = text
	}
	q.mu.Unlock()
	if i >= 0 {
		q.emit("queue.edit", map[string]any{"id": id, "chars": utf8.RuneCountInString(text), "text": qPreview(text)})
	}
	return i >= 0
}

// Toggle switches a pending item between steer and follow-up.
func (q *MsgQueue) Toggle(id int) (qMode, bool) {
	q.mu.Lock()
	i := q.find(id)
	var m qMode
	if i >= 0 {
		m = qSteer
		if q.items[i].Mode == qSteer {
			m = qFollow
		}
		q.items[i].Mode = m
	}
	q.mu.Unlock()
	if i >= 0 {
		q.emit("queue.edit", map[string]any{"id": id, "mode": m.String()})
	}
	return m, i >= 0
}

// Remove drops a pending item before it is consumed.
func (q *MsgQueue) Remove(id int, reason string) bool {
	q.mu.Lock()
	i := q.find(id)
	var it qItem
	if i >= 0 {
		it = q.items[i]
		q.items = append(q.items[:i:i], q.items[i+1:]...)
	}
	q.mu.Unlock()
	if i >= 0 {
		q.emit("queue.drop", map[string]any{"id": id, "mode": it.Mode.String(), "reason": reason, "text": qPreview(it.Text)})
	}
	return i >= 0
}

// Drain removes every pending item (reason goes to the queue.drop events).
func (q *MsgQueue) Drain(reason string) []qItem {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	all := q.items
	q.items = nil
	q.mu.Unlock()
	for _, it := range all {
		q.emit("queue.drop", map[string]any{"id": it.ID, "mode": it.Mode.String(), "reason": reason, "text": qPreview(it.Text)})
	}
	return all
}

// takeSteer removes and returns the steer items (queue order kept).
func (q *MsgQueue) takeSteer() []qItem {
	q.mu.Lock()
	defer q.mu.Unlock()
	var got, keep []qItem
	for _, it := range q.items {
		if it.Mode == qSteer {
			got = append(got, it)
		} else {
			keep = append(keep, it)
		}
	}
	q.items = keep
	return got
}

// pop removes the oldest item (any mode): what runs as the next turn.
func (q *MsgQueue) pop() (qItem, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return qItem{}, false
	}
	it := q.items[0]
	q.items = q.items[1:]
	return it, true
}

// pollSteerFile queues the complete new lines of the steer file as steer
// messages. A line still being written (no newline yet) waits for the next poll.
func (q *MsgQueue) pollSteerFile() {
	q.mu.Lock()
	fn, at := q.steerFn, q.steerAt
	q.mu.Unlock()
	if fn == "" {
		return
	}
	f, err := os.Open(fn)
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Seek(at, io.SeekStart); err != nil {
		return
	}
	r := bufio.NewReader(io.LimitReader(f, 1<<20))
	var lines []string
	for {
		l, err := r.ReadString('\n')
		if err != nil {
			break // EOF: a partial line is left for later
		}
		at += int64(len(l))
		if s := strings.TrimSpace(l); s != "" {
			lines = append(lines, s)
		}
	}
	q.mu.Lock()
	q.steerAt = at
	q.mu.Unlock()
	for _, s := range lines {
		q.AddMode(s, qSteer, "file")
	}
}

// ---------- agent side ----------

// Queue returns the agent's message queue (created on first use).
func (a *Agent) Queue() *MsgQueue {
	a.steerMu.Lock()
	defer a.steerMu.Unlock()
	if a.mq == nil {
		a.mq = &MsgQueue{ev: a.obEv}
	}
	return a.mq
}

func (a *Agent) queue() *MsgQueue {
	a.steerMu.Lock()
	defer a.steerMu.Unlock()
	return a.mq
}

// qSteerMessage is the user message that carries steer items into the turn.
func qSteerMessage(items []qItem) string {
	var b strings.Builder
	b.WriteString("<system-reminder>The user typed this while you were working. Take it into account now and adjust course; keep the work already done unless the message says otherwise.</system-reminder>")
	for _, it := range items {
		b.WriteString("\n" + qSteerMarker + it.Text)
	}
	return b.String()
}

// qInject delivers the pending steer messages at a tool boundary ("tool") or
// when the model is about to finish ("final"). It reports whether a message
// was appended (the caller then continues the turn).
func (a *Agent) qInject(where string) bool {
	q := a.queue()
	if q == nil || a.sub {
		return false
	}
	q.pollSteerFile()
	items := q.takeSteer()
	if len(items) == 0 {
		return false
	}
	a.append(Message{Role: "user", Content: qSteerMessage(items)}) // session entry before the next request
	for _, it := range items {
		q.emit("queue.consume", map[string]any{"id": it.ID, "mode": it.Mode.String(), "at": where, "src": it.Src, "text": qPreview(it.Text)})
		a.ui.Notice("↳ entregue agora: " + oneLine(it.Text, 100))
	}
	return true
}

// qTurnEnd records how the turn ended (called from Run's defer).
func (a *Agent) qTurnEnd(stop StopReason, err error) {
	q := a.queue()
	if q == nil || a.sub {
		return
	}
	q.mu.Lock()
	q.ran, q.stop, q.failed = true, stop, err != nil
	q.mu.Unlock()
}

// qNextTurn pops the message that runs as the next turn, if the last turn
// ended normally. A steer that arrived after the final check runs as a turn
// too (nothing is lost).
func (a *Agent) qNextTurn() (qItem, bool) {
	q := a.queue()
	if q == nil {
		return qItem{}, false
	}
	q.pollSteerFile()
	q.mu.Lock()
	ok := !q.failed && q.stop != StopInterrupted
	q.mu.Unlock()
	if !ok {
		return qItem{}, false
	}
	it, got := q.pop()
	if got {
		q.emit("queue.consume", map[string]any{"id": it.ID, "mode": it.Mode.String(), "at": "turn", "src": it.Src, "text": qPreview(it.Text)})
	}
	return it, got
}

// ---------- headless ----------

// qFollowFlag collects --follow-up (repeatable).
type qFlagList []string

func (l *qFlagList) String() string { return strings.Join(*l, " | ") }
func (l *qFlagList) Set(v string) error {
	*l = append(*l, v)
	return nil
}

var qFollowFlag qFlagList

func init() { opValuedFlags["--follow-up"], opValuedFlags["-follow-up"] = true, true }

// qRunHeadless runs the prompt, then each queued follow-up as its own turn
// while the previous one ended well (StopDone).
func (a *Agent) qRunHeadless(ctx context.Context, prompt string) (StopReason, error) {
	q := a.Queue()
	q.mu.Lock()
	q.steerFn, q.steerAt = os.Getenv("JAZZ_STEER_FILE"), 0
	q.mu.Unlock()
	for _, f := range qFollowFlag {
		if s := strings.TrimSpace(f); s != "" {
			q.AddMode(s, qFollow, "flag")
		}
	}
	stop, err := a.Run(ctx, prompt)
	for err == nil && ctx.Err() == nil && (stop == StopDone || stop == "") {
		it, ok := a.qNextTurn()
		if !ok {
			break
		}
		a.ui.Notice("depois: " + oneLine(it.Text, 100))
		stop, err = a.Run(ctx, it.Text)
	}
	if n := q.Len(); n > 0 {
		q.Drain("fim da execução")
		a.ui.Warn(fmt.Sprintf("%d mensagem(ns) da fila não rodaram (o turno anterior terminou com %s)", n, stop))
	}
	return stop, err
}

// ---------- interactive (App) ----------

// qSend wires the queue into a send: the key watcher feeds it, and when the
// turn is over the returned func starts the next queued message as a turn,
// or, after an interruption or an error, puts the queue back in the editor.
func (a *App) qSend() func() {
	q := a.agent.Queue()
	q.mu.Lock()
	q.ran, q.stop, q.failed = false, "", false
	q.mu.Unlock()
	a.ui.qAttach(q)
	return func() {
		if q.Len() == 0 {
			return
		}
		if it, ok := a.agent.qNextTurn(); ok {
			a.ui.Notice(a.ui.T.G().arrow + " depois: " + oneLine(it.Text, 100))
			a.send(it.Text)
			return
		}
		var texts []string
		for _, it := range q.Drain("editor") {
			texts = append(texts, it.Text)
		}
		if len(texts) > 0 {
			s := strings.TrimSpace(strings.TrimSpace(a.ed.prefill) + "\n\n" + strings.Join(texts, "\n\n"))
			a.ed.Prefill(s)
			a.ui.Notice(fmt.Sprintf("%d mensagem(ns) da fila voltaram pro editor", len(texts)))
		}
	}
}

// ---------- terminal UI ----------

// qView is the queue as the terminal shows and edits it. It lives outside
// TermUI (keyed by it) so the UI struct stays untouched.
type qView struct {
	mu      sync.Mutex
	q       *MsgQueue
	sel     int    // ID of the item being edited (0 = none)
	draft   []rune // what was typed before Up entered the queue
	lastEsc time.Time
}

var qViews sync.Map // *TermUI -> *qView

// qEscEsc: two Esc within this window clear the queue.
var qEscEsc = 800 * time.Millisecond

func (u *TermUI) qAttach(q *MsgQueue) {
	v := u.qview()
	if v == nil {
		v = &qView{}
		qViews.Store(u, v)
	}
	v.mu.Lock()
	v.q, v.sel, v.draft = q, 0, nil
	v.mu.Unlock()
}

func (u *TermUI) qview() *qView {
	if v, ok := qViews.Load(u); ok {
		return v.(*qView)
	}
	return nil
}

// qKey handles the queue keys of WatchTurn: Enter queues (or saves the item
// being edited), Up/Down walk the queue, Delete removes, Tab toggles
// agora/depois, Esc Esc clears. It returns the new typed buffer and whether
// the key was consumed (a first Esc is not: it still cancels the request).
func (u *TermUI) qKey(k Key, buf []rune, on bool) ([]rune, bool) {
	v := u.qview()
	if v == nil || !on {
		return buf, false
	}
	v.mu.Lock()
	q := v.q
	if q == nil {
		v.mu.Unlock()
		return buf, false
	}
	items := q.Items()
	idx := -1
	for i, it := range items {
		if it.ID == v.sel {
			idx = i
		}
	}
	if idx < 0 && v.sel != 0 { // the item was consumed meanwhile
		v.sel = 0
	}
	leave := func() []rune {
		d := v.draft
		v.sel, v.draft = 0, nil
		return d
	}
	var notice string
	handled := true
	switch k.Code {
	case KEnter:
		s := strings.TrimSpace(string(buf))
		switch {
		case v.sel != 0 && s == "":
			buf = leave()
		case v.sel != 0 && q.Edit(v.sel, s):
			buf = leave()
		case s != "":
			if v.sel != 0 {
				buf = leave()
			} else {
				buf = nil
			}
			q.Add(s, "tui")
		}
	case KUp:
		if len(items) == 0 {
			handled = false
			break
		}
		switch {
		case v.sel == 0:
			v.draft, idx = buf, len(items)-1
		case idx > 0:
			idx--
		}
		v.sel, buf = items[idx].ID, []rune(items[idx].Text)
	case KDown:
		if v.sel == 0 {
			handled = false
			break
		}
		if idx+1 < len(items) {
			v.sel, buf = items[idx+1].ID, []rune(items[idx+1].Text)
		} else {
			buf = leave()
		}
	case KDelete:
		if v.sel == 0 {
			handled = false
			break
		}
		q.Remove(v.sel, "delete")
		rest := q.Items()
		if len(rest) == 0 {
			buf = leave()
			break
		}
		idx = min(idx, len(rest)-1)
		v.sel, buf = rest[idx].ID, []rune(rest[idx].Text)
	case KTab:
		id := v.sel
		if id == 0 && len(items) > 0 {
			id = items[len(items)-1].ID
		}
		if id == 0 {
			handled = false
			break
		}
		q.Toggle(id)
	case KEsc:
		if time.Since(v.lastEsc) < qEscEsc && len(items) > 0 {
			n := len(q.Drain("esc-esc"))
			v.lastEsc = time.Time{}
			buf = leave()
			notice = fmt.Sprintf("fila limpa (%d mensagem(ns) descartada(s))", n)
			break
		}
		v.lastEsc = time.Now()
		if v.sel != 0 {
			buf = leave()
		}
		handled = false // the first Esc still cancels the request
	default:
		handled = false
	}
	v.mu.Unlock()
	if notice != "" {
		u.Notice(notice)
	}
	return buf, handled
}

// qLiveMax is how many queued items the live region lists.
const qLiveMax = 5

// qLive renders the queue under the status line: the text to append to the
// status (each line starts with a newline) and how many rows it adds. u.mu
// is held by the caller.
func (u *TermUI) qLive(w int) (string, int) {
	v := u.qview()
	if v == nil {
		return "", 0
	}
	v.mu.Lock()
	q, sel := v.q, v.sel
	v.mu.Unlock()
	items := q.Items()
	if len(items) == 0 {
		return "", 0
	}
	t, g := u.T, u.T.G()
	gray, gold, rst := t.fg(cGray, 90), t.fg(cGold, 33), t.sgr("0")
	lines := []string{gray + fmt.Sprintf("  na fila (%d): %s%s editar %s del remove %s tab agora/depois %s esc esc limpa", len(items), g.up, g.down, g.dot, g.dot, g.dot) + rst}
	for i, it := range items {
		if i == qLiveMax {
			lines = append(lines, gray+fmt.Sprintf("     +%d", len(items)-qLiveMax)+rst)
			break
		}
		mark := "  "
		if it.ID == sel {
			mark = gold + g.ptr + " " + rst
		}
		lab := gray + "[" + it.Mode.label() + "]" + rst
		if it.Mode == qSteer {
			lab = gold + "[" + it.Mode.label() + "]" + rst
		}
		lines = append(lines, "   "+mark+lab+" "+sanitizeTerm(oneLine(it.Text, max(w, 20))))
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("\x1b[0m\x1b[K\r\n" + clipVisible(l, w-1) + rst)
	}
	return b.String(), len(lines)
}

// qTraceLine is how `jazz trace` shows the queue events.
func qTraceLine(e obEvent) string {
	mode := map[string]string{"steer": "agora", "follow-up": "depois"}[e.str("mode")]
	txt := oneLine(e.str("text"), 80)
	switch e.str("kind") {
	case "queue.add":
		return fmt.Sprintf("fila + #%d [%s] (%s): %s", e.int("id"), mode, e.str("src"), txt)
	case "queue.edit":
		if mode != "" {
			return fmt.Sprintf("fila ~ #%d agora é [%s]", e.int("id"), mode)
		}
		return fmt.Sprintf("fila ~ #%d editada: %s", e.int("id"), txt)
	case "queue.consume":
		at := map[string]string{"tool": "na fronteira de ferramenta", "final": "antes de terminar o turno", "turn": "como turno novo"}[e.str("at")]
		return fmt.Sprintf("fila → #%d [%s] entregue %s: %s", e.int("id"), mode, at, txt)
	case "queue.drop":
		return fmt.Sprintf("fila − #%d [%s] descartada (%s): %s", e.int("id"), mode, e.str("reason"), txt)
	}
	return ""
}
