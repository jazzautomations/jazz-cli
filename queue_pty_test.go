//go:build darwin || linux

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// qPTYModel is a fake provider for the message queue: "fila tarefa" makes two
// slow bash calls, then answers; "changelog" answers at once. Every request
// body is recorded.
type qPTYModel struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs [][]map[string]any
}

func newQPTYModel(t *testing.T, sleep1, sleep2 string) *qPTYModel {
	m := &qPTYModel{}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.mu.Lock()
		m.reqs = append(m.reqs, body.Messages)
		m.mu.Unlock()
		// the last real user message (not a queued steer) and the tools after it
		ask, tools := "", 0
		for _, msg := range body.Messages {
			c := fmt.Sprint(msg["content"])
			switch {
			case msg["role"] == "user" && !strings.Contains(c, qSteerMarker):
				ask, tools = c, 0
			case msg["role"] == "tool":
				tools++
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		send := func(delta map[string]any, finish string) {
			ch := map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta}}}
			if finish != "" {
				ch["choices"].([]any)[0].(map[string]any)["finish_reason"] = finish
			}
			b, _ := json.Marshal(ch)
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		}
		call := func(id, cmd string) {
			args, _ := json.Marshal(map[string]any{"command": cmd})
			send(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": id, "type": "function",
				"function": map[string]any{"name": "bash", "arguments": string(args)}}}}, "tool_calls")
			fmt.Fprint(w, "data: [DONE]\n\n")
		}
		text := func(s string) {
			send(map[string]any{"content": s}, "")
			send(map[string]any{}, "stop")
			fmt.Fprint(w, "data: [DONE]\n\n")
		}
		switch {
		case strings.Contains(ask, "fila tarefa") && tools == 0:
			call("q1", sleep1)
		case strings.Contains(ask, "fila tarefa") && tools == 1:
			call("q2", sleep2)
		case strings.Contains(ask, "fila tarefa"):
			text("terminei a tarefa")
		case strings.Contains(ask, "changelog"):
			text("changelog feito")
		default:
			text("ok")
		}
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *qPTYModel) requests() [][]map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([][]map[string]any(nil), m.reqs...)
}

func (m *qPTYModel) sawText(s string) bool {
	for _, r := range m.requests() {
		b, _ := json.Marshal(r)
		if strings.Contains(string(b), s) {
			return true
		}
	}
	return false
}

func qPTYStart(t *testing.T, m *qPTYModel) *ptySession {
	cfg := map[string]any{"model": "fake/m1", "mode": "auto", "providers": map[string]any{
		"fake": map[string]any{"type": "openai", "base_url": m.srv.URL + "/v1", "no_key": true}}}
	s := ptyStartWith(t, "xterm-256color", cfg, "NO_PROXY=127.0.0.1,localhost", "HTTPS_PROXY=", "https_proxy=")
	s.trust()
	s.waitFor("the input box", 15*time.Second, ptyPrompt)
	return s
}

// Typing during a run queues the message (visible under the live region);
// the steer reaches the model at the next tool boundary (between the two
// tool calls) and the follow-up runs as the next turn.
func TestPTYQueueSteerAndFollowUp(t *testing.T) {
	if testing.Short() {
		t.Skip("pty session")
	}
	m := newQPTYModel(t, "sleep 2; echo um", "sleep 1; echo dois")
	s := qPTYStart(t, m)
	s.typeSlow("fila tarefa")
	s.write("\r")
	s.waitFor("the first tool", 10*time.Second, func(string) bool { return len(m.requests()) >= 1 })
	s.typeSlow("usa src")
	s.write("\r")
	s.waitFor("the steer in the queue", 5*time.Second, func(sc string) bool {
		return strings.Contains(sc, "na fila (1)") && strings.Contains(sc, "[agora] usa src")
	})
	s.typeSlow("depois: escreve o changelog")
	s.write("\r")
	s.waitFor("the follow-up in the queue", 5*time.Second, func(sc string) bool {
		return strings.Contains(sc, "na fila (2)") && strings.Contains(sc, "[depois] escreve o changelog")
	})
	if strings.Contains(s.transcript(), "terminei a tarefa") {
		t.Fatal("the turn ended before the queue was checked (slow machine?)")
	}
	s.waitFor("the steer delivered", 10*time.Second, func(sc string) bool {
		return strings.Contains(s.transcript(), "entregue agora: usa src")
	})
	s.waitFor("the follow-up turn", 15*time.Second, func(sc string) bool {
		return strings.Contains(s.transcript(), "changelog feito") && ptyPrompt(sc) && !strings.Contains(sc, "na fila (")
	})
	// the steer went out right after the q1 result, before q2 was asked for
	reqs := m.requests()
	if len(reqs) != 4 {
		t.Fatalf("requests = %d", len(reqs))
	}
	r := reqs[1]
	last, prev := r[len(r)-1], r[len(r)-2]
	if c := fmt.Sprint(last["content"]); last["role"] != "user" || !strings.Contains(c, qSteerMarker+"usa src") {
		t.Errorf("request 2 last = %v", last)
	}
	if prev["role"] != "tool" || prev["tool_call_id"] != "q1" {
		t.Errorf("steer not after q1: %v", prev)
	}
	if c := fmt.Sprint(reqs[3][len(reqs[3])-1]["content"]); !strings.Contains(c, "escreve o changelog") || strings.Contains(c, "depois:") {
		t.Errorf("follow-up turn input = %q", c)
	}
	s.write("\x04")
	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
		t.Error("jazz did not exit")
	}
}

// Up/Down edit an item, Delete removes one, Tab toggles agora/depois, and
// Esc Esc cancels the run and clears the queue: nothing queued reaches the model.
func TestPTYQueueEditDeleteClear(t *testing.T) {
	if testing.Short() {
		t.Skip("pty session")
	}
	m := newQPTYModel(t, "sleep 20; echo um", "echo dois")
	s := qPTYStart(t, m)
	s.typeSlow("fila tarefa")
	s.write("\r")
	s.waitFor("the first tool", 10*time.Second, func(string) bool { return len(m.requests()) >= 1 })
	for _, msg := range []string{"a1", "depois: b2", "c3"} {
		s.typeSlow(msg)
		s.write("\r")
	}
	s.waitFor("three items", 5*time.Second, func(sc string) bool { return strings.Contains(sc, "na fila (3)") })
	// Up selects the last item (c3) for editing
	s.write("\x1b[A")
	time.Sleep(60 * time.Millisecond)
	s.write("\x15") // Ctrl+U
	s.typeSlow("c3 editado")
	s.write("\r")
	s.waitFor("the edited item", 5*time.Second, func(sc string) bool { return strings.Contains(sc, "[agora] c3 editado") })
	// Up Up selects b2, Delete removes it
	s.write("\x1b[A")
	time.Sleep(60 * time.Millisecond)
	s.write("\x1b[A")
	time.Sleep(60 * time.Millisecond)
	s.write("\x1b[3~")
	s.waitFor("b2 removed", 5*time.Second, func(sc string) bool {
		return strings.Contains(sc, "na fila (2)") && !strings.Contains(sc, "[depois] b2")
	})
	// the selection moved to c3 editado; Tab makes it a follow-up
	s.write("\t")
	s.waitFor("c3 toggled", 5*time.Second, func(sc string) bool { return strings.Contains(sc, "[depois] c3 editado") })
	s.write("\x1b[B") // Down: leave the queue
	time.Sleep(60 * time.Millisecond)
	s.write("\x1b\x1b")
	s.waitFor("the prompt back", 15*time.Second, func(sc string) bool { return ptyPrompt(sc) && !strings.Contains(sc, "na fila (") })
	time.Sleep(300 * time.Millisecond)
	sc := s.screen()
	for _, w := range []string{"a1", "c3 editado"} {
		if m.sawText(w) {
			t.Errorf("%q reached the model", w)
		}
	}
	// whichever got the second Esc (the queue or the editor), the box is empty
	for _, l := range strings.Split(sc, "\n") {
		if strings.Contains(l, "│") && (strings.Contains(l, "a1") || strings.Contains(l, "c3 editado")) {
			t.Errorf("queued text left in the input box: %q", l)
		}
	}
	if !strings.Contains(s.transcript(), "interrompendo") {
		t.Error("first Esc did not interrupt")
	}
	s.write("\x04")
	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
		t.Error("jazz did not exit")
	}
}

// The keys of WatchTurn drive the queue: Enter adds, Up edits, Tab toggles,
// Delete removes, Esc Esc clears; the live region lists it.
func TestQueueWatchTurnKeys(t *testing.T) {
	u, keys := uiTestUI(t)
	q := &MsgQueue{}
	u.qAttach(q)
	cancelled := 0
	stop := u.WatchTurn(func() { cancelled++ }, func(string) {})
	defer func() { stop() }()
	settle := func() { time.Sleep(40 * time.Millisecond) }
	uiSendRunes(keys, "usa src\n")
	uiSendRunes(keys, "depois: changelog\n")
	settle()
	it := q.Items()
	if len(it) != 2 || it[0].Mode != qSteer || it[1].Mode != qFollow || it[1].Text != "changelog" {
		t.Fatalf("items = %+v", it)
	}
	out, rows := u.qLive(80)
	out = stripANSI(out)
	if rows != 3 || !strings.Contains(out, "na fila (2)") || !strings.Contains(out, "[agora] usa src") || !strings.Contains(out, "[depois] changelog") {
		t.Errorf("live (%d rows):\n%s", rows, out)
	}
	// draft kept while editing: Up Up selects the first item, Down/Down returns
	uiSendRunes(keys, "rascunho")
	keys <- Key{Code: KUp}
	keys <- Key{Code: KUp}
	settle()
	u.mu.Lock()
	typed := u.typed
	u.mu.Unlock()
	if typed != "usa src" {
		t.Errorf("typed after Up Up = %q", typed)
	}
	if out, _ := u.qLive(80); !strings.Contains(stripANSI(out), u.T.G().ptr+" [agora] usa src") {
		t.Errorf("selected item not marked:\n%s", out)
	}
	uiSendRunes(keys, " e lib\n") // Enter saves the edit and restores the draft
	settle()
	if it := q.Items(); it[0].Text != "usa src e lib" || len(it) != 2 {
		t.Errorf("edit = %+v", it)
	}
	u.mu.Lock()
	typed = u.typed
	u.mu.Unlock()
	if typed != "rascunho" {
		t.Errorf("draft not restored: %q", typed)
	}
	keys <- Key{Code: KCtrlU}
	keys <- Key{Code: KUp} // last item
	keys <- Key{Code: KTab}
	settle()
	if q.Items()[1].Mode != qSteer {
		t.Error("Tab did not toggle the selected item")
	}
	keys <- Key{Code: KDelete}
	settle()
	if it := q.Items(); len(it) != 1 || it[0].Text != "usa src e lib" {
		t.Errorf("after Delete = %+v", it)
	}
	keys <- Key{Code: KDown} // leave the queue
	uiSendRunes(keys, "mais uma\n")
	settle()
	if q.Len() != 2 {
		t.Fatalf("len = %d", q.Len())
	}
	if cancelled != 0 {
		t.Fatal("queue keys must not cancel")
	}
	keys <- Key{Code: KEsc}
	keys <- Key{Code: KEsc}
	settle()
	if q.Len() != 0 {
		t.Errorf("Esc Esc left %d items", q.Len())
	}
	if cancelled != 1 {
		t.Errorf("first Esc must cancel once, got %d", cancelled)
	}
	if out, rows := u.qLive(80); out != "" || rows != 0 {
		t.Errorf("empty queue still drawn: %q", out)
	}
}
