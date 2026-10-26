package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrInterrupt = errors.New("interrupt")
	ErrEOF       = errors.New("eof")
)

type Editor struct {
	ui       *TermUI
	history  []string
	histPath string
	complete func(prefix string) []string

	// CycleMode is called on Shift+Tab / Ctrl+T (set by main); may be nil.
	CycleMode func()
	prefill   string

	// Root is the directory whose files @path completion offers (default: cwd).
	Root string

	files  uiFileCache
	pastes map[string]string // collapsed paste marker -> pasted text (G17)
	pasteN int

	// boxed input (ReadInput)
	Footer func() string // the line under the box (set by main); may be nil
	// LiveFooter: the footer changes by itself (offline countdown): redraw
	// once a second while it returns true. May be nil.
	LiveFooter func() bool
	Commands   func() []EdCommand // slash commands for the "/" menu (set by main); may be nil
	frame      edFrame
	menuSel    int
	menuOff    bool   // Esc closed the menu until the text changes
	menuFor    string // buffer the menu selection belongs to
	help       bool   // "?" shortcut list shown under the box
	note       string // one-shot message in place of the footer

	cmds   []EdCommand // Commands() of this input (read once per ReadInput)
	popKey string      // buffer+cursor the cached pop-up belongs to
	popVal edPop
	paths  edPaths // @ picker candidates (nameindex.go)
	hist   edHist  // Up/Down walk with prefix filtering
}

// EdCommand is one entry of the slash-command menu.
type EdCommand struct{ Name, Help string }

// edFrame remembers where the boxed input was drawn, to redraw it in place.
type edFrame struct {
	drawn bool
	up    int // rows from the top of the frame to the cursor row
}

// Prefill puts s in the buffer of the next ReadLine (used by /rewind).
func (e *Editor) Prefill(s string) { e.prefill = s }

func NewEditor(ui *TermUI) *Editor {
	e := &Editor{ui: ui, histPath: filepath.Join(dataDir(), "history")}
	if b, err := os.ReadFile(e.histPath); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if l != "" {
				e.history = append(e.history, strings.ReplaceAll(l, "\\n", "\n"))
			}
		}
	}
	return e
}

func (e *Editor) remember(s string) {
	if strings.TrimSpace(s) == "" {
		return
	}
	if n := len(e.history); n > 0 && e.history[n-1] == s {
		return
	}
	e.history = append(e.history, s)
	if len(e.history) > 1000 {
		e.history = e.history[len(e.history)-1000:]
	}
	_ = os.MkdirAll(filepath.Dir(e.histPath), 0o700)
	var b strings.Builder
	for _, h := range e.history {
		b.WriteString(strings.ReplaceAll(h, "\n", "\\n") + "\n")
	}
	_ = writeFileAtomic(e.histPath, []byte(b.String()), 0o600)
}

func (e *Editor) render(prompt string, promptW int, buf []rune, pos int) {
	w := termWidth() - 1
	avail := w - promptW
	if avail < 10 {
		avail = 10
	}
	disp := make([]rune, len(buf))
	nl := '↵'
	if e.ui.T.ascii {
		nl = '~'
	}
	for i, r := range buf {
		switch {
		case r == '\n':
			disp[i] = nl
		case r == '\t':
			disp[i] = ' '
		case r < 0x20 || r == 0x7f || r >= 0x80 && r <= 0x9f:
			disp[i] = '?'
		default:
			disp[i] = r
		}
	}
	start := 0
	if pos > avail-1 {
		start = pos - (avail - 1)
	}
	end := start + avail
	if end > len(disp) {
		end = len(disp)
	}
	vis := string(disp[start:end])
	col := promptW + (pos - start)
	// whole line in one write: no flicker over slow SSH
	s := "\r\x1b[K" + prompt + vis + "\r"
	if col > 0 {
		s += "\x1b[" + itoa(col) + "C"
	}
	uiWrite(s)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// edCoalesce skips the redraw while more keys are already queued (tests that
// check intermediate frames turn it off).
var edCoalesce = true

// uiPasteLines is the size above which a paste is shown as a marker (G17).
const uiPasteLines = 10

// ReadLine edits one input on a single line. prompt may contain ANSI; promptW
// is its visible width. The buffer starts with the Prefill text and whatever
// was typed during the last turn (bug 39).
func (e *Editor) ReadLine(prompt string, promptW int) (string, error) {
	return e.read(false, prompt, promptW)
}

// ReadInput edits the main prompt inside a full-width rounded box that grows
// with multi-line input, with the Footer line (or the slash-command menu, or
// the shortcut list) under it. On Enter the box is replaced by the echoed input.
func (e *Editor) ReadInput() (string, error) {
	if termWidth() < 24 {
		p := e.ui.T.fg(cAccent, 36) + e.ui.T.sgr("1") + ">" + e.ui.T.sgr("0") + " "
		return e.read(false, p, 2)
	}
	return e.read(true, "", 0)
}

func (e *Editor) read(boxed bool, prompt string, promptW int) (string, error) {
	e.ui.Flush()
	buf := []rune(e.prefill)
	e.prefill = ""
	if l := e.ui.takeLeftover(); len(l) > 0 {
		buf = append(buf, l...)
	}
	pos := len(buf)
	e.pastes = nil
	e.hist.reset()
	e.cmds, e.popKey = nil, ""
	ctrlC := false
	histPrefix := e.edOpt("JAZZ_HISTORY_PREFIX", func(c UIConf) string { return c.HistoryPrefix })
	insert := func(rs []rune) {
		buf = append(buf[:pos], append(append([]rune{}, rs...), buf[pos:]...)...)
		pos += len(rs)
	}
	e.menuSel, e.menuOff, e.help, e.note = 0, false, false, ""
	draw := func() {
		if boxed {
			e.drawBox(buf, pos)
		} else {
			e.render(prompt, promptW, buf, pos)
		}
	}
	// above prints s on its own lines, above the input (which is redrawn after).
	above := func(s string) {
		if boxed {
			e.eraseFrame()
			e.ui.print(s)
		} else {
			uiWrite("\r\x1b[K" + s)
		}
	}
	draw()
	winch := uiWinch()
	e.ui.setReading(true)
	defer e.ui.setReading(false)
	async := e.ui.asyncC()
	var tick <-chan time.Time
	if boxed && e.LiveFooter != nil && e.LiveFooter() {
		tk := time.NewTicker(time.Second)
		defer tk.Stop()
		tick = tk.C
	}
	for {
		var k Key
		select {
		case <-tick:
			if e.LiveFooter() {
				draw()
			}
			continue
		case msg := <-async:
			if boxed {
				e.eraseFrame()
			} else {
				uiWrite("\r\x1b[K")
			}
			e.ui.Notice(msg)
			draw()
			continue
		case kk, ok := <-e.ui.keys.Keys:
			if !ok {
				if boxed {
					e.eraseFrame()
				}
				return "", ErrEOF
			}
			k = kk
		case <-winch:
			uiWidthInvalidate()
			draw()
			continue
		}
		if k.Code != KCtrlC {
			ctrlC = false
		}
		e.note = ""
		before := string(buf)
		navKey := k.Code == KUp || k.Code == KDown || k.Code == KTab || k.Code == KEsc || k.Code == KEnter
		if pop := e.popup(boxed && navKey, buf, pos); pop.kind != edPopNone { // only keys the pop-up uses pay for it
			n := len(pop.items)
			picked := false
			switch k.Code {
			case KUp:
				e.menuSel = (e.menuSel + n - 1) % n
				draw()
				continue
			case KDown:
				e.menuSel = (e.menuSel + 1) % n
				draw()
				continue
			case KTab:
				buf, pos = pop.pick(buf, e.menuSel, true)
				picked = true
			case KEsc:
				e.menuOff = true
				draw()
				continue
			case KEnter:
				if pop.kind == edPopMention {
					buf, pos = pop.pick(buf, e.menuSel, false)
					picked = true
				} else if pos == len(buf) {
					buf, pos = pop.pick(buf, e.menuSel, false) // and submit below
				}
			}
			if picked {
				e.menuFor, e.menuSel, e.menuOff = string(buf), 0, false
				draw()
				continue
			}
		}
		switch k.Code {
		case KRune:
			if boxed && k.R == '?' && len(buf) == 0 {
				e.help = !e.help
				break
			}
			insert([]rune{k.R})
		case KPaste:
			text := strings.ReplaceAll(strings.ReplaceAll(k.Text, "\r\n", "\n"), "\r", "\n")
			if n := strings.Count(strings.TrimRight(text, "\n"), "\n") + 1; n > uiPasteLines {
				e.pasteN++
				mark := fmt.Sprintf("[colado #%d +%d linhas]", e.pasteN, n)
				if e.pastes == nil {
					e.pastes = map[string]string{}
				}
				e.pastes[mark] = text
				insert([]rune(mark))
			} else {
				insert([]rune(text))
			}
		case KAltEnter:
			insert([]rune{'\n'})
		case KEnter:
			if pos > 0 && pos == len(buf) && buf[pos-1] == '\\' {
				buf[pos-1] = '\n'
				break
			}
			s := string(buf)
			if boxed {
				e.eraseFrame()
				e.ui.print(e.echo(s))
			} else {
				uiWrite("\r\x1b[K" + prompt + strings.ReplaceAll(sanitizeTerm(s), "\n", "\n  ") + "\n")
			}
			s = e.expandPastes(s)
			e.remember(s)
			return s, nil
		case KBackspace:
			if pos > 0 {
				buf = append(buf[:pos-1], buf[pos:]...)
				pos--
			}
		case KDelete:
			if pos < len(buf) {
				buf = append(buf[:pos], buf[pos+1:]...)
			}
		case KLeft:
			if pos > 0 {
				pos--
			}
		case KRight:
			if pos < len(buf) {
				pos++
			}
		case KHome, KCtrlA:
			pos = edLineStart(buf, pos)
		case KEnd, KCtrlE:
			pos = edLineEnd(buf, pos)
		case KWordLeft:
			for pos > 0 && unicode.IsSpace(buf[pos-1]) {
				pos--
			}
			for pos > 0 && !unicode.IsSpace(buf[pos-1]) {
				pos--
			}
		case KWordRight:
			for pos < len(buf) && unicode.IsSpace(buf[pos]) {
				pos++
			}
			for pos < len(buf) && !unicode.IsSpace(buf[pos]) {
				pos++
			}
		case KCtrlU:
			buf, pos = edKillBack(buf, pos)
		case KCtrlK:
			buf = edKillFwd(buf, pos)
		case KCtrlW:
			p := pos
			for p > 0 && unicode.IsSpace(buf[p-1]) {
				p--
			}
			for p > 0 && !unicode.IsSpace(buf[p-1]) {
				p--
			}
			buf = append(buf[:p], buf[pos:]...)
			pos = p
		case KUp:
			// a multi-line input: Up moves between its lines first
			if np, ok := edVertical(buf, pos, -1); ok {
				pos = np
				break
			}
			cur := string(buf)
			if !histPrefix && !e.hist.on {
				e.hist = edHist{on: true, idx: len(e.history), saved: cur}
			}
			if h, ok := e.hist.up(e.history, cur); ok {
				buf = []rune(h)
				pos = len(buf)
			}
		case KDown:
			if np, ok := edVertical(buf, pos, 1); ok {
				pos = np
				break
			}
			if h, ok := e.hist.down(e.history, string(buf)); ok {
				buf = []rune(h)
				pos = len(buf)
			}
		case KTab:
			if boxed {
				e.eraseFrame()
			}
			buf, pos = e.tab(buf, pos)
		case KShiftTab, KCtrlT:
			if e.CycleMode != nil {
				e.CycleMode()
				mode := e.ui.cfg.EffMode()
				e.ui.SetMode(mode)
				if !boxed { // the boxed footer shows the mode
					uiWrite("\r\x1b[K  " + e.ui.T.fg(cGray, 90) + "modo " + e.ui.modeLabel(mode) + e.ui.T.fg(cGray, 90) + " " + e.ui.T.G().dot + " shift+tab troca" + e.ui.T.sgr("0") + "\n")
				}
			}
		case KCtrlO:
			if boxed {
				e.eraseFrame()
			} else {
				uiWrite("\r\x1b[K")
			}
			e.ui.ToggleLast()
		case KCtrlR:
			if boxed {
				e.eraseFrame()
			}
			if s, ok := e.reverseSearch(string(buf)); ok {
				buf = []rune(s)
				pos = len(buf)
			}
		case KCtrlG:
			if boxed {
				e.eraseFrame()
			}
			s, err := e.externalEdit(e.expandPastes(string(buf)))
			if err != nil {
				above(uiErrorLine(e.ui.T, "editor: "+err.Error(), termWidth()-1))
			} else {
				e.pastes = nil
				buf = []rune(s)
				pos = len(buf)
			}
		case KCtrlL:
			uiWrite("\x1b[H\x1b[2J")
			e.frame = edFrame{}
		case KCtrlC:
			if len(buf) > 0 {
				buf, pos = nil, 0
				break
			}
			if ctrlC {
				if boxed {
					e.eraseFrame()
				} else {
					uiWrite("\r\x1b[K")
				}
				return "", ErrInterrupt
			}
			ctrlC = true
			if boxed {
				e.note = "ctrl+c de novo pra sair"
			} else {
				above(e.ui.T.fg(cGray, 90) + "  (Ctrl+C de novo pra sair)" + e.ui.T.sgr("0") + "\n")
			}
		case KCtrlD:
			if len(buf) == 0 {
				if boxed {
					e.eraseFrame()
				} else {
					uiWrite("\r\x1b[K")
				}
				return "", ErrEOF
			}
			if pos < len(buf) {
				buf = append(buf[:pos], buf[pos+1:]...)
			}
		case KEsc:
			// ESC at the prompt clears the line
			buf, pos = nil, 0
			e.help = false
		}
		s := string(buf)
		if s != e.menuFor {
			e.menuFor, e.menuSel, e.menuOff = s, 0, false
		}
		if k.Code != KUp && k.Code != KDown && s != before {
			e.hist.reset() // edited: the next Up starts a new walk from this text
		}
		if edCoalesce && len(e.ui.keys.Keys) > 0 && k.Code != KEnter {
			continue // more keys already queued (fast typing on a busy CPU): one redraw for all
		}
		draw()
	}
}

// expandPastes puts the real text back in place of collapsed paste markers.
func (e *Editor) expandPastes(s string) string {
	for mark, text := range e.pastes {
		s = strings.ReplaceAll(s, mark, text)
	}
	return s
}

// tab completes slash commands at the start of the line, or the @path / path
// token under the cursor (G17).
func (e *Editor) tab(buf []rune, pos int) ([]rune, int) {
	if e.complete != nil && len(buf) > 0 && buf[0] == '/' && !strings.ContainsRune(string(buf), ' ') {
		opts := e.complete(string(buf))
		if len(opts) == 1 {
			buf = []rune(opts[0] + " ")
			return buf, len(buf)
		} else if len(opts) > 1 {
			sort.Strings(opts)
			e.listOptions(opts)
			if cp := commonPrefix(opts); len(cp) > len(string(buf)) {
				buf = []rune(cp)
				return buf, len(buf)
			}
		}
		return buf, pos
	}
	start := pos
	for start > 0 && !unicode.IsSpace(buf[start-1]) {
		start--
	}
	tok := string(buf[start:pos])
	at := strings.HasPrefix(tok, "@")
	if !at && !strings.Contains(tok, "/") {
		return buf, pos
	}
	prefix := strings.TrimPrefix(tok, "@")
	opts := e.pathMatches(prefix)
	if len(opts) == 0 {
		return buf, pos
	}
	repl := ""
	switch {
	case len(opts) == 1:
		repl = opts[0]
		if !strings.HasSuffix(repl, "/") {
			repl += " "
		}
	default:
		e.listOptions(opts)
		cp := commonPrefix(opts)
		if len(cp) <= len(prefix) || !strings.HasPrefix(cp, prefix) {
			return buf, pos
		}
		repl = cp
	}
	if at {
		repl = "@" + repl
	}
	nb := append(append(append([]rune{}, buf[:start]...), []rune(repl)...), buf[pos:]...)
	return nb, start + utf8.RuneCountInString(repl)
}

func (e *Editor) listOptions(opts []string) {
	const maxShow = 24
	shown := opts
	extra := ""
	if len(shown) > maxShow {
		shown = shown[:maxShow]
		extra = fmt.Sprintf("  (+%d)", len(opts)-maxShow)
	}
	line := strings.Join(shown, "  ")
	var b strings.Builder
	w := termWidth() - 3
	for _, l := range uiWrapWords(sanitizeTerm(line)+extra, w) {
		b.WriteString("  " + l + "\n")
	}
	uiWrite("\r\x1b[K" + e.ui.T.fg(cGray, 90) + b.String() + e.ui.T.sgr("0"))
}

// uiWrapWords wraps at spaces to at most w runes per line.
func uiWrapWords(s string, w int) []string {
	w = max(w, 10)
	var out []string
	line := ""
	for _, f := range strings.Split(s, " ") {
		if line != "" && uiStrW(line)+1+uiStrW(f) > w {
			out = append(out, line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += f
	}
	return append(out, line)
}

// pathMatches lists completions for a path prefix. Relative prefixes use the
// cached project file list; absolute, ~ and ../ prefixes read the directory.
func (e *Editor) pathMatches(prefix string) []string {
	root := e.Root
	if root == "" {
		root, _ = os.Getwd()
	}
	if strings.HasPrefix(prefix, "/") || strings.HasPrefix(prefix, "~") || strings.HasPrefix(prefix, "../") {
		return uiDirMatches(root, prefix)
	}
	p := strings.TrimPrefix(prefix, "./")
	dot := len(prefix) - len(p)
	var out []string
	for _, f := range e.files.list(root) {
		if strings.HasPrefix(f, p) {
			out = append(out, prefix[:dot]+f)
		}
	}
	if len(out) == 0 && p != "" && !strings.Contains(p, "/") { // fall back to base-name matches
		for _, f := range e.files.list(root) {
			if !strings.HasSuffix(f, "/") && strings.HasPrefix(filepath.Base(f), p) {
				out = append(out, f)
				if len(out) >= 200 {
					break
				}
			}
		}
	}
	// keep one level: "src/" instead of every file below it
	if len(out) > 1 {
		seen := map[string]bool{}
		var lvl []string
		for _, o := range out {
			if !strings.HasPrefix(o, prefix) {
				// base-name match: keep as is
			} else if o == prefix {
				continue // the directory being completed itself
			} else if rest := o[len(prefix):]; strings.Contains(strings.TrimSuffix(rest, "/"), "/") {
				o = o[:len(prefix)+strings.Index(rest, "/")+1]
			}
			if !seen[o] {
				seen[o] = true
				lvl = append(lvl, o)
			}
		}
		out = lvl
	}
	sort.Strings(out)
	return out
}

func uiDirMatches(root, prefix string) []string {
	dir, base := filepath.Split(prefix)
	real := dir
	if strings.HasPrefix(real, "~") {
		h, _ := os.UserHomeDir()
		real = h + strings.TrimPrefix(real, "~")
	}
	if real == "" {
		real = "."
	}
	if !filepath.IsAbs(real) {
		real = filepath.Join(root, real)
	}
	ents, err := os.ReadDir(real)
	if err != nil {
		return nil
	}
	var out []string
	for _, en := range ents {
		n := en.Name()
		if !strings.HasPrefix(n, base) || (strings.HasPrefix(n, ".") && !strings.HasPrefix(base, ".")) {
			continue
		}
		if en.IsDir() {
			n += "/"
		}
		out = append(out, dir+n)
	}
	sort.Strings(out)
	return out
}

// uiFileCache holds the project's file list for @path completion: git ls-files,
// else fd/fdfind, else a capped WalkDir. Refreshed at most every 30 s.
type uiFileCache struct {
	mu    sync.Mutex
	root  string
	at    time.Time
	paths []string // files and their parent dirs ("dir/"), sorted
}

const uiWalkCap = 20000

func (c *uiFileCache) list(root string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.root == root && time.Since(c.at) < 30*time.Second {
		return c.paths
	}
	files := uiListFiles(root)
	set := map[string]bool{}
	for _, f := range files {
		set[f] = true
		for d := filepath.Dir(f); d != "." && d != "/" && d != ""; d = filepath.Dir(d) {
			if set[d+"/"] {
				break
			}
			set[d+"/"] = true
		}
	}
	c.paths = c.paths[:0]
	for p := range set {
		c.paths = append(c.paths, p)
	}
	sort.Strings(c.paths)
	c.root, c.at = root, time.Now()
	return c.paths
}

func uiListFiles(root string) []string {
	run := func(name string, args ...string) []string {
		if _, err := exec.LookPath(name); err != nil {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = root
		out, err := cmd.Output()
		if err != nil {
			return nil
		}
		var files []string
		for _, l := range strings.Split(string(out), "\n") {
			if l = strings.TrimPrefix(strings.TrimSpace(l), "./"); l != "" {
				files = append(files, filepath.ToSlash(l))
				if len(files) >= uiWalkCap {
					break
				}
			}
		}
		return files
	}
	if fs := run("git", "ls-files", "-co", "--exclude-standard"); len(fs) > 0 {
		return fs
	}
	for _, fd := range []string{"fd", "fdfind"} {
		if fs := run(fd, "--type", "f", "--hidden", "--exclude", ".git", "--max-results", fmt.Sprint(uiWalkCap)); len(fs) > 0 {
			return fs
		}
	}
	var files []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", ".hg", ".svn", "vendor", "__pycache__", ".venv":
				if p != root {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if rel, err := filepath.Rel(root, p); err == nil {
			files = append(files, filepath.ToSlash(rel))
		}
		if len(files) >= uiWalkCap {
			return filepath.SkipAll
		}
		return nil
	})
	return files
}

// reverseSearch is Ctrl-R: incremental search back through the history.
// Enter (or any editing key) accepts the match, Esc/Ctrl+C/Ctrl+G cancel.
func (e *Editor) reverseSearch(orig string) (string, bool) {
	t := e.ui.T
	query := []rune{}
	idx := len(e.history)
	match := ""
	find := func(from int) {
		q := strings.ToLower(string(query))
		for i := from; i >= 0; i-- {
			if q == "" || strings.Contains(strings.ToLower(e.history[i]), q) {
				idx, match = i, e.history[i]
				return
			}
		}
	}
	draw := func() {
		m := sanitizeTerm(strings.ReplaceAll(match, "\n", t.G().nl))
		if match == "" && len(query) > 0 {
			m = t.fg(cGray, 90) + "nada encontrado" + t.sgr("0")
		}
		line := t.fg(cGray, 90) + "(busca) '" + t.sgr("0") + sanitizeTerm(string(query)) + t.fg(cGray, 90) + "': " + t.sgr("0") + m
		w := termWidth() - 1
		hint := t.fg(cGray, 90) + "  ctrl+r anterior " + t.G().dot + " enter usa " + t.G().dot + " esc cancela" + t.sgr("0")
		if uiVisLen(line)+uiVisLen(hint) <= w {
			line += hint
		}
		uiWrite("\r\x1b[K" + clipVisible(line, w))
	}
	if len(e.history) > 0 {
		find(len(e.history) - 1)
	}
	draw()
	for k := range e.ui.keys.Keys {
		switch k.Code {
		case KRune, KPaste, KBackspace:
			switch {
			case k.Code == KRune:
				query = append(query, k.R)
			case k.Code == KPaste:
				query = append(query, []rune(k.Text)...)
			case len(query) > 0:
				query = query[:len(query)-1]
			}
			match, idx = "", len(e.history) // a new query: no stale match
			find(len(e.history) - 1)
		case KCtrlR:
			if idx > 0 {
				prev := match
				find(idx - 1)
				if match == prev && idx > 0 { // skip duplicates
					find(idx - 1)
				}
			}
		case KEsc, KCtrlC, KCtrlG:
			return orig, false
		case KCtrlD:
			return orig, false
		default:
			if match == "" {
				return orig, false
			}
			return match, true
		}
		draw()
	}
	return orig, false
}

// externalEdit is Ctrl-G: the buffer goes to $VISUAL/$EDITOR (fallback nano,
// then vi) with the terminal restored, and comes back edited.
func (e *Editor) externalEdit(s string) (string, error) {
	argv := uiEditorCmd()
	if len(argv) == 0 {
		return s, errors.New("nenhum editor encontrado (defina $EDITOR)")
	}
	f, err := os.CreateTemp("", "jazz-*.md")
	if err != nil {
		return s, err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(s); err != nil {
		f.Close()
		return s, err
	}
	f.Close()
	uiWrite("\r\x1b[K")
	resume := func() {}
	if e.ui.keys != nil {
		resume = e.ui.keys.Pause()
	}
	// Ctrl+C / Ctrl+\ inside the editor reach our process group too while the
	// terminal is cooked: catch them here (the child still gets default handlers).
	sigc := make(chan os.Signal, 4)
	signal.Notify(sigc, os.Interrupt, syscall.SIGQUIT)
	defer signal.Stop(sigc)
	var runErr error
	uiCooked(func() {
		cmd := exec.Command(argv[0], append(argv[1:], f.Name())...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		runErr = cmd.Run()
	})
	resume()
	if runErr != nil {
		return s, runErr
	}
	b, err := os.ReadFile(f.Name())
	if err != nil {
		return s, err
	}
	return strings.TrimRight(string(b), "\n"), nil
}

func uiEditorCmd() []string {
	for _, v := range []string{"VISUAL", "EDITOR"} {
		if f := strings.Fields(os.Getenv(v)); len(f) > 0 {
			return f
		}
	}
	names := []string{"nano", "vi"}
	if runtime.GOOS == "windows" {
		names = []string{"notepad"} // blocks until the window closes, unlike code/start
	}
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return []string{p}
		}
	}
	return nil
}

func commonPrefix(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	p := ss[0]
	for _, s := range ss[1:] {
		for !strings.HasPrefix(s, p) {
			_, n := utf8.DecodeLastRuneInString(p) // never cut inside a rune (bug 56)
			p = p[:len(p)-n]
		}
	}
	return p
}

// ---------- @path mentions (G17) ----------

var uiMentionRe = regexp.MustCompile(`(?:^|\s)@(\S+)`)

var uiImageMime = map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp"}

const (
	uiMentionMaxFile  = 50 << 10
	uiMentionMaxImage = 5 << 20
	uiMentionMaxDir   = 200
)

// ExpandMentions expands @path tokens in a submitted line: small files become
// <file> blocks (and are stamped read in t), directories a listing, images
// attachments. Called by main before Agent.Run. Tokens that are not existing
// paths (e-mail addresses, @decorators, handles) are left alone.
func ExpandMentions(t *Toolbox, line string) (text string, images []ImagePart) {
	if t == nil || !strings.Contains(line, "@") {
		return line, nil
	}
	root := t.Root
	if root == "" {
		root, _ = os.Getwd()
	}
	var blocks []string
	seen := map[string]bool{}
	for _, m := range uiMentionRe.FindAllStringSubmatch(line, -1) {
		abs, shown, fi := uiResolveMention(root, m[1])
		if fi == nil || seen[abs] {
			continue
		}
		seen[abs] = true
		switch {
		case fi.IsDir():
			blocks = append(blocks, uiDirBlock(abs, shown))
		case uiImageMime[strings.ToLower(filepath.Ext(abs))] != "":
			if fi.Size() > uiMentionMaxImage {
				blocks = append(blocks, fmt.Sprintf("<file path=%q>Image too large to attach (%d bytes).</file>", shown, fi.Size()))
				continue
			}
			data, err := os.ReadFile(abs)
			if err != nil {
				continue
			}
			images = append(images, ImagePart{Mime: uiImageMime[strings.ToLower(filepath.Ext(abs))], Data: base64.StdEncoding.EncodeToString(data)})
			blocks = append(blocks, fmt.Sprintf("<image path=%q>attached</image>", shown))
		case !fi.Mode().IsRegular():
			continue
		case fi.Size() > uiMentionMaxFile:
			blocks = append(blocks, fmt.Sprintf("<file path=%q>File too large to inline (%d bytes); use the read tool with offset/limit.</file>", shown, fi.Size()))
		default:
			data, err := os.ReadFile(abs)
			if err != nil {
				continue
			}
			if bytes.IndexByte(data[:min(len(data), 8192)], 0) >= 0 {
				blocks = append(blocks, fmt.Sprintf("<file path=%q>Binary file (%d bytes), not shown.</file>", shown, len(data)))
				continue
			}
			t.MarkRead(abs, data)
			blocks = append(blocks, uiFileBlock(shown, data))
		}
	}
	if len(blocks) == 0 {
		return line, images
	}
	return strings.Join(blocks, "\n") + "\n\n" + line, images
}

// uiResolveMention finds the path a mention points to; trailing punctuation
// ("veja @main.go," / "(@src/)") is dropped when the literal path does not exist.
func uiResolveMention(root, tok string) (abs, shown string, fi os.FileInfo) {
	for _, cand := range []string{tok, strings.TrimRight(tok, ".,;:!?)]}'\"`")} {
		if cand == "" {
			continue
		}
		p := cand
		if strings.HasPrefix(p, "~/") {
			h, _ := os.UserHomeDir()
			p = filepath.Join(h, p[2:])
		} else if !filepath.IsAbs(p) {
			p = filepath.Join(root, p)
		}
		p = filepath.Clean(p)
		if st, err := os.Stat(p); err == nil {
			shown := cand
			if r, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(r, "..") {
				shown = filepath.ToSlash(r)
			}
			return p, shown, st
		}
	}
	return "", "", nil
}

func uiFileBlock(shown string, data []byte) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<file path=%q>\n", shown)
	s := strings.TrimSuffix(string(data), "\n")
	for i, l := range strings.Split(s, "\n") {
		fmt.Fprintf(&b, "%6d\t%s\n", i+1, strings.TrimSuffix(l, "\r"))
	}
	b.WriteString("</file>")
	return b.String()
}

func uiDirBlock(abs, shown string) string {
	ents, _ := os.ReadDir(abs)
	var b strings.Builder
	fmt.Fprintf(&b, "<dir path=%q>\n", shown)
	for i, en := range ents {
		if i >= uiMentionMaxDir {
			fmt.Fprintf(&b, "... (+%d more)\n", len(ents)-i)
			break
		}
		n := en.Name()
		if en.IsDir() {
			n += "/"
		}
		b.WriteString(n + "\n")
	}
	b.WriteString("</dir>")
	return b.String()
}
