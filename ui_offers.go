package main

import (
	"fmt"
	"strings"
	"time"
)

// TermUI side of offers.go: the collapsed menu in the status line, the
// output block of a deterministic action, and notices that arrive while the
// input box is up.

// uiOfferAfter: the collapsed menu shows once the first token is this late.
const uiOfferAfter = 5 * time.Second

// SetOffers installs the actions a digit can run during this turn (nil clears).
func (u *TermUI) SetOffers(items []olAction, run func(olAction)) {
	u.mu.Lock()
	u.offers, u.offerRun = items, run
	u.mu.Unlock()
}

// offerLineNow is the collapsed menu when it should show (u.mu held): the
// request is still waiting for its first token after uiOfferAfter.
func (u *TermUI) offerLineNow() string {
	if len(u.offers) == 0 || !u.spinOn || u.lv.phase == "gerando" || u.lv.toks > 0 || time.Since(u.spinStart) < uiOfferAfter {
		return ""
	}
	return olMenuLine(u.offers)
}

// offerPick handles a digit typed during the turn (watcher goroutine):
// true = it picked an offer (the request keeps going).
func (u *TermUI) offerPick(r rune) bool {
	u.mu.Lock()
	if r < '1' || r > '9' || u.offerLineNow() == "" {
		u.mu.Unlock()
		return false
	}
	i := int(r - '1')
	if i >= len(u.offers) || u.offerRun == nil {
		u.mu.Unlock()
		return false
	}
	act, run := u.offers[i], u.offerRun
	u.mu.Unlock()
	safeGo(func() { run(act) })
	return true
}

// lastErrText is the text of the last failed tool call (menus use it).
func (u *TermUI) lastErrText() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.lastErr
}

// uiActionMax is how many output lines an action shows (ctrl+o shows all).
const uiActionMax = 40

// ActionBody prints the output of a deterministic action as a block under
// its tool line, clipped to the terminal, with the spill hint.
func (u *TermUI) ActionBody(text string) {
	text = strings.TrimRight(strings.ReplaceAll(text, "\r\n", "\n"), "\n ")
	if strings.TrimSpace(text) == "" {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.endText()
	u.ensureLine()
	t, g := u.T, u.T.G()
	w := termWidth() - 1
	lines := strings.Split(text, "\n")
	var b strings.Builder
	shown := min(len(lines), uiActionMax)
	for _, l := range lines[:shown] {
		b.WriteString(clipVisible("     "+uiClean(l), w) + "\n")
	}
	if n := len(lines) - shown; n > 0 {
		fmt.Fprintf(&b, "     %s%s +%d %s %s(ctrl+o pra expandir)%s\n", t.fg(cGray, 90), g.ell, n, uiPl(n, "linha", "linhas"), t.sgr("3"), t.sgr("0"))
	}
	u.rawWrite(b.String())
	u.lastOut = uiLastOut{label: "saída", text: text}
}

// AsyncNotice shows msg now, even while the input box is up (the editor
// erases its frame, prints it and redraws). Safe from any goroutine.
func (u *TermUI) AsyncNotice(msg string) {
	u.mu.Lock()
	if u.edReading {
		select {
		case u.async <- msg:
		default:
		}
		u.mu.Unlock()
		return
	}
	u.mu.Unlock()
	u.Notice(msg)
}

// asyncC is the channel the editor selects on.
func (u *TermUI) asyncC() <-chan string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.async == nil {
		u.async = make(chan string, 8)
	}
	return u.async
}

func (u *TermUI) setReading(on bool) {
	u.mu.Lock()
	u.edReading = on
	if u.async == nil {
		u.async = make(chan string, 8)
	}
	u.mu.Unlock()
}
