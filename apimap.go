package main

// apimap.go: the "APIs do projeto relacionadas" note. The api_map script
// (jazz-scripts/api_map.py) maps the project's public functions, classes,
// methods and types with signatures by regex; at the start of a task the
// harness ranks them by the request's identifiers and words and appends the
// best matches to the USER message (never the system prompt), so the model
// calls the project's real functions with their real signatures instead of
// guessing. No good match -> no note.

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	amMinScore = 4.0  // a match must share an identifier or two words with the request
	amMaxLines = 8    // symbols in the note
	amMaxChars = 1600 // ~400 tokens
)

var (
	amMu   sync.Mutex
	amMemo = map[string]string{} // root + request -> note (the same request is not re-mapped)
)

type amMatch struct {
	File  string  `json:"file"`
	Line  int     `json:"line"`
	Kind  string  `json:"kind"`
	Name  string  `json:"name"`
	Sig   string  `json:"sig"`
	Doc   string  `json:"doc"`
	Score float64 `json:"score"`
}

// amNote runs api_map with the request as query and renders the note ("" =
// nothing relevant, no python3, or the script failed/timed out).
func amNote(ctx context.Context, tb *Toolbox, input string) string {
	q := strings.TrimSpace(acStripReminders(input))
	if len(q) > 1500 {
		q = q[:1500]
	}
	syms, words := dcTerms(q)
	if len(syms)+len(words) == 0 {
		return ""
	}
	key := tb.Root + "\x00" + q
	amMu.Lock()
	if n, ok := amMemo[key]; ok {
		amMu.Unlock()
		return n
	}
	amMu.Unlock()
	ms := amRun(ctx, tb, q, amMaxLines)
	note := amRender(ms)
	amMu.Lock()
	if len(amMemo) > 200 {
		amMemo = map[string]string{}
	}
	amMemo[key] = note
	amMu.Unlock()
	return note
}

// amRun executes the api_map script directly (no tool call, no permission:
// it only reads the project) with a short deadline.
func amRun(ctx context.Context, tb *Toolbox, query string, limit int) []amMatch {
	r := tb.scRegistry()
	d := r.get("api_map")
	if d == nil || d.source != "builtin" { // a user/project script of that name never runs implicitly
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	res := r.scExec(ctx, d, map[string]any{"query": query, "limit": limit}, tb.Root)
	if res.TimedOut || res.Err != nil {
		dbg("apimap", map[string]any{"timeout": res.TimedOut, "err": res.Err})
		return nil
	}
	var v struct {
		OK   bool `json:"ok"`
		Data struct {
			Matches []amMatch `json:"matches"`
			MS      int       `json:"ms"`
			Files   int       `json:"files"`
		} `json:"data"`
	}
	if json.Unmarshal(res.Stdout, &v) != nil || !v.OK {
		return nil
	}
	dbg("apimap", map[string]any{"ms": v.Data.MS, "files": v.Data.Files, "matches": len(v.Data.Matches)})
	return v.Data.Matches
}

func amRender(ms []amMatch) string {
	var lines []string
	size := 0
	for _, m := range ms {
		if m.Score < amMinScore {
			continue
		}
		sig := m.Sig
		if f := strings.SplitN(sig, " ", 2)[0]; !strings.Contains(" func type fn struct enum trait class def pub export ", " "+f+" ") {
			sig = m.Kind + " " + sig
		}
		l := m.File + ":" + strconv.Itoa(m.Line) + " " + sig
		if m.Doc != "" {
			l += " — " + m.Doc
		}
		l = oneLine(l, 240)
		if size+len(l) > amMaxChars {
			break
		}
		lines = append(lines, "- "+l)
		size += len(l)
	}
	if len(lines) == 0 {
		return ""
	}
	return "APIs do projeto relacionadas ao pedido (mapa por regex, assinaturas reais; confira antes de usar):\n" + strings.Join(lines, "\n")
}

var amCodeRE = regexp.MustCompile("`[^`]+`|\\b[a-z0-9]+_[a-z0-9_]+\\b|\\b[a-z]+[A-Z]\\w*|\\b[A-Z][a-z0-9]+[A-Z]\\w*|\\b\\w+\\.(py|go|js|ts|tsx|jsx|rs|rb|java)\\b|\\b\\w+\\(\\)")

// amCodeShaped: the request names code (snake_case, camelCase, a source
// file, `backticks`, f()).
func amCodeShaped(s string) bool { return amCodeRE.MatchString(acStripReminders(s)) }
