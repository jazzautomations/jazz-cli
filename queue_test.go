package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQueueClassify(t *testing.T) {
	long := strings.Repeat("x", qSteerMax+1)
	cases := []struct {
		in   string
		mode qMode
		text string
	}{
		{"usa a pasta src", qSteer, "usa a pasta src"},
		{strings.Repeat("y", qSteerMax), qSteer, strings.Repeat("y", qSteerMax)},
		{long, qFollow, long},
		{"depois: escreve o changelog", qFollow, "escreve o changelog"},
		{"Depois:roda os testes", qFollow, "roda os testes"},
		{"+ e atualiza o README", qFollow, "e atualiza o README"},
		{"!" + long, qSteer, long},
		{"ei " + long, qSteer, "ei " + long},
		{"Opa, " + long, qSteer, "Opa, " + long},
		{"na verdade " + long, qSteer, "na verdade " + long},
		{"espera" + strings.Repeat("!", qSteerMax), qSteer, "espera" + strings.Repeat("!", qSteerMax)},
		{"eita " + long, qFollow, "eita " + long}, // "ei" only as a word
		{"esperançoso " + long, qFollow, "esperançoso " + long},
		{"depois: !" + long, qFollow, "!" + long}, // explicit follow-up wins
		{"   ", qSteer, ""},
	}
	for _, c := range cases {
		m, txt := qClassify(c.in)
		if m != c.mode || txt != c.text {
			t.Errorf("qClassify(%.30q) = %v %.30q, want %v %.30q", c.in, m, txt, c.mode, c.text)
		}
	}
}

func TestQueueOps(t *testing.T) {
	var evs []string
	q := &MsgQueue{ev: func(k string, f map[string]any) { evs = append(evs, k) }}
	if _, ok := q.Add("  ", "tui"); ok {
		t.Error("empty message queued")
	}
	a, _ := q.Add("primeira", "tui")
	b, _ := q.Add("depois: segunda", "tui")
	c, _ := q.Add("terceira", "tui")
	if b.Mode != qFollow || b.Text != "segunda" {
		t.Errorf("b = %+v", b)
	}
	if !q.Edit(a.ID, "primeira editada") || q.Items()[0].Text != "primeira editada" {
		t.Error("edit failed")
	}
	if m, ok := q.Toggle(c.ID); !ok || m != qFollow {
		t.Error("toggle failed")
	}
	if !q.Remove(b.ID, "delete") || q.Remove(b.ID, "delete") {
		t.Error("remove must work once")
	}
	st := q.takeSteer()
	if len(st) != 1 || st[0].ID != a.ID {
		t.Errorf("takeSteer = %+v", st)
	}
	if it, ok := q.pop(); !ok || it.ID != c.ID {
		t.Errorf("pop = %+v %v", it, ok)
	}
	if q.Edit(a.ID, "x") {
		t.Error("edit of a consumed item must fail")
	}
	q.Add("x", "tui")
	q.Add("y", "tui")
	if n := len(q.Drain("esc-esc")); n != 2 || q.Len() != 0 {
		t.Errorf("drain = %d, left %d", n, q.Len())
	}
	want := "queue.add queue.add queue.add queue.edit queue.edit queue.drop queue.add queue.add queue.drop queue.drop"
	if got := strings.Join(evs, " "); got != want {
		t.Errorf("events:\n got %s\nwant %s", got, want)
	}
}

// qSessUserMsgs returns the user messages written to the session file.
func qSessUserMsgs(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var e sessEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.T == "msg" && e.Msg != nil && e.Msg.Role == "user" {
			out = append(out, e.Msg.Content)
		}
	}
	return out
}

// qEvents reads the kinds (and fields) of the queue events of a session.
func qEvents(t *testing.T, sessPath string) []obEvent {
	t.Helper()
	obCloseCur()
	evs, err := obReadEvents(obEventsPath(sessPath))
	if err != nil {
		t.Fatal(err)
	}
	var out []obEvent
	for _, e := range evs {
		if strings.HasPrefix(e.str("kind"), "queue.") {
			out = append(out, e)
		}
	}
	return out
}

// A steer typed while the first of two tools runs reaches the model at the
// next tool boundary, as a user message after the tool result, and is in the
// session file before that request goes out.
func TestQueueSteerAtToolBoundary(t *testing.T) {
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "k1", Name: "bash", Args: `{"command":"echo um"}`}}},
		fakeResp{Calls: []fakeCall{{ID: "k2", Name: "bash", Args: `{"command":"echo dois"}`}}},
		fakeResp{Text: "feito"},
	), nil)
	q := h.ag.Queue()
	n := 0
	h.ui.onToolStart = func(name, detail string) {
		if n++; n == 1 {
			q.Add("usa a pasta src em vez de lib", "tui")
			q.Add("depois: escreve o changelog", "tui")
		}
	}
	if err := h.run(t, "faz a tarefa"); err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	if len(reqs) != 3 {
		t.Fatalf("requests = %d", len(reqs))
	}
	r := reqs[1]
	m := len(r.Messages)
	last, prev := r.Messages[m-1], r.Messages[m-2]
	c, _ := last["content"].(string)
	if last["role"] != "user" || !strings.Contains(c, qSteerMarker+"usa a pasta src em vez de lib") {
		t.Errorf("steer message = %v", last)
	}
	if prev["role"] != "tool" || prev["tool_call_id"] != "k1" {
		t.Errorf("steer must follow the k1 result: %v", prev)
	}
	if strings.Contains(c, "changelog") {
		t.Error("the follow-up leaked into the turn")
	}
	if reqs[0].system() != reqs[2].system() {
		t.Error("system prompt changed")
	}
	if got := strings.Count(fmtReq(reqs[2]), qSteerMarker); got != 1 {
		t.Errorf("steer delivered %d times", got)
	}
	// the follow-up waits for the next turn
	if it := q.Items(); len(it) != 1 || it[0].Mode != qFollow {
		t.Errorf("queue after turn = %+v", it)
	}
	// persisted as a user entry (write-ahead)
	found := false
	for _, u := range qSessUserMsgs(t, h.ag.sess.Path) {
		if strings.Contains(u, qSteerMarker+"usa a pasta src") {
			found = true
		}
	}
	if !found {
		t.Error("steer not in the session file")
	}
	if !strings.Contains(qNotices(h.ui), "entregue agora") {
		t.Errorf("no delivery notice: %v", qNotices(h.ui))
	}
	var kinds []string
	for _, e := range qEvents(t, h.ag.sess.Path) {
		kinds = append(kinds, e.str("kind")+":"+e.str("mode")+":"+e.str("at"))
	}
	if got := strings.Join(kinds, " "); got != "queue.add:steer: queue.add:follow-up: queue.consume:steer:tool" {
		t.Errorf("events = %s", got)
	}
}

func qNotices(u *recUI) string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return strings.Join(u.notices, "\n")
}

func fmtReq(r recReq) string {
	b, _ := json.Marshal(r.Messages)
	return string(b)
}

// Follow-ups run as their own turns after a turn that ended well; after a
// failed turn they do not run (and are reported).
func TestQueueFollowUpHeadless(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp {
		return fakeResp{Text: "resposta " + string(rune('A'+n))}
	}, nil)
	old := qFollowFlag
	t.Cleanup(func() { qFollowFlag = old })
	qFollowFlag = qFlagList{"segundo pedido", "terceiro pedido"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stop, err := h.ag.qRunHeadless(ctx, "primeiro pedido")
	if err != nil || stop != StopDone {
		t.Fatalf("stop=%s err=%v", stop, err)
	}
	reqs := h.fs.requests()
	if len(reqs) != 3 {
		t.Fatalf("requests = %d", len(reqs))
	}
	for i, want := range []string{"primeiro pedido", "segundo pedido", "terceiro pedido"} {
		if c, _ := reqs[i].last()["content"].(string); !strings.Contains(c, want) {
			t.Errorf("turn %d input = %q", i, c)
		}
	}
	us := qSessUserMsgs(t, h.ag.sess.Path)
	if len(us) != 3 {
		t.Errorf("user entries = %d", len(us))
	}
	var at []string
	for _, e := range qEvents(t, h.ag.sess.Path) {
		if e.str("kind") == "queue.consume" {
			at = append(at, e.str("at"))
		}
	}
	if strings.Join(at, ",") != "turn,turn" {
		t.Errorf("consume events at = %v", at)
	}
}

func TestQueueFollowUpSkippedAfterFailure(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp {
		return fakeResp{Status: 418, Body: "teapot"}
	}, nil)
	old := qFollowFlag
	t.Cleanup(func() { qFollowFlag = old })
	qFollowFlag = qFlagList{"não roda"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := h.ag.qRunHeadless(ctx, "primeiro"); err == nil {
		t.Fatal("want error")
	}
	for _, r := range h.fs.requests() {
		if r.contains("user", "não roda") {
			t.Fatal("follow-up ran after a failed turn")
		}
	}
	if h.ag.Queue().Len() != 0 {
		t.Error("queue not drained")
	}
}

// JAZZ_STEER_FILE: lines already in the file and lines appended while the
// run goes are steer messages, read at each tool boundary.
func TestQueueSteerFile(t *testing.T) {
	fn := filepath.Join(t.TempDir(), "steer.txt")
	if err := os.WriteFile(fn, []byte("primeira linha\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JAZZ_STEER_FILE", fn)
	h := newHarness(t, seq(
		fakeResp{Calls: []fakeCall{{ID: "k1", Name: "bash", Args: `{"command":"echo um"}`}}},
		fakeResp{Calls: []fakeCall{{ID: "k2", Name: "bash", Args: `{"command":"echo dois"}`}}},
		fakeResp{Text: "feito"},
	), nil)
	n := 0
	h.ui.onToolStart = func(name, detail string) {
		if n++; n == 2 {
			f, _ := os.OpenFile(fn, os.O_APPEND|os.O_WRONLY, 0)
			f.WriteString("segunda linha\nparcial sem fim")
			f.Close()
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := h.ag.qRunHeadless(ctx, "vai"); err != nil {
		t.Fatal(err)
	}
	reqs := h.fs.requests()
	if len(reqs) != 3 {
		t.Fatalf("requests = %d", len(reqs))
	}
	if c, _ := reqs[1].last()["content"].(string); !strings.Contains(c, qSteerMarker+"primeira linha") {
		t.Errorf("first boundary = %q", c)
	}
	c, _ := reqs[2].last()["content"].(string)
	if !strings.Contains(c, qSteerMarker+"segunda linha") || strings.Contains(c, "primeira") || strings.Contains(c, "parcial") {
		t.Errorf("second boundary = %q", c)
	}
}

func TestQueueTraceLines(t *testing.T) {
	cases := map[string]obEvent{
		"fila + #1 [agora] (tui): oi":                               {"kind": "queue.add", "id": 1.0, "mode": "steer", "src": "tui", "text": "oi"},
		"fila → #1 [agora] entregue na fronteira de ferramenta: oi": {"kind": "queue.consume", "id": 1.0, "mode": "steer", "at": "tool", "text": "oi"},
		"fila → #2 [depois] entregue como turno novo: x":            {"kind": "queue.consume", "id": 2.0, "mode": "follow-up", "at": "turn", "text": "x"},
		"fila − #3 [depois] descartada (esc-esc): y":                {"kind": "queue.drop", "id": 3.0, "mode": "follow-up", "reason": "esc-esc", "text": "y"},
		"fila ~ #4 agora é [depois]":                                {"kind": "queue.edit", "id": 4.0, "mode": "follow-up"},
	}
	for want, e := range cases {
		if got := qTraceLine(e); got != want {
			t.Errorf("got %q want %q", got, want)
		}
	}
	var b strings.Builder
	obTraceText(&b, []obEvent{{"kind": "queue.add", "id": 1.0, "mode": "steer", "src": "tui", "text": "oi", "ts": "2026-10-21T10:00:00Z"}})
	if !strings.Contains(b.String(), "fila + #1 [agora]") {
		t.Errorf("trace:\n%s", b.String())
	}
}
