package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const (
	maxToolBytes    = 30 << 10 // what the model sees from one bash result
	maxReadLines    = 2000
	maxReadBytes    = 50 << 10
	maxDocReadBytes = 64 << 10
	maxLineChars    = 2000
)

var spillSeq atomic.Int64

func spillDir() string { return filepath.Join(dataDir(), "spill") }

// spill saves full output to a file the model can grep/read later.
func spill(content string) string {
	d := spillDir()
	if err := os.MkdirAll(d, 0o700); err != nil {
		return ""
	}
	// the pid keeps concurrent jazz processes (eval lanes) from overwriting each other's files
	p := filepath.Join(d, fmt.Sprintf("out-%s-%d-%d.txt", time.Now().Format("150405"), os.Getpid(), spillSeq.Add(1)))
	if os.WriteFile(p, []byte(redactSecrets(content)), 0o600) != nil {
		return ""
	}
	return p
}

// cleanSpill removes spill and job logs older than 3 days, and session files
// older than session_days (default 30; bug 61).
func cleanSpill() {
	cut := time.Now().Add(-72 * time.Hour)
	for _, d := range []string{spillDir(), filepath.Join(dataDir(), "jobs")} {
		ents, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if fi, err := e.Info(); err == nil && !e.IsDir() && fi.ModTime().Before(cut) {
				os.Remove(filepath.Join(d, e.Name()))
			}
		}
	}
	pfCleanSessions(pfSessionDays())
}

// pfSessionDays reads "session_days" from config.json (JAZZ_SESSION_DAYS wins).
// 0 or missing = 30 days; negative = keep sessions forever.
func pfSessionDays() int {
	if v := os.Getenv("JAZZ_SESSION_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	var c struct {
		SessionDays int `json:"session_days"`
	}
	if b, err := os.ReadFile(filepath.Join(configDir(), "config.json")); err == nil {
		_ = json.Unmarshal(b, &c)
	}
	if c.SessionDays == 0 {
		return 30
	}
	return c.SessionDays
}

// pfCleanSessions deletes session files not touched for more than days.
func pfCleanSessions(days int) int {
	if days < 0 {
		return 0
	}
	root := filepath.Join(dataDir(), "sessions")
	projects, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	cut := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	n := 0
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		dir := filepath.Join(root, p.Name())
		ents, _ := os.ReadDir(dir)
		left := 0
		for _, e := range ents {
			fi, err := e.Info()
			if err != nil || e.IsDir() || !(strings.HasSuffix(e.Name(), ".jsonl") || strings.HasSuffix(e.Name(), obHandoffExt)) { // sessions, events, handoff notes
				left++
				continue
			}
			if fi.ModTime().Before(cut) {
				if os.Remove(filepath.Join(dir, e.Name())) == nil {
					n++
					continue
				}
			}
			left++
		}
		if left == 0 {
			os.Remove(dir)
		}
	}
	return n
}

// truncateHeadTail keeps 20% head and 80% tail (errors live at the end), cut on
// line boundaries, spilling the full text to a file.
func truncateHeadTail(s string, max int) string {
	if len(s) <= max {
		return s
	}
	headN := max / 5
	tailN := max - headN
	head := s[:headN]
	if i := strings.LastIndexByte(head, '\n'); i > headN/2 {
		head = head[:i]
	}
	tail := s[len(s)-tailN:]
	if i := strings.IndexByte(tail, '\n'); i >= 0 && i < tailN/2 {
		tail = tail[i+1:]
	}
	totalLines := strings.Count(s, "\n") + 1
	omitted := len(s) - len(head) - len(tail)
	note := fmt.Sprintf("\n\n[… %d bytes omitted from the middle (%d lines total).", omitted, totalLines)
	if p := spill(s); p != "" {
		note += fmt.Sprintf(" Full output saved to %s — use rg on it or read it with offset/limit instead of re-running.", p)
	}
	note += "]\n\n"
	return head + note + tail
}

func clipLine(l string) string {
	if len(l) <= maxLineChars {
		return l
	}
	cut := maxLineChars
	for cut > 0 && !utf8.RuneStart(l[cut]) {
		cut--
	}
	return l[:cut] + fmt.Sprintf("… [line truncated, %d bytes]", len(l))
}
