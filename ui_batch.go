package main

// ui_batch.go: the merged confirmation (approve_batch.go). One box lists
// every call of the response that needs an OK, numbered, with its risk in
// red; Enter says yes to all of them, u asks one by one, n or Esc refuses
// them all. Same guards as a single approval: keys in the first 250 ms or
// while the user is still typing do not answer.

import (
	"context"
	"fmt"
	"strings"
)

// ApproveBatch asks for several calls at once.
func (u *TermUI) ApproveBatch(ctx context.Context, items []apBatchItem) ([]Approval, bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	if u.keys == nil {
		return nil, true // no terminal: the single prompts deny each one
	}
	u.mu.Lock()
	lastKey := u.lastKey
	u.mu.Unlock()
	u.StopThinking()
	t, g := u.T, u.T.G()
	w := termWidth() - 1
	inner := mnBoxWidth(w) - 4
	reset, dim, red := t.sgr("0"), t.fg(cGray, 90), t.fg(cRed, 31)
	border := t.fg(cGold, 33)
	var body []string
	for i, it := range items {
		if it.Reason != "" {
			border = red
		}
		head := fmt.Sprintf("%d. %s%s%s  ", i+1, t.sgr("1"), uiToolLabel(it.Tool), reset)
		d := oneLine(u.uiDetail(it.Detail), 400)
		body = append(body, mnClip(head+d, inner))
		if it.Reason != "" {
			for _, l := range mnWrap(g.warn+" "+sanitizeTerm(it.Reason), inner-3) {
				body = append(body, "   "+red+l)
			}
		}
	}
	all := []Approval{}
	for range items {
		all = append(all, Allow)
	}
	opts := []mnItem{
		{Label: fmt.Sprintf("Sim, as %d", len(items)), Key: 's', Keys: "yt"},
		{Label: "Uma por uma", Key: 'u'},
		{Label: "Não, nenhuma", Key: 'n'},
	}
	sp := mnSpec{Tag: fmt.Sprintf("%d confirmações", len(items)), Body: body, Title: fmt.Sprintf("Fazer estas %d ações?", len(items)),
		Items: opts, Color: border, Guard: true, Since: lastKey,
		Keys: "enter sim pra tudo " + g.dot + " u uma por uma " + g.dot + " n/esc nenhuma " + g.dot + " v ver tudo"}
	sp.View = func() string {
		var b strings.Builder
		for i, it := range items {
			fmt.Fprintf(&b, "    %s%s%s %d. %s\n", dim, g.bar, reset, i+1, uiToolLabel(it.Tool))
			for _, l := range uiWrap(u.uiDetail(it.Detail), max(w-8, 10)) {
				b.WriteString("    " + dim + g.bar + reset + "   " + l + "\n")
			}
			if it.Reason != "" {
				b.WriteString("    " + dim + g.bar + reset + "   " + red + g.warn + " " + sanitizeTerm(it.Reason) + reset + "\n")
			}
		}
		return b.String()
	}
	keys, release := u.grabKeys(ctx)
	defer release()
	i := u.menuKeys(ctx, keys, &sp)
	what := fmt.Sprintf("%d ações", len(items))
	switch i {
	case 0:
		u.menuDone(true, "sim pra tudo "+dim+g.dot+" "+what)
		return all, false
	case 1:
		u.menuDone(true, "uma por uma "+dim+g.dot+" "+what)
		return nil, true
	}
	u.menuDone(false, "não "+dim+g.dot+" "+what)
	no := make([]Approval, len(items))
	return no, false
}
