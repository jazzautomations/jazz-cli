package main

// ui_secret.go: the masked sudo password prompt in the middle of a turn
// (consent.go, secretAsker). The password is typed into jazz, never into
// the model's context: the terminal shows bullets only (no length), and the
// value goes back to the caller and nowhere else.

import (
	"context"
	"strings"
	"time"
)

// AskSecret reads one masked line. ok=false on Esc/Ctrl+C/EOF or cancel.
func (u *TermUI) AskSecret(ctx context.Context, label string) (string, bool) {
	if u.keys == nil {
		return "", false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	u.StopThinking()
	u.mu.Lock()
	u.endText()
	u.gap()
	u.mu.Unlock()
	t := u.T
	bullet := "•"
	if t.ascii {
		bullet = "*"
	}
	keys, release := u.grabKeys(ctx)
	defer release()
	var buf []rune
	draw := func() {
		u.print("\r\x1b[K" + t.fg(cGold, 33) + label + t.sgr("0") + func() string { // typing shows, the length does not
			if len(buf) == 0 {
				return ""
			}
			return strings.Repeat(bullet, 3)
		}())
	}
	draw()
	since := time.Now()
	for {
		k, ok := uiReadKey(ctx, keys)
		if !ok {
			u.print("\n")
			return "", false
		}
		if time.Since(since) < uiApproveGrace && k.Code != KEsc && k.Code != KCtrlC {
			continue // typed ahead before the prompt existed: never part of a password
		}
		switch k.Code {
		case KEnter:
			u.print("\n")
			return string(buf), true
		case KEsc, KCtrlC:
			u.print("\n")
			return "", false
		case KBackspace:
			if len(buf) > 0 {
				buf = buf[:len(buf)-1]
			}
		case KCtrlU:
			buf = nil
		case KRune:
			buf = append(buf, k.R)
		case KPaste:
			buf = append(buf, []rune(strings.TrimRight(k.Text, "\r\n"))...)
		default:
			continue
		}
		draw()
	}
}
