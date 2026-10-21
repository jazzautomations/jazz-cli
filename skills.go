package main

// skills.go: built-in skills picked by the harness, not by the model
// (docs/research/skills-personas-scripts.md B+C, bodies from skill-bodies.md).
//
// Thirteen short skills are embedded in the binary. The model never sees a list
// of them: a cheap scorer (keyword regexes in PT and EN over the user message,
// file globs of edited paths, signals parsed from tool output, the phase of
// the turn) picks at most one per turn, and its body is appended once as a
// <system-reminder> to the user message (keyword triggers) or to the tool
// result that triggered it (signal triggers). The system prompt never changes,
// so the prompt cache keeps hitting. The history is the record of what was
// injected: a skill that left the history (compaction, rewind) can fire again,
// and the skill of the current turn is re-injected when compaction drops it.
//
// Config: "skills": "auto" (default) | "manual" (only /skill <name>) | "off",
// or the object form {"mode","claude_dir","threshold","per_turn","per_session"}.
// JAZZ_SKILLS=off|auto|manual overrides the mode (A/B runs).

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

//go:embed skills/*/SKILL.md skills/triggers.json
var skFS embed.FS

// SkillsConf is the "skills" config entry. It also accepts a bare string
// ("off", "auto", "manual") as a shorthand for the mode.
type SkillsConf struct {
	Mode       string `json:"mode,omitempty"`        // auto (default) | manual | off
	ClaudeDir  bool   `json:"claude_dir,omitempty"`  // also list ~/.claude/skills in the prompt (default false)
	Threshold  int    `json:"threshold,omitempty"`   // minimum trigger score (default 3)
	PerTurn    int    `json:"per_turn,omitempty"`    // skills injected per turn (default 1)
	PerSession int    `json:"per_session,omitempty"` // skills in the history before compaction (default 3)
}

func (s *SkillsConf) UnmarshalJSON(b []byte) error {
	var mode string
	if json.Unmarshal(b, &mode) == nil {
		*s = SkillsConf{Mode: mode}
		return nil
	}
	type plain SkillsConf
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*s = SkillsConf(p)
	return nil
}

const (
	skDefaultThreshold  = 3
	skDefaultPerTurn    = 1
	skDefaultPerSession = 3
	skMaxBodyTokens     = 400 // enforced by TestSkillsBodiesFitBudget
	skScanBytes         = 8 << 10
)

// skBodyBudget is the token cap of one skill body. frontend-craft carries the
// modes, the reflexes and the bans in one reminder and only fires on UI work,
// so it gets twice the budget of the process skills.
func skBodyBudget(name string) int {
	if name == "frontend-craft" {
		return 2 * skMaxBodyTokens
	}
	return skMaxBodyTokens
}

// skModeOf is the effective skills mode: JAZZ_SKILLS, then config, then auto.
func skModeOf(c *Config) string {
	m := strings.ToLower(strings.TrimSpace(os.Getenv("JAZZ_SKILLS")))
	if m == "" && c != nil {
		m = strings.ToLower(strings.TrimSpace(c.Skills.Mode))
	}
	switch m {
	case "off", "manual":
		return m
	}
	return "auto"
}

func skLimits(c *Config) (threshold, perTurn, perSession int) {
	threshold, perTurn, perSession = skDefaultThreshold, skDefaultPerTurn, skDefaultPerSession
	if c == nil {
		return
	}
	if c.Skills.Threshold > 0 {
		threshold = c.Skills.Threshold
	}
	if c.Skills.PerTurn > 0 {
		perTurn = c.Skills.PerTurn
	}
	if c.Skills.PerSession > 0 {
		perSession = c.Skills.PerSession
	}
	return
}

// skClaudeDir mirrors Config.Skills.ClaudeDir for the prompt builder, which has
// no config (set by EnsureSystem before the prompt is built).
var skClaudeDir atomic.Bool

func skConfigure(c *Config) { skClaudeDir.Store(c != nil && c.Skills.ClaudeDir) }

// skBuiltin is one embedded skill with its compiled triggers.
type skBuiltin struct {
	Name, Description, Body string
	Priority, Min           int
	kw, kwWeak, neg, cmd    []*regexp.Regexp
	files                   []string
	signals, phases         map[string]bool
}

type skTriggerJSON struct {
	Name     string   `json:"name"`
	KW       []string `json:"kw"`
	KWWeak   []string `json:"kw_weak"`
	Neg      []string `json:"neg"`
	Files    []string `json:"files"`
	Cmd      []string `json:"cmd"`
	Signals  []string `json:"signals"`
	Phase    []string `json:"phase"`
	Priority int      `json:"priority"`
	Min      int      `json:"min"`
}

var (
	skOnce sync.Once
	skAll  []*skBuiltin
	skErr  error
)

// skBuiltins loads and compiles the embedded skills once (lazily: nothing is
// compiled when skills are off).
func skBuiltins() ([]*skBuiltin, error) {
	skOnce.Do(func() { skAll, skErr = skLoad() })
	return skAll, skErr
}

func skLoad() ([]*skBuiltin, error) {
	raw, err := skFS.ReadFile("skills/triggers.json")
	if err != nil {
		return nil, err
	}
	var trig []skTriggerJSON
	if err := json.Unmarshal(raw, &trig); err != nil {
		return nil, fmt.Errorf("skills/triggers.json: %w", err)
	}
	compile := func(name string, pats []string) ([]*regexp.Regexp, error) {
		var out []*regexp.Regexp
		for _, p := range pats {
			re, err := regexp.Compile("(?i)" + p)
			if err != nil {
				return nil, fmt.Errorf("skill %s: %w", name, err)
			}
			out = append(out, re)
		}
		return out, nil
	}
	set := func(xs []string) map[string]bool {
		m := map[string]bool{}
		for _, x := range xs {
			m[x] = true
		}
		return m
	}
	var out []*skBuiltin
	for _, t := range trig {
		b, err := skFS.ReadFile("skills/" + t.Name + "/SKILL.md")
		if err != nil {
			return nil, fmt.Errorf("skill %s: %w", t.Name, err)
		}
		name, desc, body := skParse(string(b))
		if name != t.Name || body == "" {
			return nil, fmt.Errorf("skill %s: bad SKILL.md (name %q)", t.Name, name)
		}
		s := &skBuiltin{Name: name, Description: desc, Body: body, Priority: t.Priority, Min: t.Min,
			files: t.Files, signals: set(t.Signals), phases: set(t.Phase)}
		if s.kw, err = compile(name, t.KW); err != nil {
			return nil, err
		}
		if s.kwWeak, err = compile(name, t.KWWeak); err != nil {
			return nil, err
		}
		if s.neg, err = compile(name, t.Neg); err != nil {
			return nil, err
		}
		if s.cmd, err = compile(name, t.Cmd); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// skParse splits a SKILL.md into its frontmatter name/description and body.
func skParse(s string) (name, desc, body string) {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") {
		return "", "", strings.TrimSpace(s)
	}
	end := strings.Index(s[4:], "\n---")
	if end < 0 {
		return "", "", ""
	}
	for _, l := range strings.Split(s[4:4+end], "\n") {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch strings.TrimSpace(k) {
		case "name":
			name = v
		case "description":
			desc = v
		}
	}
	rest := s[4+end+4:]
	return name, desc, strings.TrimSpace(rest)
}

// skFind returns the built-in skill called name (nil if none).
func skFind(name string) *skBuiltin {
	all, _ := skBuiltins()
	name = strings.ToLower(strings.TrimSpace(name))
	for _, s := range all {
		if s.Name == name {
			return s
		}
	}
	return nil
}

// skEstTokens is a conservative token estimate for English prose.
func skEstTokens(s string) int {
	w := len(strings.Fields(s)) * 13 / 10
	c := len(s) / 4
	return max(w, c)
}

// skSignals is what the scorer sees for one decision point.
type skSignals struct {
	Input string          // user text (turn start only)
	Files []string        // paths mentioned (turn start) or edited (tool result)
	Cmd   string          // bash command (tool result)
	Flags map[string]bool // parsed from tool output: exit_nonzero, stacktrace, test_fail, git_conflict, diff_large, long_ask
	Phase string          // explore | edit | verify
	Kind  string          // task kind from the budget classifier (turn start only; budget.go)
}

// skCand is one scored skill (for picking and the debug log).
type skCand struct {
	s     *skBuiltin
	score int
}

// skScore scores one skill. Weights: each strong keyword regex 3 (max 2
// counted), each weak keyword 2, a file glob 2, a command regex 3, each tool
// signal 3 (a bare nonzero exit 1), and the phase +1 only when something else
// already matched (a phase alone never triggers).
func skScore(s *skBuiltin, sig skSignals) int {
	score := 0
	if sig.Input != "" {
		neg := false
		for _, re := range s.neg {
			if re.MatchString(sig.Input) {
				neg = true
				break
			}
		}
		if !neg {
			hits := 0
			for _, re := range s.kw {
				if re.MatchString(sig.Input) {
					hits++
				}
			}
			score += 3 * min(hits, 2)
			for _, re := range s.kwWeak {
				if re.MatchString(sig.Input) {
					score += 2
				}
			}
		}
	}
	if len(s.files) > 0 {
	files:
		for _, f := range sig.Files {
			base := path.Base(filepath.ToSlash(f))
			for _, g := range s.files {
				if ok, _ := path.Match(g, base); ok {
					score += 2
					break files
				}
			}
		}
	}
	if sig.Cmd != "" {
		for _, re := range s.cmd {
			if re.MatchString(sig.Cmd) {
				score += 3
				break
			}
		}
	}
	for f, on := range sig.Flags {
		if !on || !s.signals[f] {
			continue
		}
		if f == "exit_nonzero" {
			score++
		} else {
			score += 3
		}
	}
	if score > 0 && s.phases[sig.Phase] {
		score++
	}
	return score
}

// skRank scores every built-in and returns the candidates with a positive
// score, best first (ties: higher priority, then name).
func skRank(sig skSignals) []skCand {
	all, err := skBuiltins()
	if err != nil {
		return nil
	}
	var out []skCand
	for _, s := range all {
		// At turn start, debug-loop needs a bug report: an error word inside a
		// feature or refactor spec ("é erro de uso", "mesmas exceções") is not
		// one. Tool signals (failing test, trace) still pick it mid-turn.
		if s.Name == "debug-loop" && sig.Kind != "" && sig.Kind != "bugfix" {
			continue
		}
		if n := skScore(s, sig); n > 0 {
			out = append(out, skCand{s, n})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		if out[i].s.Priority != out[j].s.Priority {
			return out[i].s.Priority > out[j].s.Priority
		}
		return out[i].s.Name < out[j].s.Name
	})
	return out
}

// skPick returns the best-ranked candidate that clears its threshold (a
// skill's own "min" may raise it) and is not in skip.
func skPick(cands []skCand, threshold int, skip map[string]bool) *skBuiltin {
	for _, c := range cands {
		if c.score >= max(threshold, c.s.Min) && !skip[c.s.Name] {
			return c.s
		}
	}
	return nil
}

var (
	skExitRe     = regexp.MustCompile(`\[exit code [1-9][0-9]*\]`)
	skTraceRe    = regexp.MustCompile(`(?m)Traceback \(most recent call last\)|^panic: |^goroutine \d+ \[running\]|^\s+at \S.*\(\S+:\d+(:\d+)?\)$|^Exception in thread|^\s+File ".+", line \d+|^thread '.+' panicked at`)
	skTestFailRe = regexp.MustCompile(`(?m)^--- FAIL|^FAIL\s|\bFAILED\b|\b[1-9]\d* (failed|failing)\b|\bAssertionError\b|^\s*✕ |Tests:\s+[1-9]\d* failed|test result: FAILED`)
	skConflictRe = regexp.MustCompile(`(?m)^CONFLICT \(|^<<<<<<< |Automatic merge failed`)
	skPathRe     = regexp.MustCompile(`[\w./-]*\w\.[A-Za-z]{1,5}\b`)
)

// skOutputFlags parses the tail of a bash result into trigger signals.
func skOutputFlags(out string) map[string]bool {
	if len(out) > skScanBytes {
		out = out[len(out)-skScanBytes:]
	}
	f := map[string]bool{}
	if skExitRe.MatchString(out) {
		f["exit_nonzero"] = true
		// Traces and test failures only count when the command failed: a
		// passing run that prints "0 failed" or logs an old trace is no bug.
		if skTraceRe.MatchString(out) {
			f["stacktrace"] = true
		}
		if skTestFailRe.MatchString(out) {
			f["test_fail"] = true
		}
	}
	if skConflictRe.MatchString(out) {
		f["git_conflict"] = true
	}
	return f
}

// skInputPaths returns file-like words of the user message (foo_test.go, src/a.ts).
func skInputPaths(s string) []string {
	return skPathRe.FindAllString(s, 20)
}

// skState is the per-agent skill bookkeeping (Agent.sk).
type skState struct {
	forced    string // /skill <name> for the next turn
	turnSkill string // skill injected in the current turn ("" = none yet)
	turnCount int    // skills injected in the current turn
	injAt     int    // index in Msgs of the message carrying turnSkill
	edited    bool   // an edit/write succeeded in this turn
	ranAfter  bool   // bash ran after the last edit
	diffLines int    // lines written by edit/write in this turn
}

// skMarker starts the reminder of a skill; it is also how an injection already
// in the history is recognised.
func skMarker(name string) string {
	return "<system-reminder>Skill " + strconv.Quote(name) + " applies"
}

func skReminder(s *skBuiltin, why string) string {
	return skMarker(s.Name) + " (" + why + "). Follow it while it fits the task:\n" + s.Body + "\n</system-reminder>"
}

// skInjected returns the skills whose reminders are in the history.
func (a *Agent) skInjected() map[string]bool {
	all, _ := skBuiltins()
	got := map[string]bool{}
	const pre = "<system-reminder>Skill \""
	for _, m := range a.Msgs {
		if m.Role != "user" && m.Role != "tool" {
			continue
		}
		c := m.Content
		for {
			i := strings.Index(c, pre)
			if i < 0 {
				break
			}
			c = c[i+len(pre):]
			for _, s := range all {
				if strings.HasPrefix(c, s.Name+"\" applies") {
					got[s.Name] = true
				}
			}
		}
	}
	return got
}

func (a *Agent) skPhase() string {
	switch {
	case a.sk.ranAfter:
		return "verify"
	case a.sk.edited:
		return "edit"
	}
	return "explore"
}

func skLogCands(cands []skCand) []string {
	var out []string
	for i, c := range cands {
		if i == 4 {
			break
		}
		out = append(out, fmt.Sprintf("%s:%d", c.s.Name, c.score))
	}
	return out
}

// ForceSkill queues built-in skill name for the next turn (/skill <name>).
func (a *Agent) ForceSkill(name string) error {
	if skModeOf(a.cfg) == "off" {
		return fmt.Errorf("skills desligadas (config \"skills\": \"off\" ou JAZZ_SKILLS=off)")
	}
	s := skFind(name)
	if s == nil {
		return fmt.Errorf("skill desconhecida: %s (veja /skill)", name)
	}
	a.sk.forced = s.Name
	return nil
}

// skTurnInput starts the turn's skill bookkeeping and appends the skill picked
// from the user's message (or forced with /skill) to input.
func (a *Agent) skTurnInput(input string) string {
	a.sk.turnSkill, a.sk.turnCount = "", 0
	a.sk.edited, a.sk.ranAfter, a.sk.diffLines = false, false, 0
	if a.sub {
		return input
	}
	mode := skModeOf(a.cfg)
	if f := a.sk.forced; f != "" {
		a.sk.forced = ""
		if s := skFind(f); s != nil && mode != "off" {
			dbg("skill", map[string]any{"at": "turn", "pick": s.Name, "forced": true})
			a.sk.turnSkill, a.sk.turnCount, a.sk.injAt = s.Name, 1, len(a.Msgs)
			a.ui.Notice("skill aplicada: " + s.Name)
			return input + "\n\n" + skReminder(s, "requested by the user with /skill")
		}
	}
	if mode != "auto" || a.mode() == "plan" { // plan mode: skills that edit would fight the plan reminder
		return input
	}
	return input + a.skDecide("turn", skInputSignals(acStripReminders(input)))
}

// skInputSignals is what the scorer sees at the start of a turn: the user's
// text (first 8 KB), the file names it mentions and whether it is a long ask.
func skInputSignals(text string) skSignals {
	if len(text) > skScanBytes {
		text = text[:skScanBytes]
	}
	files := skInputPaths(text)
	kind := bgClassify(text).Kind
	text = bgNegatedRE.ReplaceAllString(text, " ") // "não refatore" is no refactor request
	return skSignals{Input: text, Files: files, Phase: "explore", Kind: kind,
		Flags: map[string]bool{"long_ask": len(text) >= 400 || len(files) >= 3}}
}

// skDecide picks a skill for sig under the per-turn and per-session caps and
// returns the reminder to append ("" = none), prefixed with a blank line.
func (a *Agent) skDecide(at string, sig skSignals) string {
	threshold, perTurn, perSession := skLimits(a.cfg)
	cands := skRank(sig)
	if len(cands) == 0 {
		return ""
	}
	log := map[string]any{"at": at, "cands": skLogCands(cands), "phase": sig.Phase}
	skip := func(why string) string {
		log["skip"] = why
		dbg("skill", log)
		return ""
	}
	if skPick(cands, threshold, nil) == nil {
		return skip("below_threshold")
	}
	if a.sk.turnCount >= perTurn {
		return skip("turn_cap")
	}
	done := a.skInjected()
	if len(done) >= perSession {
		return skip("session_cap")
	}
	s := skPick(cands, threshold, done)
	if s == nil {
		return skip("already_injected")
	}
	log["pick"] = s.Name
	dbg("skill", log)
	a.sk.turnSkill, a.sk.injAt = s.Name, len(a.Msgs)
	a.sk.turnCount++
	a.ui.Notice("skill aplicada: " + s.Name)
	why := "picked by jazz from the user's request"
	if at == "tool" {
		why = "picked by jazz from this tool result"
	}
	return "\n\n" + skReminder(s, why)
}

// skToolNote returns the skill reminder to append to the result of tc ("" =
// none): a skill triggered by this result, or the current turn's skill again
// when compaction dropped it from the history.
func (a *Agent) skToolNote(tc ToolCall, out ToolOutput) string {
	if a.sub || a.tools == nil {
		return ""
	}
	mode := skModeOf(a.cfg)
	if mode == "off" {
		return ""
	}
	if r := a.skReinject(); r != "" {
		return r
	}
	if mode != "auto" || a.mode() == "plan" || strings.HasPrefix(tc.Name, "mcp__") {
		return ""
	}
	name := CanonicalTool(tc.Name)
	sig := skSignals{Flags: map[string]bool{}}
	switch name {
	case "edit", "write":
		if out.IsErr {
			return ""
		}
		a.sk.edited, a.sk.ranAfter = true, false
		a.sk.diffLines += skArgLines(tc.Args)
		if p := argPath(tc.Args); p != "" {
			sig.Files = []string{p}
		}
		if a.sk.diffLines > 40 {
			sig.Flags["diff_large"] = true
		}
	case "bash":
		if a.sk.edited {
			a.sk.ranAfter = true
		}
		if _, perTurn, _ := skLimits(a.cfg); a.sk.turnCount >= perTurn {
			return "" // cheap exit before any regex: the turn already has its skill
		}
		if m, _, err := parseArgs(tc.Args); err == nil {
			sig.Cmd, _ = argStr(normalizeArgs("bash", m), "command")
		}
		sig.Flags = skOutputFlags(out.Text)
	default:
		return ""
	}
	if _, perTurn, _ := skLimits(a.cfg); a.sk.turnCount >= perTurn {
		return "" // the turn already has its skill
	}
	sig.Phase = a.skPhase()
	return a.skDecide("tool", sig)
}

// skReinject re-adds the current turn's skill when compaction dropped it from
// the history. The check is cheap: the message it went into, by index; only
// when that one moved (compaction re-indexes) is the history scanned.
func (a *Agent) skReinject() string {
	name := a.sk.turnSkill
	if name == "" {
		return ""
	}
	mk := skMarker(name)
	if i := a.sk.injAt; i >= 0 && i < len(a.Msgs) && strings.Contains(a.Msgs[i].Content, mk) {
		return ""
	}
	for i := len(a.Msgs) - 1; i >= 0; i-- {
		if strings.Contains(a.Msgs[i].Content, mk) {
			a.sk.injAt = i
			return ""
		}
	}
	s := skFind(name)
	if s == nil {
		return ""
	}
	dbg("skill", map[string]any{"at": "tool", "pick": name, "reinject": "compaction"})
	a.ui.Notice("skill aplicada de novo depois de compactar: " + name)
	a.sk.injAt = len(a.Msgs)
	return "\n\n" + skReminder(s, "re-added after the conversation was compacted")
}

// skArgLines counts the lines an edit/write call writes.
func skArgLines(raw string) int {
	m, _, err := parseArgs(raw)
	if err != nil {
		return 0
	}
	n := 0
	for _, k := range []string{"content", "new_string"} {
		if s, ok := m[k].(string); ok && s != "" {
			n += strings.Count(s, "\n") + 1
		}
	}
	if es, ok := m["edits"].([]any); ok {
		for _, e := range es {
			if em, ok := e.(map[string]any); ok {
				if s, ok := em["new_string"].(string); ok && s != "" {
					n += strings.Count(s, "\n") + 1
				}
			}
		}
	}
	return n
}

// SkillList describes the built-in skills for /skill (name: description).
func SkillList() []string {
	all, err := skBuiltins()
	if err != nil {
		return []string{"erro nas skills embutidas: " + err.Error()}
	}
	var out []string
	for _, s := range all {
		out = append(out, s.Name+": "+s.Description)
	}
	sort.Strings(out)
	return out
}

// SkillMode is the effective skills mode (auto | manual | off) for /skill.
func (a *Agent) SkillMode() string { return skModeOf(a.cfg) }
