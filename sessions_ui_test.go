package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSessAutoTitle(t *testing.T) {
	cases := []struct{ in, want string }{
		{"faça a tarefa1 em 3 passos", "Faça a tarefa1 em 3 passos"},
		{"por favor, você pode corrigir o parser de datas no módulo de relatórios e rodar os testes", "Corrigir o parser de datas no módulo"},
		{"<system-reminder>budget</system-reminder>\nconserta o bug do login", "Conserta o bug do login"},
		{"<file path=\"a.go\">\n     1\tpackage a\n</file>\n\nexplica @a.go pra mim", "Explica @a.go pra mim"},
		{"oi jazz, pode rodar os testes e me dizer o que falhou please", "Rodar os testes e me dizer"},
		{"```go\nfmt.Println(1)\n```\nPor que isso não compila?", "Por que isso não compila"},
		{"   ", "(sem título)"},
		{"## Refatorar **o** módulo `auth` inteiro para usar contexto em vez de globais", "Refatorar o módulo auth inteiro para usar contexto"},
	}
	for _, c := range cases {
		got := sessAutoTitle(c.in)
		if got != c.want {
			t.Errorf("sessAutoTitle(%q) = %q, want %q", c.in, got, c.want)
		}
		if n := len(strings.Fields(got)); got != "(sem título)" && (n > sessTitleMax || n < min(sessTitleMin, len(strings.Fields(c.want)))) {
			t.Errorf("%q: %d words", got, n)
		}
		if again := sessAutoTitle(c.in); again != got {
			t.Errorf("not deterministic: %q vs %q", got, again)
		}
	}
	// a cut title never ends on a connector while it can stay at 5+ words
	if got := sessAutoTitle("adicione um teste para a função de soma de matrizes com números negativos"); strings.HasSuffix(got, " de") || strings.HasSuffix(got, " para") {
		t.Errorf("dangling connector: %q", got)
	}
}

func TestSessTitleStoredAndRenamed(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	root := t.TempDir()
	s, err := NewSession(root, "fake/m1", "SYS")
	if err != nil {
		t.Fatal(err)
	}
	s.Append(Message{Role: "user", Content: "corrige o teste que quebrou no ci ontem à noite"})
	s.AppendMeta(Message{Role: "assistant", Content: "ok", ToolCalls: []ToolCall{{ID: "c1", Name: "bash", Args: `{"command":"ls"}`}}}, &sessMsgMeta{Usage: Usage{In: 1000, Out: 200, Cost: 0.01}})
	s.Append(Message{Role: "tool", ToolCallID: "c1", ToolName: "bash", Content: "a"})
	list := ListSessions(root)
	if len(list) != 1 {
		t.Fatalf("%d sessions", len(list))
	}
	// no title entry yet (a session from before titles): derived, same rule
	if got, want := list[0].Title, sessAutoTitle("corrige o teste que quebrou no ci ontem à noite"); got != want {
		t.Errorf("derived title %q, want %q", got, want)
	}
	if list[0].Cwd != root || list[0].Usage.In != 1000 || list[0].Usage.Cost != 0.01 || list[0].Tools != 1 {
		t.Errorf("info: %+v", list[0])
	}
	s.SetTitle("Primeiro nome")
	s.SetTitle("Nome novo\x1b[31m")
	s.Close()
	list = ListSessions(root)
	if list[0].Title != "Nome novo[31m" && list[0].Title != "Nome novo" {
		t.Errorf("rename: title %q", list[0].Title)
	}
	s2, msgs, _, _, err := LoadSession(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if !strings.HasPrefix(s2.Title(), "Nome novo") {
		t.Errorf("loaded title %q", s2.Title())
	}
	if len(msgs) != 3 {
		t.Errorf("title entries changed the history: %d msgs", len(msgs))
	}
	if all := ListAllSessions(); len(all) != 1 || all[0].ID != s.ID {
		t.Errorf("ListAllSessions = %+v", all)
	}
}

// The first turn of a new session stores its title; later turns and a
// loaded session keep theirs.
func TestAgentAutoTitle(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp { return fakeResp{Text: "feito"} }, nil)
	if err := h.run(t, "por favor crie um servidor http simples em go com testes"); err != nil {
		t.Fatal(err)
	}
	if err := h.run(t, "agora outra coisa completamente diferente aqui"); err != nil {
		t.Fatal(err)
	}
	want := "Crie um servidor http simples em go"
	if got := h.ag.sess.Title(); got != want {
		t.Errorf("title %q, want %q", got, want)
	}
	ents, _ := sessReadEntries(h.ag.sess.Path)
	n := 0
	for _, e := range ents {
		if e.T == "title" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d title entries, want 1", n)
	}
	if l := ListSessions(h.root); len(l) != 1 || l[0].Title != want {
		t.Errorf("list: %+v", l)
	}
	// the mode lands in the event stream for the picker's row
	evs, _ := obReadEvents(obEventsPath(h.ag.sess.Path))
	if s := obSummarize(evs); s.Mode != "auto" {
		t.Errorf("mode in summary = %q", s.Mode)
	}
}

func pkTestItems() []pkItem {
	var items []pkItem
	for _, s := range []string{"Corrigir parser de datas", "Escrever testes do login", "Atualizar README", "Refatorar sessões", "Deploy no servidor"} {
		items = append(items, pkItem{Title: s, Right: "há 2 h", Val: s})
	}
	return items
}

func TestPickerKeys(t *testing.T) {
	sp := &pkSpec{Items: pkTestItems()}
	st := newPkState(sp)
	press := func(ks ...Key) (bool, int) {
		var done bool
		var pick int
		for _, k := range ks {
			done, pick = st.key(k)
			if done {
				break
			}
		}
		return done, pick
	}
	r := func(c rune) Key { return Key{Code: KRune, R: c} }
	// ↓↓ Enter = third row; j/k move too
	if done, pick := press(Key{Code: KDown}, Key{Code: KDown}, Key{Code: KEnter}); !done || pick != 2 {
		t.Fatalf("↓↓ enter = %v %d", done, pick)
	}
	st = newPkState(sp)
	if done, pick := press(r('j'), r('j'), r('j'), r('k'), Key{Code: KEnter}); !done || pick != 2 {
		t.Fatalf("jjjk enter = %v %d", done, pick)
	}
	// type to filter, with a typo: "sesoes" finds "Refatorar sessões"
	st = newPkState(sp)
	press(r('s'), r('e'), r('s'), r('o'), r('e'), r('s'))
	if len(st.view) == 0 || sp.Items[st.view[0]].Title != "Refatorar sessões" {
		t.Fatalf("filter view = %v", st.view)
	}
	// in the filter, j and digits are text, not keys
	press(Key{Code: KCtrlU}, r('d'), r('e'), r('p'), r('l'), r('o'), r('j'))
	if string(st.query) != "deploj" {
		t.Fatalf("query %q", string(st.query))
	}
	if len(st.view) != 1 || sp.Items[st.view[0]].Title != "Deploy no servidor" {
		t.Fatalf("typo filter view = %v", st.view)
	}
	// Esc clears the filter first, then cancels
	if done, _ := press(Key{Code: KEsc}); done || len(st.query) != 0 || len(st.view) != 5 {
		t.Fatalf("first esc: done=%v query=%q view=%d", done, string(st.query), len(st.view))
	}
	if done, pick := press(Key{Code: KEsc}); !done || pick != -1 {
		t.Fatalf("second esc = %v %d", done, pick)
	}
	// digits are a shortcut while nothing is typed
	st = newPkState(sp)
	if done, pick := press(r('4')); !done || pick != 3 {
		t.Fatalf("digit 4 = %v %d", done, pick)
	}
	// "/" starts the filter, so a filter can start with a digit or j
	st = newPkState(sp)
	press(r('/'), r('j'))
	if string(st.query) != "j" {
		t.Fatalf("after / the query is %q", string(st.query))
	}
	// rows that cannot be picked are skipped
	items := pkTestItems()
	items[1].Off = true
	sp2 := &pkSpec{Items: items}
	st = newPkState(sp2)
	if done, pick := func() (bool, int) { st.key(Key{Code: KDown}); return st.key(Key{Code: KEnter}) }(); !done || pick != 2 {
		t.Fatalf("off row not skipped: %v %d", done, pick)
	}
	// Tab replaces the list
	tabbed := 0
	sp3 := &pkSpec{Items: pkTestItems(), Tab: func() ([]pkItem, string) {
		tabbed++
		return []pkItem{{Title: "outra pasta", Val: "x"}}, "todas"
	}}
	st = newPkState(sp3)
	st.key(Key{Code: KTab})
	if done, pick := st.key(Key{Code: KEnter}); tabbed != 1 || !done || pick != 0 || sp3.Items[pick].Val != "x" || sp3.Head != "todas" {
		t.Fatalf("tab: %d %v %d %+v", tabbed, done, pick, sp3.Items)
	}
}

// The picker keeps the selection on screen and says how many rows are below.
func TestPickerScroll(t *testing.T) {
	var items []pkItem
	for i := 0; i < 40; i++ {
		i := i
		items = append(items, pkItem{Title: "sessão " + string(rune('A'+i%26)) + strings.Repeat("x", i%3), Meta: func() string { return "meta " + itoa(i) }, Group: map[bool]string{true: "~/a", false: "~/b"}[i < 20]})
	}
	sp := &pkSpec{Head: "sessões", Hint: pkHint, Items: items}
	st := newPkState(sp)
	th := Theme{}
	for i := 0; i < 25; i++ {
		st.key(Key{Code: KDown})
	}
	lines := st.lines(th, 80, 12)
	if len(lines) > 12 {
		t.Fatalf("%d lines > 12", len(lines))
	}
	all := rpPlain(strings.Join(lines, "\n"))
	if !strings.Contains(all, "meta 25") || !strings.Contains(all, "26/40") || !strings.Contains(all, "~/b") {
		t.Fatalf("selection not visible:\n%s", all)
	}
	for _, l := range lines {
		if uiVisLen(l) > 80 {
			t.Errorf("line wider than 80: %q", stripANSI(l))
		}
	}
	// lazy meta: only rows that were drawn read their meta line
	if len(sp.metas) > 12 {
		t.Errorf("%d meta lines computed for a 12-line list", len(sp.metas))
	}
}

func TestPfResumeArgs(t *testing.T) {
	cases := []struct{ in, want []string }{
		{[]string{"-r"}, []string{"-r=" + pfPickResume}},
		{[]string{"-r", "--yolo"}, []string{"-r=" + pfPickResume, "--yolo"}},
		{[]string{"--resume"}, []string{"--resume=" + pfPickResume}},
		{[]string{"-r", "abc"}, []string{"-r", "abc"}},
		{[]string{"-C", "x", "-r"}, []string{"-C", "x", "-r=" + pfPickResume}},
		{[]string{"--", "-r"}, []string{"--", "-r"}},
	}
	for _, c := range cases {
		if got := pfResumeArgs(c.in); strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("pfResumeArgs(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestReplayLimit(t *testing.T) {
	t.Setenv("JAZZ_RESUME_REPLAY", "")
	if n := replayLimit(&Config{}); n != 200 {
		t.Errorf("default %d", n)
	}
	var c Config
	_ = json.Unmarshal([]byte(`{"resume_replay": 30}`), &c)
	if n := replayLimit(&c); n != 30 {
		t.Errorf("config 30 = %d", n)
	}
	_ = json.Unmarshal([]byte(`{"resume_replay": "all"}`), &c)
	if n := replayLimit(&c); n != -1 {
		t.Errorf("all = %d", n)
	}
	t.Setenv("JAZZ_RESUME_REPLAY", "off")
	if n := replayLimit(&c); n != 0 {
		t.Errorf("env off = %d", n)
	}
}

func TestSessAgo(t *testing.T) {
	now := time.Date(2026, 10, 21, 15, 0, 0, 0, time.Local)
	for _, c := range []struct {
		d    time.Duration
		want string
	}{{10 * time.Second, "agora"}, {5 * time.Minute, "há 5 min"}, {2 * time.Hour, "há 2 h"}, {30 * time.Hour, "ontem"}, {3 * 24 * time.Hour, "há 3 dias"}, {20 * 24 * time.Hour, "1 out"}, {400 * 24 * time.Hour, "16 set 2025"}} {
		if got := sessAgo(now.Add(-c.d), now); got != c.want {
			t.Errorf("%s ago = %q, want %q", c.d, got, c.want)
		}
	}
}

func replayTestMsgs() []Message {
	editArgs, _ := json.Marshal(map[string]any{"path": "calc.py", "old_string": "a\nb\nc", "new_string": "a\nB\nB2\nc"})
	return []Message{
		{Role: "user", Content: "<system-reminder>budget: small</system-reminder>\n<file path=\"x.go\">\n 1\tsegredo\n</file>\n\nconserta o calc"},
		{Role: "assistant", Content: "Vou **olhar** o `calc.py`.", ToolCalls: []ToolCall{
			{ID: "c1", Name: "bash", Args: `{"command":"pytest -q"}`},
			{ID: "c2", Name: "edit", Args: string(editArgs)},
			{ID: "c3", Name: "read", Args: `{"path":"nada.py"}`},
		}},
		{Role: "tool", ToolCallID: "c1", ToolName: "bash", Content: "1 failed. resto\n[exit code 1]"},
		{Role: "tool", ToolCallID: "c2", ToolName: "edit", Content: "Edited calc.py"},
		{Role: "tool", ToolCallID: "c3", ToolName: "read", Content: "Error: file not found"},
		{Role: "user", Content: "<system-reminder>only a harness note</system-reminder>"},
		{Role: "assistant", Content: "## Pronto\n\n- corrigi o **bug**\n- RESPOSTA-FINAL"},
	}
}

func TestReplayView(t *testing.T) {
	uiWidthOverride.Store(86)
	defer uiWidthOverride.Store(0)
	u := NewTermUI(&Config{}, nil)
	u.T = Theme{color: true}
	u.md.t = u.T
	view, hidden, nh := u.replayView(replayTestMsgs(), map[string]bool{"c3": true}, -1, 85)
	vis := rpPlain(view)
	for _, want := range []string{
		"› conserta o calc",
		"● Vou olhar o calc.py.",
		"● Bash(pytest -q)\n  ⎿  1 failed",
		"● Edit(calc.py)\n  ⎿  Atualizou calc.py com 2 adições e 1 remoção",
		"● Read(nada.py)\n  ⎿  Error: file not found",
		"Pronto",
		"RESPOSTA-FINAL",
	} {
		if !strings.Contains(vis, want) {
			t.Errorf("missing %q in:\n%s", want, vis)
		}
	}
	for _, bad := range []string{"system-reminder", "budget: small", "segredo", "harness note", "**", "`"} {
		if strings.Contains(vis, bad) {
			t.Errorf("replay shows %q:\n%s", bad, vis)
		}
	}
	if nh != 0 || hidden != "" {
		t.Errorf("all entries asked, %d hidden", nh)
	}
	// the failed read is red, the bash call green (outcome colors like the live view)
	if !strings.Contains(view, u.T.fg(cRed, 31)+"●") || !strings.Contains(view, u.T.fg(cGreen, 32)+"●") {
		t.Errorf("dots not colored by outcome:\n%q", view)
	}
	// order: prompt, answer, tools, final answer
	iu, it, ie := strings.Index(vis, "conserta o calc"), strings.Index(vis, "Bash(pytest"), strings.Index(vis, "RESPOSTA-FINAL")
	if !(iu < it && it < ie) {
		t.Errorf("order wrong: %d %d %d", iu, it, ie)
	}
	// capped: the last 2 entries and a marker; the rest goes to /expand
	view, hidden, nh = u.replayView(replayTestMsgs(), nil, 2, 85)
	vis = rpPlain(view)
	if nh != 4 || !strings.Contains(vis, "… (4 mais acima, /expand)") || strings.Contains(vis, "conserta o calc") || !strings.Contains(hidden, "conserta o calc") {
		t.Errorf("cap: nh=%d\n%s\nhidden:\n%s", nh, vis, hidden)
	}
	if v, _, _ := u.replayView(replayTestMsgs(), nil, 0, 85); v != "" {
		t.Errorf("off still renders: %q", v)
	}
}

// Replay writes the whole transcript in one write and /expand gets the rest.
func TestReplayOneWrite(t *testing.T) {
	uiWidthOverride.Store(86)
	defer uiWidthOverride.Store(0)
	old := uiWrite
	var writes []string
	uiWrite = func(s string) { writes = append(writes, s) }
	defer func() { uiWrite = old }()
	u := NewTermUI(&Config{}, nil)
	var msgs []Message
	for i := 0; i < 300; i++ {
		msgs = append(msgs, Message{Role: "user", Content: "pergunta " + itoa(i)}, Message{Role: "assistant", Content: "resposta " + itoa(i)})
	}
	u.Replay(msgs, nil, 200, replayTotals(u.T, SessionInfo{Tools: 3, Usage: Usage{In: 12000, Out: 3000}}, len(msgs)))
	big := 0
	for _, w := range writes {
		if strings.Contains(w, "resposta 299") {
			big++
			if !strings.Contains(w, "pergunta 200") || strings.Contains(w, "pergunta 199\n") || !strings.Contains(w, "conversa anterior") {
				t.Errorf("one write should hold the last 200 entries and the totals")
			}
		}
	}
	if big != 1 {
		t.Errorf("transcript in %d writes", big)
	}
	if !strings.Contains(u.lastOut.text, "pergunta 0") || !strings.Contains(u.lastOut.label, "400") {
		t.Errorf("/expand: %q %q", u.lastOut.label, oneLine(u.lastOut.text, 80))
	}
}

func TestSessTreeRows(t *testing.T) {
	t.Setenv("JAZZ_HOME", t.TempDir())
	s, err := NewSession(t.TempDir(), "fake/m1", "SYS")
	if err != nil {
		t.Fatal(err)
	}
	s.Append(Message{Role: "user", Content: "a"})
	s.Append(Message{Role: "assistant", Content: "A"})
	s.Rewind(1)
	s.Append(Message{Role: "assistant", Content: "A2"})
	s.Close()
	rows, err := sessTreeRows(s.Path)
	if err != nil || len(rows) != 3 {
		t.Fatalf("%v %+v", err, rows)
	}
	leaf := 0
	for _, r := range rows {
		if r.Leaf {
			leaf++
			if r.Text != "A2" || !r.Active {
				t.Errorf("leaf row %+v", r)
			}
		}
	}
	if leaf != 1 {
		t.Errorf("%d leaf rows", leaf)
	}
	lines, _ := sessTreeLines(s.Path)
	if len(lines) != 3 || !strings.Contains(lines[2], "← atual") && !strings.Contains(strings.Join(lines, "\n"), "← atual") {
		t.Errorf("lines:\n%s", strings.Join(lines, "\n"))
	}
	_ = os.Remove(filepath.Join(filepath.Dir(s.Path), "x"))
}
