package main

// lessons.go: the lessons store of `jazz improve` (docs/improve.md).
//
// Two plain-markdown files, AGENTS.md-compatible and meant to be read and
// edited by people:
//   - project: <repo root>/.jazz/lessons.md — lessons about this project;
//   - global:  configDir()/lessons.md      — tool and harness lessons
//     ("provider X cuts tool calls at 16k output").
//
// One lesson per line: "- [tag] text (sessão <id>)". Everything after the
// "## Histórico" heading is the changelog (date, session, how it was applied)
// and is never injected; prose lines are never injected either.
//
// Injection: the lesson lines are read ONCE, when the system prompt is built
// at session start, and rendered right after the AGENTS.md block (through
// acSteeringPrompt). They never change mid-session, and the same lessons give a
// byte-identical block, so the cross-session prompt prefix only changes when a
// lesson changes. A resumed session keeps the system prompt it was born with.
//
// Lessons are project text with system-prompt reach, like AGENTS.md: read with
// pfSafeRead (no symlinks out of the project), secrets redacted, invisible
// text stripped, and the block wrapped as untrusted when it reads like an
// injection. Writes only ever come from `jazz improve` (proposal → --apply, or
// improve.auto for project lessons, capped) and are recorded in the audit log.
//
// Switch: --lessons on|off > JAZZ_LESSONS > config "lessons" > on.

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

const (
	lsFileName    = "lessons.md"
	lsMaxLessons  = 40       // lesson lines read (and written) per file
	lsMaxLine     = 300      // bytes per lesson line in the prompt
	lsMaxFile     = 64 << 10 // bigger files are not lesson files
	lsHistoryHead = "## Histórico"
)

// lsLesson is one parsed lesson line.
type lsLesson struct {
	Tag     string // without brackets ("" = none)
	Text    string // the lesson itself, without tag and evidence
	Session string // evidence: the session id ("" = none)
}

// lsLine renders the canonical line of a lesson.
func (l lsLesson) lsLine() string {
	var b strings.Builder
	b.WriteString("- ")
	if l.Tag != "" {
		b.WriteString("[" + l.Tag + "] ")
	}
	b.WriteString(l.Text)
	if l.Session != "" {
		b.WriteString(" (sessão " + l.Session + ")")
	}
	return b.String()
}

var (
	lsTagRE     = regexp.MustCompile(`^\[([^\]\n]{1,24})\]\s*`)
	lsSessRE    = regexp.MustCompile(`\s*\((?:sessão|sessao|session)\s+([A-Za-z0-9_.@-]{4,40})\)\s*$`)
	lsBulletRE  = regexp.MustCompile(`^\s{0,3}[-*]\s+`)
	lsHeadingRE = regexp.MustCompile(`^\s{0,3}#{1,6}\s`)
)

// lsParseLine parses "- [tag] text (sessão id)"; ok=false for non-lesson lines.
func lsParseLine(line string) (lsLesson, bool) {
	m := lsBulletRE.FindStringIndex(line)
	if m == nil {
		return lsLesson{}, false
	}
	s := strings.TrimSpace(line[m[1]:])
	var l lsLesson
	if t := lsTagRE.FindStringSubmatch(s); t != nil {
		l.Tag = strings.TrimSpace(t[1])
		s = s[len(t[0]):]
	}
	if e := lsSessRE.FindStringSubmatch(s); e != nil {
		l.Session = e[1]
		s = s[:len(s)-len(e[0])]
	}
	l.Text = strings.TrimSpace(s)
	return l, l.Text != ""
}

// lsParse returns the lessons of a file's text (changelog excluded, capped).
func lsParse(text string) []lsLesson {
	var out []lsLesson
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if lsIsHistory(line) {
			break
		}
		if l, ok := lsParseLine(line); ok {
			out = append(out, l)
			if len(out) >= lsMaxLessons {
				break
			}
		}
	}
	return out
}

func lsIsHistory(line string) bool {
	t := strings.ToLower(strings.TrimSpace(line))
	return lsHeadingRE.MatchString(line) && (strings.Contains(t, "histórico") || strings.Contains(t, "historico") || strings.Contains(t, "changelog"))
}

// ---------- paths and switch ----------

// lsProjectRoot is the repository root of root (the git top level, else root).
func lsProjectRoot(root string) string {
	if top := gitRoot(root); top != "" {
		return top
	}
	return root
}

func lsProjectPath(root string) string {
	return filepath.Join(lsProjectRoot(root), ".jazz", lsFileName)
}

func lsGlobalPath() string { return filepath.Join(configDir(), lsFileName) }

// lsState is the session-wide switch, set once by main (lsConfigure).
var lsState atomic.Int32 // 0 = not configured (on), 1 = on, 2 = off

// lsConfigure applies --lessons, JAZZ_LESSONS and config "lessons".
func lsConfigure(cfg *Config, flagVal string) error {
	v := strings.TrimSpace(flagVal)
	if v == "" {
		v = strings.TrimSpace(os.Getenv("JAZZ_LESSONS"))
	}
	if v == "" && cfg != nil {
		v = strings.TrimSpace(cfg.Lessons)
	}
	switch strings.ToLower(v) {
	case "", "on", "1", "true", "yes":
		lsState.Store(1)
	case "off", "0", "false", "no", "none":
		lsState.Store(2)
	default:
		return errors.New("--lessons deve ser on ou off")
	}
	return nil
}

func lsEnabled() bool { return lsState.Load() != 2 }

// lsFlag registers --lessons (validated by lsConfigure after config loads).
func lsFlag(fs *flag.FlagSet) *string { return fs.String("lessons", "", "") }

// lsValuedFlags are the flags of this file that take a separate value.
var lsValuedFlags = map[string]bool{"--lessons": true, "-lessons": true}

// ---------- reading ----------

// lsReadFile reads a lessons file; within != "" keeps project files inside it.
func lsReadFile(path, within string) (string, bool) {
	b, ok := pfSafeRead(path, within, lsMaxFile)
	if !ok {
		return "", false
	}
	return string(b), true
}

// lsLoad returns the project and global lessons of root.
func lsLoad(root string) (project, global []lsLesson) {
	top := lsProjectRoot(root)
	if s, ok := lsReadFile(filepath.Join(top, ".jazz", lsFileName), top); ok {
		project = lsParse(s)
	}
	if s, ok := lsReadFile(lsGlobalPath(), ""); ok {
		global = lsParse(s)
	}
	return project, global
}

// lsPromptBlock is the session-start block with the lessons ("" = none or
// off). It depends only on the lesson lines, never on time or the task, so
// two sessions with the same lessons get the same bytes.
func lsPromptBlock(root string) string {
	if !lsEnabled() {
		return ""
	}
	project, global := lsLoad(root)
	return lsRender(project, global)
}

func lsRender(project, global []lsLesson) string {
	if len(project)+len(global) == 0 {
		return ""
	}
	var body strings.Builder
	section := func(title string, ls []lsLesson) {
		if len(ls) == 0 {
			return
		}
		body.WriteString(title + "\n")
		for _, l := range ls {
			line := l.lsLine()
			if len(line) > lsMaxLine {
				line = strings.ToValidUTF8(line[:lsMaxLine], "") + "…"
			}
			body.WriteString(line + "\n")
		}
	}
	section("Project (.jazz/lessons.md):", project)
	section("Harness and tools (global):", global)
	s, rep := sanitizeForModel(redactSecrets(body.String()), snMode(nil))
	block := "<lessons>\nLessons learned from earlier measured runs (written by `jazz improve` from session evidence, reviewable in the files named below). Use them as hints: the user's request and the project instructions win, and a lesson that does not fit the current task is ignored.\n" + s + "</lessons>\n"
	label := injScan(s)
	if label == "" {
		label = rep.suspect()
	}
	if label != "" {
		block = fmt.Sprintf("<untrusted suspected_injection=%q>\n%s</untrusted>\n", label, block)
	}
	return block
}

// ---------- writing ----------

const lsHeader = `# Lições do jazz

Uma lição por linha: ` + "`- [tag] texto (sessão <id>)`" + `. Escritas por ` + "`jazz improve`" + ` a partir de
execuções medidas; edite ou apague à vontade. O jazz lê só as linhas de lição (até 40), uma vez no
início de cada sessão. O histórico abaixo registra quando e de onde cada lição veio.

`

// lsAppend adds lessons to the file at path (created when missing) and a
// changelog line for each. via is "apply" or "auto". It returns the lessons
// actually written (duplicates and lessons past the cap are skipped).
func lsAppend(path string, add []lsLesson, via string, now time.Time) ([]lsLesson, error) {
	old := ""
	if b, err := os.ReadFile(path); err == nil {
		if len(b) > lsMaxFile {
			return nil, fmt.Errorf("%s passou de %d KB: limpe-o antes", path, lsMaxFile>>10)
		}
		old = strings.ReplaceAll(string(b), "\r\n", "\n")
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	have := lsParse(old)
	var wrote []lsLesson
	for _, l := range add {
		if len(have) >= lsMaxLessons {
			break
		}
		if imDuplicate(l.Text, have) {
			continue
		}
		have = append(have, l)
		wrote = append(wrote, l)
	}
	if len(wrote) == 0 {
		return nil, nil
	}
	text := old
	if strings.TrimSpace(text) == "" {
		text = lsHeader
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	hist := -1
	lastLesson := -1
	for i, line := range lines {
		if lsIsHistory(line) {
			hist = i
			break
		}
		if _, ok := lsParseLine(line); ok {
			lastLesson = i
		}
	}
	var ins []string
	for _, l := range wrote {
		ins = append(ins, l.lsLine())
	}
	at := len(lines) // no history: lessons go at the end
	switch {
	case lastLesson >= 0:
		at = lastLesson + 1
	case hist >= 0:
		at = hist
		ins = append(ins, "")
	}
	if lastLesson < 0 && at > 0 && strings.TrimSpace(lines[at-1]) != "" {
		ins = append([]string{""}, ins...) // first lesson: a blank line after the prose
	}
	lines = append(lines[:at], append(ins, lines[at:]...)...)
	if hist < 0 {
		lines = append(lines, "", lsHistoryHead, "")
	} else if hist >= at {
		hist += len(ins)
	}
	for _, l := range wrote {
		entry := fmt.Sprintf("- %s · sessão %s · %s · %s", now.Format("2006-01-02"), orDash(l.Session), via, strings.TrimPrefix(l.lsLine(), "- "))
		lines = append(lines, entry)
	}
	out := strings.Join(lines, "\n") + "\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(out), 0o644); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, err
	}
	for _, l := range wrote {
		auditRec("lesson.applied", map[string]any{"path": path, "via": via, "tag": l.Tag, "lesson": oneLine(l.Text, 300), "session": l.Session, "sha256": obSHA(l.lsLine())})
	}
	return wrote, nil
}
