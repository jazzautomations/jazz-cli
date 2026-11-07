package main

// methods.go: the method library (light HTN, docs/design/offline-brain.md §4)
// and its executor.
//
// A method is {name, intent, priority, pre, steps, verify, risk, cost}; steps
// call registered capabilities (caps.go), never free shell. Resolution is
// Pyhop-style: the methods of the intent, highest priority first; the first
// whose preconditions hold runs; a failed step goes through the recovery
// table (recover.go), which may move on to the next method; when none is
// left the task is blocked with an explanation. Success needs evidence
// (task.go): every method declares verifiers, and a method without one ends
// "unverified".
//
// Conditions are a closed mini-language: {"fact": "slot.target", "op":
// "set" | "unset" | "eq", "val": ...}. Facts: slot.<name>, hint,
// project.test|build|git, bin.<name>, env.online.

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed methods/*.json
var mhFS embed.FS

type mhCond struct {
	Fact string `json:"fact"`
	Op   string `json:"op"`
	Val  string `json:"val,omitempty"`
}

type mhStep struct {
	Cap string            `json:"cap"`
	In  map[string]string `json:"in"`
	Out string            `json:"out,omitempty"`
}

type mhCheck struct {
	Check string `json:"check"`
	Arg   string `json:"arg"`
	Arg2  string `json:"arg2,omitempty"` // ex.: dir alvo do list_present
}

// mhMethod is one way of doing an intent.
type mhMethod struct {
	Name     string    `json:"name"`
	Intent   string    `json:"intent"`
	Doc      string    `json:"doc,omitempty"`
	Priority int       `json:"priority"`
	Pre      []mhCond  `json:"pre"`
	Steps    []mhStep  `json:"steps"`
	Verify   []mhCheck `json:"verify"`
	Needs    []string  `json:"needs,omitempty"` // facts that must be set ("bin.ssh"): the tool the method uses is present
	Cmds     []string  `json:"cmds,omitempty"`  // literal commands the method runs as read-only: each must be in cbReadCmds
	// Slots declares the $slot.x placeholders a learned_cmd step may use:
	// name → entity kind ("dir", "file", "text"). Witnessed runs prove
	// them (distill.go); at execution every path-typed value is confined
	// to the project root before the command is re-checked.
	Slots  map[string]string `json:"slots,omitempty"`
	Risk   string            `json:"risk"`
	CostMS int               `json:"cost_ms"`
}

var (
	mhOnce sync.Once
	mhLib  map[string][]mhMethod
	mhErr  error
)

// mhLibrary loads the embedded methods, grouped by intent, best first.
func mhLibrary() (map[string][]mhMethod, error) {
	mhOnce.Do(func() {
		mhLib = map[string][]mhMethod{}
		ents, _ := mhFS.ReadDir("methods")
		for _, e := range ents {
			b, _ := mhFS.ReadFile("methods/" + e.Name())
			var f struct {
				Methods []mhMethod `json:"methods"`
			}
			if err := json.Unmarshal(b, &f); err != nil {
				mhErr = fmt.Errorf("methods/%s: %w", e.Name(), err)
				continue
			}
			for _, m := range f.Methods {
				if m.Name == "" || m.Intent == "" {
					continue
				}
				if bad := mhBadCmd(m); bad != "" {
					mhErr = fmt.Errorf("methods/%s: %s: comando fora da lista de leitura: %q", e.Name(), m.Name, bad)
					continue
				}
				mhLib[m.Intent] = append(mhLib[m.Intent], m)
			}
		}
		for k := range mhLib {
			l := mhLib[k]
			sort.SliceStable(l, func(i, j int) bool { return l[i].Priority > l[j].Priority })
		}
		if nuForgeOn() {
			dsLoadMethods(mhLib) // métodos aprendidos (distill.go), mesma cerca do overlay da gramática
		}
	})
	return mhLib, mhErr
}

// mhBadCmd returns the first declared command that is not an allowlisted
// read-only registry command ("" = all fine).
func mhBadCmd(m mhMethod) string {
	for _, c := range m.Cmds {
		if !cbReadCmd(c) {
			return c
		}
	}
	return ""
}

// mhHolds evaluates one precondition against the facts.
func mhHolds(c mhCond, facts map[string]string) bool {
	v, ok := facts[c.Fact]
	set := ok && v != ""
	switch c.Op {
	case "set", "":
		return set
	case "unset":
		return !set
	case "eq":
		return v == c.Val
	case "ne":
		return v != c.Val
	}
	return false
}

func mhApplicable(m mhMethod, facts map[string]string) bool {
	for _, n := range m.Needs {
		if !mhHolds(mhCond{Fact: n, Op: "set"}, facts) {
			return false
		}
	}
	for _, c := range m.Pre {
		if !mhHolds(c, facts) {
			return false
		}
	}
	return true
}

// ---------- executor ----------

// brEnv is what the brain may use. Every hook is optional except Root.
type brEnv struct {
	Root    string
	Shell   *Shell
	Permit  func(ctx context.Context, cmd string) (bool, string)
	Ask     func(q string, opts []string) int                         // chosen index, -1 = no answer
	AskText func(q string) (string, bool)                             // free answer
	Sink    func(tkEvent)                                             // task persistence
	Profile *epProfile                                                // nil = epGet(false)
	World   *nuWorld                                                  // nil = project + environment
	Run     func(ctx context.Context, cmd string) (string, int, bool) // nil = Shell (tests)
	Scripts *scRegistry                                               // nil = scLoad(Root)
	// shadow-git (checkpoint.go): BeginTurn opens the turn snapshot,
	// AfterWrite records the paths a command changed, Undo restores the
	// last writing turn. nil = the brain runs without /undo coverage.
	BeginTurn  func()
	AfterWrite func(cmd string)
	Undo       func() ([]string, error)
	// HostRun executes one argv OUTSIDE the command sandbox: process ops
	// (ps/pgrep/kill) only see the sandbox's PID namespace through Shell,
	// so host ops need a direct exec. The brain builds the argv itself
	// (validated values only) — there is no shell to inject into. The
	// permission check on the equivalent command string still applies.
	// nil = fall back to the sandboxed Shell.
	HostRun func(ctx context.Context, argv []string) (string, int)
}

// brCtx is one execution.
type brCtx struct {
	ctx       context.Context
	env       *brEnv
	task      *TaskState
	in        Intent
	vars      map[string]any
	facts     map[string]string
	out       strings.Builder
	questions int
	commands  int
	explore   *rvExplore
	stop      tkStatus // set by a step that ends the task
	stopWhy   string
	cat       rvCat
	attempts  map[rvCat]int
	proj      fpProject
	mslots    map[string]string // slot kinds of the method currently running
}

// brStep is what a capability returns.
type brStep struct {
	val  any
	obs  rvObs
	ok   bool
	why  string
	stop tkStatus // a step may end the task (report, question)
	skip bool     // o item não é aplicável (binário, grande demais) — for_each conta à parte
}

func (cx *brCtx) say(f string, a ...any) {
	fmt.Fprintf(&cx.out, f, a...)
	if !strings.HasSuffix(f, "\n") {
		cx.out.WriteByte('\n')
	}
}

// subst resolves "$var" and "$slot.x" in an input.
func (cx *brCtx) subst(v string) any {
	if !strings.HasPrefix(v, "$") {
		return v
	}
	k := v[1:]
	if strings.HasPrefix(k, "slot.") {
		if s, ok := cx.task.Slots[k[5:]]; ok {
			return s
		}
		return ""
	}
	if k == "hint" {
		return cx.in.Hint
	}
	if k == "goal" { // the request as typed: scripts that match it against real data (config keys)
		return cx.task.Goal
	}
	if strings.HasPrefix(k, "fact.") {
		return cx.facts[k[5:]]
	}
	if x, ok := cx.vars[k]; ok {
		return x
	}
	return ""
}

func (cx *brCtx) str(in map[string]string, k string) string {
	if v, ok := cx.subst(in[k]).(string); ok {
		return v
	}
	return ""
}

// substStr interpolates "$slot.x"/"$var" refs embedded inside a template
// string ("*.$slot.ext" → "*.log"). Only applied to authored method inputs
// that don't start with "$" — a $-prefixed value may be user text ($kf).
var reSlotRef = regexp.MustCompile(`\$[a-zA-Z_][a-zA-Z0-9_.]*`)

func (cx *brCtx) substStr(v string) string {
	return reSlotRef.ReplaceAllStringFunc(v, func(m string) string {
		if s, ok := cx.subst(m).(string); ok {
			return s
		}
		return m
	})
}

// runCmd runs a command through the permission check and the shell.
func (cx *brCtx) runCmd(cmd string, timeout time.Duration) (out string, exit int, obs rvObs) {
	return cx.runCmdAs(cmd, timeout, false)
}

// runCmdAs is runCmd; read=true marks an allowlisted registry command
// (caps.go cbReadCmds) as read-only for the permission check. Still sandboxed
// and audited like any other command.
func (cx *brCtx) runCmdAs(cmd string, timeout time.Duration, read bool) (out string, exit int, obs rvObs) {
	obs = rvNoCmd()
	t := cx.task
	if t.Budget.Cmds > 0 && t.Budget.UsedCmds >= t.Budget.Cmds {
		obs.BudgetLeft = 0
		return "", -1, obs
	}
	if cx.env.Permit != nil {
		pctx := cx.ctx
		if read {
			pctx = cbReadCtx(pctx, cmd)
		}
		if ok, why := cx.env.Permit(pctx, cmd); !ok {
			obs.Denied = true
			obs.Output = why
			t.AddEvidence("cmd", "negado: "+cmd+" ("+why+")")
			return "", -1, obs
		}
	}
	t.Budget.UsedCmds++
	cx.commands++
	timedOut := false
	if cx.env.Run != nil {
		out, exit, timedOut = cx.env.Run(cx.ctx, cmd)
	} else if cx.env.Shell != nil {
		r := cx.env.Shell.Run(cx.ctx, cmd, timeout, nil)
		out, exit = redactSecrets(r.Output), r.ExitCode
		timedOut = strings.Contains(strings.ToLower(r.Note), "timeout") || strings.Contains(strings.ToLower(r.Note), "tempo")
	} else {
		obs.Exit = 127
		obs.Output = "sem shell"
		return "", 127, obs
	}
	if cx.env.AfterWrite != nil {
		cx.env.AfterWrite(cmd) // shadow-git; cpAfterBash skips read-only commands
	}
	obs.Exit, obs.Output, obs.TimedOut = exit, truncateHeadTail(out, 4000), timedOut
	t.AddEvidence("cmd", fmt.Sprintf("`%s` → exit %d", cmd, exit))
	if cx.explore != nil && cx.explore.Observe(cmd, IsReadOnly(cmd), out) {
		cx.stop, cx.stopWhy = tkNeedsInput, rvExploreQuestion(cx.explore.Streak())
	}
	return out, exit, obs
}

// brCaps are the implementations of the brain capabilities. Filled in
// init() — for_each resolves caps through brCap at runtime, and a var
// literal would create an initialization cycle (brCaps→brForEach→brCap).
var brCaps map[string]func(cx *brCtx, in map[string]string) brStep

func init() {
	brCaps = map[string]func(cx *brCtx, in map[string]string) brStep{
		"resolve_name": brResolve,
		"choose":       brChoose,
		"ask_slot":     brAskSlot,
		"refill":       brRefill,
		"need":         brNeed,
		"take":         brTake,
		"for_each":     brForEach,
		"save_list":    brSaveList,
		"learned_cmd":  brLearnedCmd,
		"read_file":    brReadFile,
		"run_tests":    brRunTests,
		"test_context": brTestContext,
		"git_status":   brGitStatus,
		"env_status":   brEnvStatus,
		"self_info":    brSelfInfo,
		"tool_report":  brToolReport,
		"tool_status":  brToolStatus,
		"host_report":  brHostReport,
		"host_list":    brHostList,
		"systemd":      brSystemd,
		"net_status":   brNetStatus,
		"proj_cmd":     brProjCmd,
		"read_cmd":     brReadCmd,
		"say":          brSay,
		"report":       brReport,
		"file_op":      brFileOp,
		"text_op":      brTextOp,
		"archive_op":   brArchiveOp,
		"perm_op":      brPermOp,
		"git_op":       brGitOp,
		"proc_kill":    brProcKill,
		"pkg_cmd":      brPkgCmd,
		"container_op": brContainerOp,
		"undo":         brUndo,
	}
}

// brRefill re-reads the request under another intent's slot schema and
// merges the slots the primary parse could not see: "apaga o arquivo mais
// novo da pasta downloads" parsed as delete_file has no dir/sort, but read
// as file_find those words ARE dir=downloads, sort=mtime. need: comma
// groups; "a|b" is satisfied when at least one named slot exists — no
// match means the step fails, the chain dies and the ask variant takes
// over like it does today.
func brRefill(cx *brCtx, in map[string]string) brStep {
	intent := in["intent"]
	w := cx.env.World
	if w == nil {
		w = nuRuntimeWorld(cx.env.Root, cx.env.Profile)
	}
	slots := nuGet().FillAs(intent, cx.task.Goal, w)
	for k, v := range slots {
		if _, have := cx.task.Slots[k]; !have {
			cx.task.Slots[k] = v
			cx.facts["slot."+k] = v
		}
	}
	for _, grp := range strings.Split(in["need"], ",") {
		if grp == "" {
			continue
		}
		sat := false
		for _, k := range strings.Split(grp, "|") {
			if cx.task.Slots[strings.TrimSpace(k)] != "" {
				sat = true
			}
		}
		if !sat {
			return brStep{obs: rvNoCmd(), why: "o pedido não dá o que " + intent + " precisa (" + grp + ")"}
		}
	}
	return brStep{obs: rvNoCmd(), ok: true}
}

// brTake lifts one item out of a script result into a slot: the producer
// step leaves its payload in $from (file_find → data.files), take picks
// item `index`, reads `field` (default path) and writes the slot — the
// next chain step's $slot.X then resolves to the found value.
func brTake(cx *brCtx, in map[string]string) brStep {
	v := cx.subst(in["from"])
	m, _ := v.(map[string]any)
	data, _ := m["data"].(map[string]any)
	field := in["field"]
	if field == "" {
		field = "path"
	}
	idx := 0
	if s := in["index"]; s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			idx = n
		}
	}
	val := ""
	if arr, ok := data[field].([]any); ok && idx < len(arr) {
		switch it := arr[idx].(type) {
		case string:
			val = it
		case map[string]any:
			val, _ = it[field].(string)
		}
	}
	if val == "" {
		for _, key := range []string{"files", "items", "results", "matches", "entries"} {
			arr, ok := data[key].([]any)
			if !ok || idx >= len(arr) {
				continue
			}
			switch it := arr[idx].(type) {
			case map[string]any:
				val, _ = it[field].(string)
			case string:
				val = it
			}
			break
		}
	}
	if val == "" {
		return brStep{obs: rvNoCmd(), why: "nenhum item em " + in["from"]}
	}
	slot := in["slot"]
	cx.task.Slots[slot] = val
	cx.facts["slot."+slot] = val
	cx.task.AddEvidence("plan", fmt.Sprintf("%s = %s", slot, val))
	cx.say("→ %s", val)
	return brStep{val: val, obs: rvNoCmd(), ok: true}
}

// brNeed gates a method on slot groups: "a|b" is satisfied when at least
// one named slot exists. Unlike refill it reads no text — the primary
// parse's slots are trusted as-is (FillAs can poison generic name slots
// with cue words like "chamados").
func brNeed(cx *brCtx, in map[string]string) brStep {
	for _, grp := range strings.Split(in["slots"], ",") {
		if grp == "" {
			continue
		}
		sat := false
		for _, k := range strings.Split(grp, "|") {
			if cx.task.Slots[strings.TrimSpace(k)] != "" {
				sat = true
			}
		}
		if !sat {
			return brStep{obs: rvNoCmd(), why: "o pedido não traz o que preciso (" + grp + ")"}
		}
	}
	return brStep{obs: rvNoCmd(), ok: true}
}

// brForEach: the bounded fan-out for "apaga todos os .tmp" — runs the inner
// cap once per produced path. Every item passes through the inner cap's own
// gates (file_op → brSafePath + brPermit + confinamento), paths with ../
// absoluto ou ~ são ignorados, resultado truncado recusa (um conjunto
// parcial nunca vira alvo), e cada delete confere que o arquivo sumiu.
func brForEach(cx *brCtx, in map[string]string) brStep {
	v := cx.subst(in["from"])
	m, _ := v.(map[string]any)
	data, _ := m["data"].(map[string]any)
	if tr, _ := data["truncated"].(bool); tr {
		return brStep{obs: rvNoCmd(), why: "resultado truncado — recuso agir num conjunto parcial"}
	}
	var items []any
	for _, k := range []string{"files", "items", "results", "matches", "entries"} {
		if arr, ok := data[k].([]any); ok {
			items = arr
			break
		}
	}
	if len(items) == 0 {
		return brStep{obs: rvNoCmd(), why: "o produtor não encontrou nada pra agir"}
	}
	max := 32
	if s := in["max"]; s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			max = n
		}
	}
	inner := brCap(in["do"])
	if inner == nil {
		return brStep{obs: rvNoCmd(), why: "for_each: capacidade desconhecida " + in["do"]}
	}
	done, failed, skipped := 0, 0, 0
	sumKey := cx.str(in, "sum")
	sums := 0.0
	sample := map[string]any{}
	firstWhy := ""
	if len(items) > max {
		failed = len(items) - max // excedente conta como não-feito, honesto
		firstWhy = fmt.Sprintf("%d itens além do teto %d", len(items)-max, max)
		items = items[:max]
	}
	for _, it := range items {
		path := brItemPath(it)
		if path == "" || strings.Contains(path, "..") || strings.HasPrefix(path, "/") || strings.HasPrefix(path, "~") {
			failed++
			if firstWhy == "" {
				firstWhy = "item sem path seguro"
			}
			continue
		}
		ii := make(map[string]string, len(in))
		for k, v := range in {
			ii[k] = v
		}
		delete(ii, "from")
		delete(ii, "do")
		delete(ii, "slot")
		delete(ii, "max")
		delete(ii, "sum") // controle do for_each, não argumento do cap interno
		// "./$kf": arquivos cujo nome começa com $ não podem ir crus — o
		// subst do cap os leria como variável e esvaziaria o path.
		ii["path"] = "./" + path
		r := inner(cx, ii)
		if !r.ok {
			if r.skip {
				skipped++
				continue // não-aplicável não é falha — o lote segue honesto
			}
			failed++
			if firstWhy == "" {
				firstWhy = path + ": " + r.why
			}
			continue
		}
		// in["sum"]: acumula um campo numérico do data de cada item —
		// "wc -l sobre conjunto" vira sum=lines com total auditável.
		if sumKey != "" {
			if rm, ok := r.val.(map[string]any); ok {
				if rd, ok := rm["data"].(map[string]any); ok {
					if n, has := rd[sumKey]; has {
						f := brToFloat(n)
						sums += f
						if len(sample) < 8 {
							sample[path] = f
						}
					}
				}
			}
		}
		if in["do"] == "file_op" && in["op"] == "delete" {
			if abs, _, err := brSafePath(cx.env.Root, path); err == nil {
				if _, e := os.Stat(abs); e == nil {
					failed++
					if firstWhy == "" {
						firstWhy = path + " continua lá"
					}
					continue
				}
			}
		}
		done++
	}
	cx.task.AddEvidence("plan", fmt.Sprintf("for_each %s: %d feito(s), %d falha(s), %d pulado(s)", in["do"], done, failed, skipped))
	if done == 0 && skipped > 0 && failed == 0 {
		return brStep{obs: rvNoCmd(), why: fmt.Sprintf("nenhum item aplicável — %d pulado(s)", skipped)}
	}
	if failed > 0 {
		return brStep{obs: rvNoCmd(), why: fmt.Sprintf("%d feito(s), %d falha(s), %d pulado(s) — primeira: %s", done, failed, skipped, firstWhy)}
	}
	dout := map[string]any{
		"done": done, "failed": failed, "skipped": skipped,
		"total": len(items),
	}
	if sumKey != "" {
		dout["sum"], dout["sum_key"], dout["sample"] = sums, sumKey, sample
		cx.say("total de %s em %d itens: %.0f", sumKey, done, sums)
	}
	return brStep{obs: rvNoCmd(), ok: true, val: map[string]any{"data": dout}}
}

// brToFloat: o val pode chegar int (in-process), float64 (JSON) ou
// string serializada (round-trip de evidência) — todos são contagens
// válidas. Zero-padding serializado sai via TrimLeft.
func brToFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case string:
		f, _ := strconv.ParseFloat(strings.TrimLeft(strings.TrimSpace(n), "0"), 64)
		return f
	}
	return 0
}

// brSaveList escreve a lista produzida inteira num arquivo confinado —
// "find X e salva a lista em out.txt" sem redirect de shell: path preso
// no root, permissão de write, e lista vazia não cria arquivo nenhum.
func brSaveList(cx *brCtx, in map[string]string) brStep {
	obs := rvNoCmd()
	v := cx.subst(in["from"])
	m, _ := v.(map[string]any)
	data, _ := m["data"].(map[string]any)
	var items []any
	for _, k := range []string{"files", "items", "results", "matches", "entries"} {
		if arr, ok := data[k].([]any); ok {
			items = arr
			break
		}
	}
	if len(items) == 0 {
		return brStep{obs: obs, why: "o produtor não encontrou nada pra salvar"}
	}
	abs, rel, err := brSafePath(cx.env.Root, cx.str(in, "path"))
	if err != nil {
		return brStep{obs: obs, why: err.Error()}
	}
	if ok, why := cx.brPermit("write " + rel); !ok {
		obs.Denied, obs.Output = true, why
		return brStep{obs: obs, why: why}
	}
	var lines []string
	for _, it := range items {
		if s := brItemPath(it); s != "" {
			lines = append(lines, s)
		}
	}
	if len(lines) == 0 {
		return brStep{obs: obs, why: "a lista produzida não tem paths pra salvar"}
	}
	if in["sort"] == "true" {
		sort.Strings(lines)
	}
	if err = os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return brStep{obs: obs, why: "não criei o diretório: " + err.Error()}
	}
	if err = os.WriteFile(abs, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return brStep{obs: obs, why: "não escrevi: " + err.Error()}
	}
	cx.task.AddEvidence("plan", fmt.Sprintf("save_list %s: %d linha(s)", rel, len(lines)))
	obs.Output = fmt.Sprintf("%s: %d linha(s) salvas", rel, len(lines))
	return brStep{val: rel, obs: obs, ok: true}
}

// brCopyTree copia arquivo ou diretório (recursivo) — ambos os lados já
// passaram pelo brSafePath do chamador. Preserva permissão do fonte.
func brCopyTree(src, dst string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, data, fi.Mode().Perm())
	}
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		to := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(to, 0o755)
		}
		return brCopyTree(p, to)
	})
}

// brItemPath lifts a path out of a producer item — scripts emit either
// bare strings or objects with a "path" field.
func brItemPath(it any) string {
	switch x := it.(type) {
	case string:
		return x
	case map[string]any:
		p, _ := x["path"].(string)
		return p
	}
	return ""
}

// brCap resolves a capability name: the named Go caps above, or `script:<n>`
// routed to the Toolbox script runner — every script is a registered
// capability (caps.go cbRegistry), and this is the dispatch the registry
// expects (docs/design/offline-brain.md §5).
func brCap(name string) func(cx *brCtx, in map[string]string) brStep {
	if fn := brCaps[name]; fn != nil {
		return fn
	}
	if n, ok := strings.CutPrefix(name, "script:"); ok && scNameRe.MatchString(n) {
		return func(cx *brCtx, in map[string]string) brStep { return brScript(cx, n, in) }
	}
	return nil
}

// brReadCmd runs one exact allowlisted read-only registry command
// (caps.go cbReadCmds) and prints it. The method declares the argv; the
// exemption never covers model- or user-typed commands.
func brReadCmd(cx *brCtx, in map[string]string) brStep {
	cmd := in["cmd"]
	out, exit, obs := cx.runCmdAs(cmd, 20*time.Second, true)
	if obs.Exit < 0 || obs.Denied {
		return brStep{obs: obs, why: obs.Output}
	}
	shown := strings.TrimRight(truncateHeadTail(out, 4000), "\n")
	if shown == "" {
		shown = "(sem saída)"
	}
	cx.say("$ %s\n%s", cmd, shown)
	if exit != 0 {
		return brStep{val: brCmdRes{cmd, exit, out}, obs: obs, why: cmd + " falhou"}
	}
	return brStep{val: brCmdRes{cmd, exit, out}, obs: obs, ok: true}
}

// brUnitRe is what may reach a systemd unit name (no flags, no injection).
var brUnitRe = lazyRx(`^[a-zA-Z0-9_.@-]{1,64}$`)

// brSystemd answers service_status/service_logs: systemctl status or
// journalctl of ONE validated unit name, through the normal permission
// check (read-only commands pass in auto).
func brSystemd(cx *brCtx, in map[string]string) brStep {
	op, unit := in["op"], cx.str(in, "unit")
	if !brUnitRe.MatchString(unit) || strings.HasPrefix(unit, "-") {
		return brStep{obs: rvNoCmd(), why: "nome de serviço inválido: " + unit}
	}
	var cmd string
	switch op {
	case "logs":
		cmd = "journalctl -u " + unit + " -n 50 --no-pager --output=short-iso"
	case "start", "stop", "restart", "reload", "enable", "disable":
		cmd = "systemctl " + op + " " + unit
	default:
		cmd = "systemctl status " + unit + " --no-pager"
	}
	out, exit, obs := cx.runCmd(cmd, 30*time.Second)
	if obs.Denied || obs.Exit < 0 {
		return brStep{obs: obs, why: obs.Output}
	}
	shown := strings.TrimRight(truncateHeadTail(out, 4000), "\n")
	if shown == "" {
		shown = "(sem saída)"
	}
	cx.say("$ %s\n%s", cmd, shown)
	// "unit could not be found" (exit 4) means systemd doesn't manage it —
	// the service may still run as a plain process (sshd in a container).
	// Failing the step lets the probe method try runit/docker/pgrep.
	if exit == 4 || strings.Contains(out, "could not be found") {
		return brStep{val: brCmdRes{cmd, exit, out}, obs: obs,
			why: "systemd não conhece " + unit + " — pode estar rodando fora do systemd"}
	}
	// systemctl status exits 3 on inactive/failed units: that IS the answer
	return brStep{val: brCmdRes{cmd, exit, out}, obs: obs, ok: true}
}

// brHostList prints the known hosts (SSH config + Tailscale peers).
func brHostList(cx *brCtx, in map[string]string) brStep {
	p := cx.env.Profile
	if p == nil {
		p = epGet(false)
	}
	if len(p.SSHHosts) == 0 && len(p.Tailscale) == 0 {
		cx.say("nenhum host conhecido (sem ~/.ssh/config e nenhum peer Tailscale)")
		cx.task.AddEvidence("env", "0 hosts")
		return brStep{val: 0, obs: rvNoCmd(), ok: true}
	}
	for _, h := range p.SSHHosts {
		cx.say("SSH:      %s (%s@%s)", h.Name, orDash(h.User), orDash(h.HostName))
	}
	for _, t := range p.Tailscale {
		st := "offline"
		if t.Online {
			st = "online"
		}
		cx.say("Tailscale: %s (%s, %s)", t.Name, orDash(t.IP), st)
	}
	cx.task.AddEvidence("env", fmt.Sprintf("%d hosts ssh, %d peers tailscale", len(p.SSHHosts), len(p.Tailscale)))
	return brStep{val: len(p.SSHHosts) + len(p.Tailscale), obs: rvNoCmd(), ok: true}
}

// brNetStatus reports the connectivity the environment profile measured.
func brNetStatus(cx *brCtx, in map[string]string) brStep {
	p := cx.env.Profile
	if p == nil {
		p = epGet(false)
	}
	if p.Online {
		cx.say("online — o perfil de ambiente confirmou saída pra internet")
	} else {
		cx.say("offline — o perfil de ambiente não conseguiu sair pra internet")
	}
	cx.task.AddEvidence("env", fmt.Sprintf("online=%v", p.Online))
	return brStep{val: p, obs: rvNoCmd(), ok: true}
}

// brProjCmd runs the project's detected build/lint command (fpDetect), like
// run_tests does for tests: the command itself, then the outcome.
func brProjCmd(cx *brCtx, in map[string]string) brStep {
	which := in["which"]
	var cmd, label string
	switch which {
	case "lint":
		cmd, label = cx.proj.Lint, "lint"
	case "fmt":
		cmd, label = cx.proj.Fmt, "format"
	default:
		cmd, label = cx.proj.Build, "build"
	}
	obs := rvNoCmd()
	if cmd == "" {
		return brStep{obs: obs, why: "não detectei comando de " + label + " neste projeto"}
	}
	if f := strings.Fields(cmd); len(f) > 0 && cx.env.Run == nil && !strings.ContainsAny(f[0], "/=$") {
		if _, err := exec.LookPath(f[0]); err != nil {
			obs.Exit, obs.Output = 127, f[0]+": command not found"
			return brStep{obs: obs, why: f[0] + " não está no PATH"}
		}
	}
	cx.say("$ %s", cmd)
	out, exit, obs2 := cx.runCmd(cmd, 10*time.Minute)
	if obs2.Denied || obs2.BudgetLeft == 0 {
		return brStep{obs: obs2, why: obs2.Output}
	}
	shown := strings.TrimRight(truncateHeadTail(out, 4000), "\n")
	if shown != "" {
		cx.say("%s", shown)
	}
	if obs2.TimedOut {
		return brStep{val: brCmdRes{cmd, exit, out}, obs: obs2, why: label + " estourou o tempo"}
	}
	cx.task.AddEvidence("cmd", fmt.Sprintf("%s: exit %d", label, exit))
	return brStep{val: brCmdRes{cmd, exit, out}, obs: obs2, ok: true}
}

// brSafePath resolves a project-relative path and refuses escapes (absolute
// paths, ~, ..): the brain only writes inside Root.
func brSafePath(root, p string) (abs, rel string, err error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", "", errors.New("sem caminho")
	}
	if filepath.IsAbs(p) || strings.HasPrefix(p, "~") {
		return "", "", errors.New("caminho fora do projeto: " + p + " — rode jazz -C <pasta> para agir lá, ou peça um caminho dentro do projeto")
	}
	rel = filepath.Clean(filepath.FromSlash(p))
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", errors.New("caminho fora do projeto: " + p + " — rode jazz -C <pasta> para agir lá, ou peça um caminho dentro do projeto")
	}
	rootAbs, _ := filepath.Abs(root)
	abs, _ = filepath.Abs(filepath.Join(rootAbs, rel))
	if abs != rootAbs && !strings.HasPrefix(abs, rootAbs+string(filepath.Separator)) {
		return "", "", errors.New("caminho fora do projeto: " + p + " — rode jazz -C <pasta> para agir lá, ou peça um caminho dentro do projeto")
	}
	return abs, rel, nil
}

// brPermit asks the env's permission for a synthetic command line that
// describes the write ("write f.go", "rm f.go", "mv a b"): the same
// classifier that gates bash decides, and the audit shows a real command.
func (cx *brCtx) brPermit(cmd string) (bool, string) {
	if cx.env.Permit == nil {
		return true, ""
	}
	return cx.env.Permit(cx.ctx, cmd)
}

// brFileOp creates/writes/appends/renames/deletes a file inside the project.
// Writes go through the permission path and then AfterWrite, so the
// shadow-git checkpoint can undo them (checkpoint.go).
func brFileOp(cx *brCtx, in map[string]string) brStep {
	op, obs := in["op"], rvNoCmd()
	abs, rel, err := brSafePath(cx.env.Root, cx.str(in, "path"))
	if err != nil {
		return brStep{obs: obs, why: err.Error()}
	}
	toAbs, toRel, target := "", "", ""
	// to_dir: destino é <dir>/<basename(path)> — o fan-out de mv/cp
	// ("move todos os .png pra images/") repassa o dir por item.
	if d := cx.str(in, "to_dir"); d != "" {
		dAbs, dRel, e := brSafePath(cx.env.Root, d)
		if e != nil {
			return brStep{obs: obs, why: "destino: " + e.Error()}
		}
		toAbs, toRel = filepath.Join(dAbs, filepath.Base(abs)), filepath.ToSlash(filepath.Join(dRel, filepath.Base(rel)))
	} else if op == "rename" || op == "copy" {
		toAbs, toRel, err = brSafePath(cx.env.Root, cx.str(in, "to"))
		if err != nil {
			return brStep{obs: obs, why: "destino: " + err.Error()}
		}
	} else if op == "link" || op == "symlink" {
		target = cx.str(in, "to")
		if target == "" {
			return brStep{obs: obs, why: "link sem alvo"}
		}
	}
	var synth, done string
	switch op {
	case "create":
		synth = "write " + rel
		if _, e := os.Stat(abs); e == nil {
			return brStep{obs: obs, why: rel + " já existe (pede pra editar, ou apaga antes)"}
		}
		done = "criado"
	case "write":
		synth = "write " + rel
		done = "escrito"
	case "append":
		synth = "write " + rel + " (append)"
		done = "acrescentado"
	case "mkdir":
		synth = "mkdir -p " + rel
		done = "diretório criado"
	case "delete":
		synth = "rm " + rel
		done = "apagado"
	case "rename":
		synth = "mv " + rel + " " + toRel
		done = "renomeado"
	case "copy":
		synth = "cp -r " + rel + " " + toRel
		done = "copiado"
	case "link":
		synth = "ln " + target + " " + rel
		done = "link criado"
	case "symlink":
		synth = "ln -s " + target + " " + rel
		done = "link simbólico criado"
	default:
		return brStep{obs: obs, why: "file_op desconhecido: " + op}
	}
	if ok, why := cx.brPermit(synth); !ok {
		obs.Denied, obs.Output = true, why
		return brStep{obs: obs, why: why}
	}
	content := []byte(cx.str(in, "content"))
	switch op {
	case "create", "write":
		if err = os.MkdirAll(filepath.Dir(abs), 0o755); err == nil {
			err = os.WriteFile(abs, content, 0o644)
		}
	case "append":
		var f *os.File
		if f, err = os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644); err == nil {
			_, err = f.Write(content)
			f.Close()
		}
	case "mkdir":
		err = os.MkdirAll(abs, 0o755)
	case "delete":
		err = os.Remove(abs)
	case "rename":
		err = os.Rename(abs, toAbs)
	case "copy":
		err = brCopyTree(abs, toAbs)
	case "link":
		err = os.Link(target, abs)
	case "symlink":
		err = os.Symlink(target, abs)
	}
	if err != nil {
		return brStep{obs: obs, why: op + " " + rel + ": " + err.Error()}
	}
	if cx.env.AfterWrite != nil {
		cx.env.AfterWrite(synth)
	}
	shown := rel
	if op == "rename" || op == "copy" {
		shown = rel + " → " + toRel
	}
	if op == "link" || op == "symlink" {
		shown = rel + " → " + target
	}
	if op == "mkdir" || op == "delete" || op == "link" || op == "symlink" {
		cx.say("%s: %s", done, shown)
	} else if fi, e := os.Stat(abs); e == nil {
		cx.say("%s: %s (%d bytes)", done, shown, fi.Size())
	}
	if (op == "create" || op == "write") && len(content) == 0 {
		// "cria X com o texto Y": the NLU has no content slot — say the file
		// is empty instead of letting it look like Y was written
		if g := strings.ToLower(cx.task.Goal); strings.Contains(g, "texto") || strings.Contains(g, "conteúdo") || strings.Contains(g, "dizendo") {
			cx.say("(criei vazio — o conteúdo em si vai pelo modelo)")
		}
	}
	cx.task.AddEvidence("file", fmt.Sprintf("%s %s", op, shown))
	if cx.env.AfterWrite != nil {
		cx.say("(/undo desfaz)")
	}
	// copy/link: o val é o DESTINO — verificar file_exists no fim da cap
	// prova o artefato criado (pro rename o val da origem já prova via
	// file_gone, e pra copy a origem continua existindo mesmo).
	if op == "copy" {
		return brStep{val: toRel, obs: obs, ok: true}
	}
	return brStep{val: rel, obs: obs, ok: true}
}

// brTextOp: in-file text transforms confined like file_op — "replace" is
// the sed -i family: literal from_str→to_str per file, atomic write via
// tmp+rename inside the same dir so the checkpoint can undo.
func brTextOp(cx *brCtx, in map[string]string) brStep {
	op, obs := in["op"], rvNoCmd()
	if op == "sort" {
		return brTextSort(cx, in)
	}
	if op != "replace" {
		return brStep{obs: obs, why: "text_op desconhecido: " + op}
	}
	abs, rel, err := brSafePath(cx.env.Root, cx.str(in, "path"))
	if err != nil {
		return brStep{obs: obs, why: err.Error()}
	}
	from := cx.str(in, "from_str")
	if from == "" {
		return brStep{obs: obs, why: "replace sem texto de origem"}
	}
	to := cx.str(in, "to_str")
	toAbs, toRel := abs, rel
	if d := cx.str(in, "to"); d != "" {
		toAbs, toRel, err = brSafePath(cx.env.Root, d)
		if err != nil {
			return brStep{obs: obs, why: "destino: " + err.Error()}
		}
	}
	synth := "sed -i 's/" + from + "/" + to + "/g' " + rel
	if ok, why := cx.brPermit(synth); !ok {
		obs.Denied, obs.Output = true, why
		return brStep{obs: obs, why: why}
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return brStep{obs: obs, why: "replace " + rel + ": " + err.Error()}
	}
	if len(data) > 8<<20 {
		return brStep{obs: obs, why: "replace " + rel + ": arquivo grande demais (>8MB)", skip: true}
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return brStep{obs: obs, why: "replace " + rel + ": arquivo binário", skip: true}
	}
	hay, cnt := string(data), 0
	var out string
	switch {
	case cx.str(in, "ignore_case") == "true":
		// literal case-insensitive: quote the needle, fold via regexp
		re := regexp.MustCompile("(?i)" + regexp.QuoteMeta(from))
		out = re.ReplaceAllString(hay, to)
		cnt = len(re.FindAllStringIndex(hay, -1))
	case cx.str(in, "first_per_line") == "true":
		// sed s/a/b/ sem flag g: primeira ocorrência POR LINHA
		lines := strings.Split(hay, "\n")
		for i, l := range lines {
			if strings.Contains(l, from) {
				lines[i] = strings.Replace(l, from, to, 1)
				cnt++
			}
		}
		out = strings.Join(lines, "\n")
	default:
		cnt = strings.Count(hay, from)
		out = strings.ReplaceAll(hay, from, to)
	}
	if cnt == 0 {
		cx.say("%s: %q não aparece — nada a substituir", rel, from)
		return brStep{val: toRel, obs: obs, ok: true}
	}
	if b := cx.str(in, "backup_ext"); b != "" {
		if bAbs, _, e := brSafePath(cx.env.Root, rel+b); e == nil {
			os.WriteFile(bAbs, data, 0o644)
		}
	}
	tmp := toAbs + ".jazz.tmp"
	if err = os.WriteFile(tmp, []byte(out), 0o644); err == nil {
		err = os.Rename(tmp, toAbs)
	}
	if err != nil {
		return brStep{obs: obs, why: "replace " + rel + ": " + err.Error()}
	}
	if cx.env.AfterWrite != nil {
		cx.env.AfterWrite(synth)
		cx.say("(/undo desfaz)")
	}
	cx.say("substituído: %s → %s (%d ocorrência(s))", rel, toRel, cnt)
	cx.task.AddEvidence("file", fmt.Sprintf("replace %s", rel))
	return brStep{val: toRel, obs: obs, ok: true}
}

// brTextSort: ordena as linhas de um arquivo confinado — sort(1) sem
// shell. numeric/reverse/unique, key = coluna 1-based sobre o delimitador
// (default: campos em branco). Sem `to`/`inplace` imprime (read-only);
// com `to` grava no destino confinado, com inplace:true regrava a fonte —
// ambos passam por permissão, tmp+rename e checkpoint/undo.
func brTextSort(cx *brCtx, in map[string]string) brStep {
	obs := rvNoCmd()
	abs, rel, err := brSafePath(cx.env.Root, cx.str(in, "path"))
	if err != nil {
		return brStep{obs: obs, why: err.Error()}
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return brStep{obs: obs, why: "sort " + rel + ": " + err.Error()}
	}
	if len(data) > 8<<20 {
		return brStep{obs: obs, why: "sort " + rel + ": arquivo grande demais (>8MB)", skip: true}
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return brStep{obs: obs, why: "sort " + rel + ": arquivo binário", skip: true}
	}
	flags := "," + cx.str(in, "flags") + ","
	numeric := strings.Contains(flags, ",numeric,")
	reverse := strings.Contains(flags, ",reverse,")
	unique := strings.Contains(flags, ",unique,")
	key, _ := strconv.Atoi(cx.str(in, "key"))
	delim := cx.str(in, "delim")
	keyOf := func(l string) string {
		if key <= 0 {
			return l
		}
		var f []string
		if delim != "" {
			f = strings.Split(l, delim)
		} else {
			f = strings.Fields(l)
		}
		if key > len(f) {
			return ""
		}
		return f[key-1]
	}
	less := func(a, b string) bool {
		ka, kb := keyOf(a), keyOf(b)
		if numeric {
			fa, _ := strconv.ParseFloat(strings.TrimSpace(ka), 64)
			fb, _ := strconv.ParseFloat(strings.TrimSpace(kb), 64)
			if fa != fb {
				return fa < fb
			}
		}
		return ka < kb
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	sort.SliceStable(lines, func(i, j int) bool { return less(lines[i], lines[j]) })
	if reverse {
		for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
			lines[i], lines[j] = lines[j], lines[i]
		}
	}
	if unique {
		out := lines[:0]
		for i, l := range lines {
			if i == 0 || keyOf(l) != keyOf(lines[i-1]) {
				out = append(out, l)
			}
		}
		lines = out
	}
	to := cx.str(in, "to")
	if to == "" && cx.str(in, "inplace") == "true" {
		to = rel
	}
	out := strings.Join(lines, "\n") + "\n"
	if to == "" {
		shown := lines
		if len(shown) > 200 {
			shown = shown[:200]
			cx.say("… (%d linhas no total, primeiras 200)", len(lines))
		}
		for _, l := range shown {
			cx.say("%s", l)
		}
		// val carrega as linhas e a spec resolvida — out_sorted re-verifica
		// a ordenação com comparador próprio (falso sucesso não passa).
		return brStep{val: map[string]any{"data": map[string]any{
			"lines": lines, "flags": cx.str(in, "flags"),
			"key": key, "delim": delim, "reverse": reverse,
		}}, obs: obs, ok: true}
	}
	toAbs, toRel, err := brSafePath(cx.env.Root, to)
	if err != nil {
		return brStep{obs: obs, why: "destino: " + err.Error()}
	}
	synth := "sort " + rel + " > " + toRel
	if ok, why := cx.brPermit(synth); !ok {
		obs.Denied, obs.Output = true, why
		return brStep{obs: obs, why: why}
	}
	tmp := toAbs + ".jazz.tmp"
	if err = os.WriteFile(tmp, []byte(out), 0o644); err == nil {
		err = os.Rename(tmp, toAbs)
	}
	if err != nil {
		return brStep{obs: obs, why: "sort " + rel + ": " + err.Error()}
	}
	if cx.env.AfterWrite != nil {
		cx.env.AfterWrite(synth)
		cx.say("(/undo desfaz)")
	}
	cx.say("ordenado: %s → %s (%d linhas)", rel, toRel, len(lines))
	cx.task.AddEvidence("file", fmt.Sprintf("sort %s → %s", rel, toRel))
	return brStep{val: toRel, obs: obs, ok: true}
}

// brArchiveOp: cria/extrai tar/tar.gz/zip confinado ao root — Go-native,
// sem depender de tar/zip no PATH (potato-friendly). Anti tar-slip:
// membros ../, absolutos e links simbólicos não são escritos. Saída do
// extract é a lista de paths escritos (list_present prova o pós-estado).
// brArcList lista os nomes de entrada de um pacote tar/tar.gz/tgz/zip —
// usado pelo verificador arc_contains pra provar conteúdo, não existência.
func brArcList(abs string) (map[string]bool, error) {
	got := map[string]bool{}
	add := func(n string) {
		n = strings.TrimPrefix(strings.TrimSuffix(n, "/"), "./")
		if n != "" {
			got[n] = true
		}
	}
	if strings.HasSuffix(abs, ".zip") {
		zr, err := zip.OpenReader(abs)
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		for _, f := range zr.File {
			add(f.Name)
		}
		return got, nil
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var rd io.Reader = f
	if strings.HasSuffix(abs, ".tar.gz") || strings.HasSuffix(abs, ".tgz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		rd = gz
	}
	tr := tar.NewReader(rd)
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		add(h.Name)
	}
	return got, nil
}

func brArchiveOp(cx *brCtx, in map[string]string) brStep {
	op, obs := in["op"], rvNoCmd()
	switch op {
	case "create":
		out := cx.str(in, "out")
		if out == "" {
			return brStep{obs: obs, why: "archive sem destino"}
		}
		abs, rel, err := brSafePath(cx.env.Root, out)
		if err != nil {
			return brStep{obs: obs, why: err.Error()}
		}
		var srcList []string
		if raw := in["srcs"]; strings.HasPrefix(raw, "$") {
			// lista produzida (file_find): mesma dieta do for_each.from
			if mm, ok := cx.subst(raw).(map[string]any); ok {
				data, _ := mm["data"].(map[string]any)
				for _, k := range []string{"files", "items", "results", "matches", "entries"} {
					if arr, ok := data[k].([]any); ok {
						for _, it := range arr {
							if p := brItemPath(it); p != "" {
								srcList = append(srcList, p)
							}
						}
						break
					}
				}
			}
		}
		if srcList == nil {
			srcList = strings.FieldsFunc(cx.substStr(in["srcs"]), func(r rune) bool {
				return r == '\n' || r == ',' || r == ' '
			})
		}
		// "archive files/directories under data/": as palavras-tipo foram
		// filtradas do srcs — a fonte real é o dir escopado.
		if len(srcList) == 0 {
			if d := cx.str(in, "dir"); d != "" {
				srcList = []string{d}
			}
		}
		var srcs [][2]string
		for _, f := range srcList {
			if f == "" {
				continue
			}
			sAbs, sRel, e := brSafePath(cx.env.Root, f)
			if e != nil {
				return brStep{obs: obs, why: e.Error()}
			}
			srcs = append(srcs, [2]string{sAbs, sRel})
		}
		if len(srcs) == 0 {
			return brStep{obs: obs, why: "archive sem fontes"}
		}
		// "comprime a pasta logs em tar.gz": o destino era a palavra-formato,
		// não um nome — deriva <fonte>.<formato> como o tar faria.
		fmtExt := map[string]string{
			"tar": ".tar", "tar.gz": ".tar.gz", "tgz": ".tgz", "zip": ".zip",
			"tarball": ".tar.gz", "gz": ".tar.gz", "gzip": ".tar.gz",
			"jar": ".jar", "whl": ".whl",
		}
		if ext, ok := fmtExt[rel]; ok {
			base := filepath.Base(srcs[0][1])
			// conjunto produzido ("a tar of all .log files"): nomear pelo
			// primeiro item é enganoso — "a.log.tar" guarda 3 arquivos.
			if len(srcs) > 1 || base == "." || base == "/" {
				base = "archive"
			}
			rel = base + ext
			abs = filepath.Join(cx.env.Root, filepath.FromSlash(rel))
		}
		var files []string
		for _, s := range srcs {
			sAbs, sRel := s[0], s[1]
			fi, err := os.Stat(sAbs)
			if err != nil {
				return brStep{obs: obs, why: "fonte " + sRel + ": " + err.Error()}
			}
			if fi.IsDir() {
				err = filepath.Walk(sAbs, func(p string, i os.FileInfo, err error) error {
					if err != nil {
						return err
					}
					relP, err := filepath.Rel(cx.env.Root, p)
					if err != nil {
						return err
					}
					if i.IsDir() || i.Mode().IsRegular() {
						files = append(files, filepath.ToSlash(relP))
					}
					return nil
				})
				if err != nil {
					return brStep{obs: obs, why: "fonte " + sRel + ": " + err.Error()}
				}
			} else if fi.Mode().IsRegular() {
				files = append(files, sRel)
			}
		}
		if len(files) == 0 {
			return brStep{obs: obs, why: "nenhum arquivo regular nas fontes"}
		}
		synth := "tar -czf " + rel + " " + strings.Join(files, " ")
		if ok, why := cx.brPermit(synth); !ok {
			obs.Denied, obs.Output = true, why
			return brStep{obs: obs, why: why}
		}
		// formato pelo sufixo; "copy_of_folder1" sem ponto é tar puro —
		// o que o tar -cf produziria. Sufixo estranho (x.rar) recusa.
		isZip := strings.HasSuffix(rel, ".zip")
		isGzTar := strings.HasSuffix(rel, ".tar.gz") || strings.HasSuffix(rel, ".tgz")
		isTar := strings.HasSuffix(rel, ".tar") || !strings.Contains(filepath.Base(rel), ".")
		if !isZip && !isGzTar && !isTar {
			return brStep{obs: obs, why: "formato de archive não suportado: " + rel + " (tar/tar.gz/tgz/zip)"}
		}
		var buf bytes.Buffer
		switch {
		case isZip:
			zw := zip.NewWriter(&buf)
			for _, f := range files {
				data, err := os.ReadFile(filepath.Join(cx.env.Root, filepath.FromSlash(f)))
				if err != nil {
					continue
				}
				w, err := zw.Create(f)
				if err == nil {
					w.Write(data)
				}
			}
			zw.Close()
		default: // tar, tar.gz/tgz ou tar puro sem extensão
			var w io.Writer = &buf
			var gz *gzip.Writer
			if isGzTar {
				gz = gzip.NewWriter(&buf)
				w = gz
			}
			tw := tar.NewWriter(w)
			for _, f := range files {
				absF := filepath.Join(cx.env.Root, filepath.FromSlash(f))
				fi, err := os.Stat(absF)
				if err != nil || fi.IsDir() {
					if err == nil && fi.IsDir() {
						tw.WriteHeader(&tar.Header{Name: f + "/", Typeflag: tar.TypeDir, Mode: 0o755})
					}
					continue
				}
				data, err := os.ReadFile(absF)
				if err != nil {
					continue
				}
				if err := tw.WriteHeader(&tar.Header{
					Name: f, Typeflag: tar.TypeReg, Mode: int64(fi.Mode() & 0o777), Size: int64(len(data)),
				}); err == nil {
					tw.Write(data)
				}
			}
			tw.Close()
			if gz != nil {
				gz.Close()
			}
		}
		tmp := abs + ".jazz.tmp"
		if err = os.WriteFile(tmp, buf.Bytes(), 0o644); err == nil {
			err = os.Rename(tmp, abs)
		}
		if err != nil {
			return brStep{obs: obs, why: "archive " + rel + ": " + err.Error()}
		}
		if cx.env.AfterWrite != nil {
			cx.env.AfterWrite(synth)
			cx.say("(/undo desfaz)")
		}
		cx.say("archive criado: %s (%d arquivo(s))", rel, len(files))
		cx.task.AddEvidence("file", fmt.Sprintf("archive create %s", rel))
		packed := make([]any, 0, len(files))
		for _, f := range files {
			packed = append(packed, f)
		}
		return brStep{val: map[string]any{
			"path": rel, "packed": len(files), "files": packed,
		}, obs: obs, ok: true}
	case "extract":
		arc := cx.str(in, "arc")
		if arc == "" {
			return brStep{obs: obs, why: "extract sem arquivo"}
		}
		abs, rel, err := brSafePath(cx.env.Root, arc)
		if err != nil {
			return brStep{obs: obs, why: err.Error()}
		}
		dir := cx.str(in, "dir")
		if dir == "" {
			dir = "."
		}
		dirAbs, dirRel, err := brSafePath(cx.env.Root, dir)
		if err != nil {
			return brStep{obs: obs, why: "destino: " + err.Error()}
		}
		synth := "tar -xf " + rel + " -C " + dirRel
		if ok, why := cx.brPermit(synth); !ok {
			obs.Denied, obs.Output = true, why
			return brStep{obs: obs, why: why}
		}
		var written []any
		skipped := 0
		addWritten := func(name string) {
			name = strings.TrimSuffix(name, "/")
			if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
				skipped++ // tar-slip: nem escreve nem prova
				return
			}
			tgt := filepath.Join(dirAbs, filepath.FromSlash(name))
			root, _ := filepath.Abs(cx.env.Root)
			full, _ := filepath.Abs(tgt)
			if !strings.HasPrefix(full, root+string(os.PathSeparator)) && full != root {
				skipped++
				return
			}
			if err := os.MkdirAll(filepath.Dir(tgt), 0o755); err != nil {
				skipped++
				return
			}
			written = append(written, map[string]any{"path": filepath.Join(dirRel, filepath.FromSlash(name)), "abs": tgt})
		}
		// formato pelos magic bytes, não pela extensão — "backup" sem
		// extensão extrai se for tar/zip de verdade, e um ".zip" que não
		// é PK falha com a razão certa.
		head := make([]byte, 300)
		var n int
		if fh, err := os.Open(abs); err == nil {
			n, _ = fh.Read(head)
			fh.Close()
		}
		head = head[:n]
		magicZip := n >= 4 && bytes.Equal(head[:4], []byte("PK\x03\x04"))
		magicGz := n >= 2 && head[0] == 0x1f && head[1] == 0x8b
		magicTar := n >= 262 && string(head[257:262]) == "ustar"
		switch {
		case magicZip:
			zr, err := zip.OpenReader(abs)
			if err != nil {
				return brStep{obs: obs, why: "extract " + rel + ": " + err.Error()}
			}
			defer zr.Close()
			for _, f := range zr.File {
				if f.FileInfo().IsDir() {
					continue
				}
				before := len(written)
				addWritten(f.Name)
				if len(written) == before {
					continue
				}
				rc, err := f.Open()
				if err != nil {
					continue
				}
				data, err := io.ReadAll(io.LimitReader(rc, 64<<20))
				rc.Close()
				if err != nil {
					continue
				}
				tgt := written[len(written)-1].(map[string]any)["abs"].(string)
				os.WriteFile(tgt, data, f.Mode()&0o777)
			}
		case magicGz || magicTar || !strings.Contains(filepath.Base(rel), ".") ||
			strings.HasSuffix(rel, ".tar") || strings.HasSuffix(rel, ".tgz"):
			fh, err := os.Open(abs)
			if err != nil {
				return brStep{obs: obs, why: "extract " + rel + ": " + err.Error()}
			}
			defer fh.Close()
			var rd io.Reader = fh
			if magicGz {
				gz, err := gzip.NewReader(fh)
				if err != nil {
					return brStep{obs: obs, why: "extract " + rel + ": " + err.Error()}
				}
				defer gz.Close()
				rd = gz
			}
			tr := tar.NewReader(rd)
			for {
				h, err := tr.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					return brStep{obs: obs, why: "extract " + rel + ": " + err.Error()}
				}
				if h.Typeflag != tar.TypeReg {
					if h.Typeflag != tar.TypeDir {
						skipped++ // link/device/fifo: não escreve
					}
					continue
				}
				before := len(written)
				addWritten(h.Name)
				if len(written) == before {
					continue
				}
				data, err := io.ReadAll(io.LimitReader(tr, 64<<20))
				if err != nil {
					continue
				}
				tgt := written[len(written)-1].(map[string]any)["abs"].(string)
				os.WriteFile(tgt, data, os.FileMode(h.Mode&0o777))
			}
		default:
			return brStep{obs: obs, why: "formato de archive não suportado: " + rel + " (tar/tar.gz/tgz/zip)"}
		}
		if len(written) == 0 {
			return brStep{obs: obs, why: "nada extraído de " + rel}
		}
		if cx.env.AfterWrite != nil {
			cx.env.AfterWrite(synth)
			cx.say("(/undo desfaz)")
		}
		var files []any
		for _, w := range written {
			m := w.(map[string]any)
			files = append(files, map[string]any{"path": m["path"]})
		}
		cx.say("extraído: %s → %s (%d arquivo(s))", rel, dirRel, len(files))
		if skipped > 0 {
			cx.say("membros não-extraídos (link/dir/unsafe): %d", skipped)
			cx.task.AddEvidence("file", fmt.Sprintf("archive extract %s: %d membros pulados", rel, skipped))
		}
		cx.task.AddEvidence("file", fmt.Sprintf("archive extract %s", rel))
		return brStep{val: map[string]any{"data": map[string]any{"files": files}}, obs: obs, ok: true}
	}
	return brStep{obs: obs, why: "archive_op desconhecido: " + op}
}

// brPermOp: chmod/chown/chgrp confinado ao root — o par file_op das
// permissões. mode aceita octal ("755","0644") ou simbólico simples
// ("u+x","ug=rwx,o=","a-w"); recursive desce um dir inteiro com teto.
// Permit decide; EPERM do chown vira falha honesta, não sucesso.
func brPermOp(cx *brCtx, in map[string]string) brStep {
	op, obs := in["op"], rvNoCmd()
	switch op {
	case "chmod", "chown", "chgrp":
	default:
		return brStep{obs: obs, why: "perm_op desconhecido: " + op}
	}
	path := cx.str(in, "path")
	if path == "" {
		return brStep{obs: obs, why: "perm_op sem alvo"}
	}
	abs, rel, err := brSafePath(cx.env.Root, path)
	if err != nil {
		return brStep{obs: obs, why: err.Error()}
	}
	modeStr := cx.str(in, "mode")
	ownerStr := cx.str(in, "owner")
	groupStr := cx.str(in, "group")
	recursive := cx.str(in, "recursive") == "true"
	if op == "chmod" && modeStr == "" {
		return brStep{obs: obs, why: "chmod sem modo"}
	}
	if op == "chown" && ownerStr == "" && groupStr == "" {
		return brStep{obs: obs, why: "chown sem dono/grupo"}
	}
	if op == "chgrp" && groupStr == "" {
		return brStep{obs: obs, why: "chgrp sem grupo"}
	}
	synth := op
	if recursive {
		synth += " -R"
	}
	if modeStr != "" {
		synth += " " + modeStr
	}
	if ownerStr != "" {
		synth += " " + ownerStr
	}
	if groupStr != "" {
		synth += ":" + groupStr
	}
	synth += " " + rel
	if ok, why := cx.brPermit(synth); !ok {
		obs.Denied, obs.Output = true, why
		return brStep{obs: obs, why: why}
	}
	var targets []string
	fi, err := os.Stat(abs)
	if err != nil {
		return brStep{obs: obs, why: rel + ": " + err.Error()}
	}
	if recursive && fi.IsDir() {
		err = filepath.Walk(abs, func(p string, i os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if len(targets) >= 512 {
				return fmt.Errorf("recursive: mais de 512 entradas — recuso conjunto parcial")
			}
			targets = append(targets, p)
			return nil
		})
		if err != nil {
			return brStep{obs: obs, why: err.Error()}
		}
	} else {
		targets = []string{abs}
	}
	done := 0
	for _, tgt := range targets {
		tfi, err := os.Stat(tgt)
		if err != nil {
			continue
		}
		switch op {
		case "chmod":
			var nm os.FileMode
			if m, ok := brParseOctal(modeStr); ok {
				nm = m
			} else {
				m, e := brApplySymbolic(tfi.Mode().Perm(), modeStr)
				if e != nil {
					return brStep{obs: obs, why: "modo inválido " + modeStr + ": " + e.Error()}
				}
				nm = m
			}
			if err := os.Chmod(tgt, nm); err != nil {
				return brStep{obs: obs, why: "chmod " + rel + ": " + err.Error()}
			}
		case "chown", "chgrp":
			uid, gid := -1, -1
			if op == "chown" && ownerStr != "" {
				uid, err = brResolveUser(ownerStr)
				if err != nil {
					return brStep{obs: obs, why: "dono " + ownerStr + ": " + err.Error()}
				}
			}
			if groupStr != "" {
				gid, err = brResolveGroup(groupStr)
				if err != nil {
					return brStep{obs: obs, why: "grupo " + groupStr + ": " + err.Error()}
				}
			}
			if err := os.Chown(tgt, uid, gid); err != nil {
				return brStep{obs: obs, why: op + " " + rel + ": " + err.Error()}
			}
		}
		done++
	}
	if done == 0 {
		return brStep{obs: obs, why: "nada alterado em " + rel}
	}
	if cx.env.AfterWrite != nil {
		cx.env.AfterWrite(synth)
		cx.say("(/undo desfaz)")
	}
	cx.say("%s aplicado em %s (%d)", synth, rel, done)
	cx.task.AddEvidence("file", fmt.Sprintf("%s em %s (%d alvos)", synth, rel, done))
	return brStep{val: rel, obs: obs, ok: true}
}

// brParseOctal: "755"/"0755"/"4755" → FileMode, ou ok=false quando não é
// octal de 3-4 dígitos (aí quem chama tenta o simbólico).
func brParseOctal(s string) (os.FileMode, bool) {
	if len(s) < 3 || len(s) > 4 {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 8, 32)
	if err != nil || n > 0o7777 {
		return 0, false
	}
	return os.FileMode(n), true
}

// brApplySymbolic: subconjunto honesto do chmod simbólico —
// "u+x", "a-w", "ug=rwx,o=", "go+r". X vira x só em dir/já-executável;
// s/t são ignorados com aviso, não inventados.
func brApplySymbolic(cur os.FileMode, expr string) (os.FileMode, error) {
	out := cur
	for _, clause := range strings.Split(expr, ",") {
		clause = strings.TrimSpace(clause)
		op := strings.IndexAny(clause, "+-=")
		if op < 0 || (op == 0 && clause[0] == '=') {
			return cur, fmt.Errorf("cláusula sem operador: %q", clause)
		}
		who := clause[:op]
		if who == "" {
			who = "a" // chmod "+x"/"-w": sem who explícito ⇒ todos
		}
		perm := clause[op+1:]
		var mask os.FileMode
		for _, c := range who {
			switch c {
			case 'u':
				mask |= 0o700
			case 'g':
				mask |= 0o070
			case 'o':
				mask |= 0o007
			case 'a':
				mask |= 0o777
			default:
				return cur, fmt.Errorf("who inválido %q em %q", string(c), clause)
			}
		}
		var bits os.FileMode
		for _, p := range perm {
			switch p {
			case 'r':
				bits |= mask & 0o444
			case 'w':
				bits |= mask & 0o222
			case 'x', 'X':
				bits |= mask & 0o111
			case 's', 't':
				// suid/sgid/sticky: recusa silenciosa seria pior — ignora
			default:
				return cur, fmt.Errorf("permissão inválida %q em %q", string(p), clause)
			}
		}
		switch clause[op] {
		case '+':
			out |= bits
		case '-':
			out &^= bits
		case '=':
			out = (out &^ mask) | bits
		}
	}
	return out, nil
}

func brResolveUser(name string) (int, error) {
	if u, err := strconv.Atoi(name); err == nil {
		return u, nil
	}
	u, err := user.Lookup(name)
	if err != nil {
		return -1, err
	}
	return strconv.Atoi(u.Uid)
}

func brResolveGroup(name string) (int, error) {
	if g, err := strconv.Atoi(name); err == nil {
		return g, nil
	}
	g, err := user.LookupGroup(name)
	if err != nil {
		return -1, err
	}
	return strconv.Atoi(g.Gid)
}

// brGitOp runs git write subcommands through the normal permission path:
// interactive asks, untrusted -p denies (honest), trusted runs.
func brGitOp(cx *brCtx, in map[string]string) brStep {
	sub, obs := in["sub"], rvNoCmd()
	var cmds []string
	switch sub {
	case "commit":
		msg := strings.TrimSpace(cx.str(in, "msg"))
		if msg == "" || len(msg) > 300 {
			return brStep{obs: obs, why: "mensagem de commit vazia ou longa demais"}
		}
		cmds = []string{"git add -A", "git commit -m " + brShQ(msg)}
	case "checkout":
		b := strings.TrimSpace(cx.str(in, "branch"))
		if !brGitRefRe.MatchString(b) || strings.Contains(b, "..") || strings.HasPrefix(b, "-") {
			return brStep{obs: obs, why: "branch inválida: " + b}
		}
		cmds = []string{"git checkout " + b}
	case "pull":
		cmds = []string{"git pull --ff-only"}
	case "push":
		cmds = []string{"git push"}
	case "stash_push":
		cmds = []string{"git stash push -u"}
	default:
		return brStep{obs: obs, why: "git_op desconhecido: " + sub}
	}
	var last brCmdRes
	for _, cmd := range cmds {
		out, exit, o := cx.runCmd(cmd, 2*time.Minute)
		last = brCmdRes{cmd, exit, out}
		if o.Denied || o.Exit < 0 {
			return brStep{val: last, obs: o, why: o.Output}
		}
		shown := strings.TrimRight(truncateHeadTail(out, 2000), "\n")
		if shown == "" {
			shown = "(sem saída)"
		}
		cx.say("$ %s\n%s", cmd, shown)
		if exit != 0 {
			return brStep{val: last, obs: o, why: cmd + " falhou"}
		}
	}
	cx.task.AddEvidence("cmd", "git "+sub+": ok")
	return brStep{val: last, obs: rvNoCmd(), ok: true}
}

// brGitRefRe is what may reach a git ref/branch name (no flags, no ..).
var brGitRefRe = lazyRx(`^[a-zA-Z0-9][a-zA-Z0-9._/-]{0,79}$`)

// brShQ single-quotes s for the shell (the only interpolation the brain does).
func brShQ(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// brProcKill signals a process (TERM by default): a numeric pid directly, or
// a process name resolved through pgrep (one match only — ambiguous stays
// honest). kill goes through the permission path, so -p denies and
// interactive asks.
// brHostExec is env.HostRun for production: a direct exec, no shell. The
// argv comes from capability code after validation, never from raw input.
func brHostExec(ctx context.Context, argv []string) (string, int) {
	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	b, err := c.CombinedOutput()
	if err == nil {
		return string(b), 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return string(b), ee.ExitCode()
	}
	return err.Error(), 127
}

// brHostCmd runs a validated argv on the host (env.HostRun) or through the
// sandboxed shell as a fallback. desc is the command string the permission
// check and the audit see.
func brHostCmd(cx *brCtx, desc string, argv []string, timeout time.Duration) (out string, exit int, obs rvObs) {
	if cx.env.HostRun == nil {
		return cx.runCmd(desc, timeout) // sandboxed fallback: permit inside
	}
	if cx.env.Permit != nil {
		if ok, why := cx.env.Permit(cx.ctx, desc); !ok {
			obs = rvNoCmd()
			obs.Denied, obs.Output = true, why
			cx.task.AddEvidence("cmd", "negado: "+desc+" ("+why+")")
			return "", -1, obs
		}
	}
	cx.task.Budget.UsedCmds++
	cx.commands++
	out, exit = cx.env.HostRun(cx.ctx, argv)
	obs = rvNoCmd()
	obs.Exit, obs.Output = exit, truncateHeadTail(out, 4000)
	cx.task.AddEvidence("cmd", fmt.Sprintf("`%s` → exit %d", desc, exit))
	return out, exit, obs
}

func brProcKill(cx *brCtx, in map[string]string) brStep {
	pid, obs := cx.str(in, "pid"), rvNoCmd()
	if !brPidRe.MatchString(pid) {
		name := strings.TrimSpace(pid)
		if !brProcNameRe.MatchString(name) || strings.HasPrefix(name, "-") {
			return brStep{obs: obs, why: "pid inválido (quero o número, ex.: 1234): " + pid}
		}
		out, exit, o := brHostCmd(cx, "pgrep -x "+name, []string{"pgrep", "-x", name}, 10*time.Second)
		if o.Denied || o.Exit < 0 {
			return brStep{obs: o, why: o.Output}
		}
		pids := strings.Fields(strings.TrimSpace(out))
		switch {
		case exit != 0 || len(pids) == 0:
			return brStep{obs: o, why: "nenhum processo '" + name + "' rodando"}
		case len(pids) > 1:
			cx.say("'%s' tem %d processos: %s — mata pelo pid", name, len(pids), strings.Join(pids, ", "))
			return brStep{obs: o, why: "vários pids pra " + name}
		}
		pid = pids[0]
	}
	sig := strings.ToUpper(strings.TrimPrefix(cx.str(in, "sig"), "-"))
	if sig == "" {
		sig = "TERM"
	}
	if _, ok := map[string]bool{"TERM": true, "KILL": true, "HUP": true, "INT": true}[sig]; !ok {
		return brStep{obs: obs, why: "sinal não permitido: " + sig}
	}
	cmd := "kill -" + sig + " " + pid
	out, exit, o := brHostCmd(cx, cmd, []string{"kill", "-" + sig, pid}, 10*time.Second)
	if o.Denied || o.Exit < 0 {
		return brStep{obs: o, why: o.Output}
	}
	cx.say("$ %s", cmd)
	if shown := strings.TrimSpace(out); shown != "" {
		cx.say("%s", shown)
	}
	return brStep{val: brCmdRes{cmd, exit, out}, obs: o, ok: true}
}

var brPidRe = lazyRx(`^[0-9]{1,7}$`)

// brProcNameRe: what may reach pgrep -x (a process name, no flags/injection).
var brProcNameRe = lazyRx(`^[a-zA-Z0-9_.-]{1,64}$`)

// brPkgCmd installs/updates a dependency with the manager the project files
// declare (package.json, pyproject/requirements, go.mod, Cargo.toml): the
// command goes through the permission path like any exec.
func brPkgCmd(cx *brCtx, in map[string]string) brStep {
	dep, obs := strings.TrimSpace(cx.str(in, "dep")), rvNoCmd()
	if !brDepRe.MatchString(dep) {
		return brStep{obs: obs, why: "dependência inválida: " + dep}
	}
	has := func(f string) bool { _, e := os.Stat(filepath.Join(cx.env.Root, f)); return e == nil }
	update := in["op"] == "update"
	var cmd string
	switch {
	case has("package.json"):
		if update {
			cmd = "npm update " + dep
		} else {
			cmd = "npm install " + dep
		}
	case has("requirements.txt"), has("pyproject.toml"):
		if update {
			dep += " --upgrade"
		}
		cmd = "pip install " + dep
	case has("go.mod"):
		if update {
			dep += "@latest"
		}
		cmd = "go get " + dep
	case has("Cargo.toml"):
		if update {
			cmd = "cargo update -p " + dep
		} else {
			cmd = "cargo add " + dep
		}
	default:
		return brStep{obs: obs, why: "não achei gerenciador de pacotes (procurei package.json, requirements.txt/pyproject.toml, go.mod, Cargo.toml)"}
	}
	out, exit, o := cx.runCmd(cmd, 5*time.Minute)
	if o.Denied || o.Exit < 0 {
		return brStep{obs: o, why: o.Output}
	}
	shown := strings.TrimRight(truncateHeadTail(out, 3000), "\n")
	if shown == "" {
		shown = "(sem saída)"
	}
	cx.say("$ %s\n%s", cmd, shown)
	if o.TimedOut {
		return brStep{val: brCmdRes{cmd, exit, out}, obs: o, why: "instalação estourou o tempo"}
	}
	return brStep{val: brCmdRes{cmd, exit, out}, obs: o, ok: true}
}

var brDepRe = lazyRx(`^[a-zA-Z0-9][a-zA-Z0-9._@/+~-]{0,80}$`)

// brContainerOp starts/stops/restarts one validated container on docker or
// podman (whichever the environment has) through the permission path.
func brContainerOp(cx *brCtx, in map[string]string) brStep {
	op := in["op"]
	name, obs := strings.TrimSpace(cx.str(in, "name")), rvNoCmd()
	var sub string
	switch op {
	case "start", "stop", "restart":
		if !brUnitRe.MatchString(name) || strings.HasPrefix(name, "-") {
			return brStep{obs: obs, why: "nome de container inválido: " + name}
		}
		sub = op + " " + name
	case "image_prune":
		sub = "image prune -af"
	case "system_prune":
		sub = "system prune -af"
	default:
		return brStep{obs: obs, why: "container_op desconhecido: " + op}
	}
	rt := ""
	if f := cx.task.Facts; f["bin.docker"] != "" {
		rt = "docker"
	} else if f["bin.podman"] != "" {
		rt = "podman"
	}
	if rt == "" {
		return brStep{obs: obs, why: "nem docker nem podman no PATH"}
	}
	cmd := rt + " " + sub
	out, exit, o := cx.runCmd(cmd, 60*time.Second)
	if o.Denied || o.Exit < 0 {
		return brStep{obs: o, why: o.Output}
	}
	shown := strings.TrimSpace(out)
	cx.say("$ %s\n%s", cmd, orDash(shown))
	return brStep{val: brCmdRes{cmd, exit, out}, obs: o, ok: true}
}

// brUndo restores the files of the last writing turn (checkpoint.go): the
// "desfaz" path of the offline brain.
func brUndo(cx *brCtx, in map[string]string) brStep {
	obs := rvNoCmd()
	if cx.env.Undo == nil {
		return brStep{obs: obs, why: "sem checkpoints nesta sessão — não sei o que desfazer"}
	}
	paths, err := cx.env.Undo()
	if err != nil {
		return brStep{obs: obs, why: "undo falhou: " + err.Error()}
	}
	if len(paths) == 0 {
		cx.say("nada pra desfazer — nenhuma turn anterior escreveu arquivos")
		cx.task.AddEvidence("undo", "0 paths")
		return brStep{val: "nada", obs: obs, ok: true}
	}
	cx.say("desfeito: %s", strings.Join(paths, ", "))
	cx.task.AddEvidence("undo", fmt.Sprintf("%d path(s)", len(paths)))
	return brStep{val: strings.Join(paths, ", "), obs: obs, ok: true}
}

// brSay prints a fixed text and returns it: the way for a method to show
// something verified ("o texto apareceu") without ending the task like
// report does.
func brSay(cx *brCtx, in map[string]string) brStep {
	text := cx.str(in, "text")
	if text == "" {
		return brStep{obs: rvNoCmd(), why: "texto vazio"}
	}
	cx.say("%s", text)
	cx.task.AddEvidence("say", fmt.Sprintf("mostrei %d bytes", len(text)))
	return brStep{val: text, obs: rvNoCmd(), ok: true}
}

// brScript runs a script:<name> capability through the Toolbox script
// runner — the same sandboxed, env-scrubbed, timed execution the model
// gets. `in` keys are the script's own arg names: values go through
// $-substitution, then the script's schema validates/coerces. Read effects
// run free like any dry-run capability; other effects still go through the
// permission hook because a slot may carry user text (a cmd arg).
func brScript(cx *brCtx, name string, in map[string]string) brStep {
	obs := rvNoCmd()
	t := cx.task
	if t.Budget.Cmds > 0 && t.Budget.UsedCmds >= t.Budget.Cmds {
		obs.BudgetLeft = 0
		return brStep{obs: obs, why: "orçamento de comandos esgotado"}
	}
	r := cx.env.Scripts
	if r == nil {
		r = scLoad(cx.env.Root)
	}
	d := r.get(name)
	if d == nil {
		return brStep{obs: obs, why: "script desconhecido: " + name}
	}
	interp := r.sh
	if d.Run == "python3" {
		interp = r.python
	}
	if interp == "" {
		obs.Exit = 127
		return brStep{obs: obs, why: "sem " + d.Run + " no PATH"}
	}
	args := map[string]any{}
	var kv []string
	for k, v := range in {
		x := cx.subst(v)
		if s, ok := x.(string); ok {
			if s == "" {
				continue // empty slot: let the schema apply the default or the required error
			}
			if !strings.HasPrefix(v, "$") {
				x = cx.substStr(s) // "*.$slot.ext" → "*.log"
			}
		}
		args[k] = x
		kv = append(kv, k+"="+oneLine(fmt.Sprint(x), 60))
	}
	sort.Strings(kv)
	args, errs := scValidate(d.Args, args)
	if len(errs) > 0 {
		return brStep{obs: obs, why: "args inválidos p/ script:" + name + ": " + strings.Join(errs, "; ")}
	}
	effect := d.Effect
	if op, _ := argStr(args, "op"); op != "" {
		if e, ok := d.EffectByOp[op]; ok {
			effect = e
		}
	}
	desc := "script:" + name
	if len(kv) > 0 {
		desc += " " + strings.Join(kv, " ")
	}
	// the model path's policy (scripts.go scPolicy.readOnly): a read script
	// that runs a non-read-only cmd, or a net script going remote, is NOT
	// read-only and needs the same permission check as the tool call
	cmd, _ := argStr(args, "cmd")
	cmd = strings.TrimSpace(cmd)
	remote := d.Name == "http_check" && argBool(args, "allow_remote")
	readonly := (cmd == "" || IsReadOnly(cmd)) && (effect == "read" || effect == "net" && !remote)
	if !readonly && cx.env.Permit != nil {
		if cmd == "" {
			cmd = desc
		}
		if ok, why := cx.env.Permit(cx.ctx, cmd); !ok {
			obs.Denied, obs.Output = true, why
			t.AddEvidence("script", "negado: "+desc+" ("+why+")")
			return brStep{obs: obs, why: "permissão negada: " + why}
		}
	}
	if cmd != "" {
		cx.say("$ %s", cmd) // echo the command the script runs, like run_tests does
	}
	t.Budget.UsedCmds++
	cx.commands++
	res := r.scExec(cx.ctx, d, args, cx.env.Root)
	obs.Exit, obs.TimedOut = res.Exit, res.TimedOut
	switch {
	case cx.ctx.Err() == context.Canceled:
		return brStep{obs: obs, why: "cancelado"}
	case res.TimedOut:
		return brStep{obs: obs, why: name + " estourou o tempo (" + scTimeout.String() + ")"}
	case res.Err != nil:
		return brStep{obs: obs, why: name + " não iniciou: " + res.Err.Error()}
	}
	var v any
	if err := json.Unmarshal(bytes.TrimSpace(res.Stdout), &v); err != nil {
		tail := strings.TrimSpace(res.Stderr)
		if tail == "" {
			tail = strings.TrimSpace(string(res.Stdout))
		}
		obs.Output = truncateHeadTail(redactSecrets(tail), 4000)
		return brStep{obs: obs, why: fmt.Sprintf("%s não devolveu JSON (exit %d): %s", name, res.Exit, oneLine(tail, 200))}
	}
	obs.Output = truncateHeadTail(string(res.Stdout), 4000)
	ok, conf, errMsg := scStatus(v)
	t.AddEvidence("script", fmt.Sprintf("%s → exit %d, ok=%v, conf %.2f", desc, res.Exit, ok, conf))
	if !ok {
		if errMsg == "" {
			errMsg = "o script reportou falha"
		}
		return brStep{val: v, obs: obs, why: oneLine(errMsg, 300)}
	}
	for _, l := range brScriptLines(v, 40) {
		cx.say("%s", l)
	}
	return brStep{val: v, obs: obs, ok: true}
}

// brScriptLines renders a script's data payload for the terminal: the
// script's own one-line summary (data.text) first — that is the answer a
// person reads — then scalars inline, arrays one item per line (≤10),
// nested maps indented.
func brScriptLines(v any, max int) []string {
	data, _ := v.(map[string]any)["data"].(map[string]any)
	var lines []string
	if text, _ := data["text"].(string); strings.TrimSpace(text) != "" {
		lines = append(lines, strings.TrimSpace(text))
		rest := make(map[string]any, len(data))
		for k, x := range data {
			if k != "text" {
				rest[k] = x
			}
		}
		data = rest
	}
	var walk func(m map[string]any, pad string)
	walk = func(m map[string]any, pad string) {
		var keys []string
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if len(lines) >= max {
				if len(lines) == 0 || lines[len(lines)-1] != pad+"…" {
					lines = append(lines, pad+"…")
				}
				return
			}
			switch x := m[k].(type) {
			case map[string]any:
				lines = append(lines, pad+k+":")
				walk(x, pad+"  ")
			case []any:
				lines = append(lines, fmt.Sprintf("%s%s (%d):", pad, k, len(x)))
				for i, it := range x {
					if i == 10 || len(lines) >= max {
						lines = append(lines, pad+"  …")
						break
					}
					if m2, ok := it.(map[string]any); ok {
						lines = append(lines, pad+"  "+brScriptKV(m2))
					} else {
						lines = append(lines, pad+"  "+oneLine(fmt.Sprint(it), 200))
					}
				}
			default:
				lines = append(lines, pad+k+": "+oneLine(brScalarKV(k, x), 200))
			}
		}
	}
	walk(data, "")
	return lines
}

// brScalar prints a JSON scalar for people: whole numbers without
// scientific notation ("38281715712", not "3.8281715712e+10").
func brScalar(x any) string { return brScalarKV("", x) }

// brSizeKeys are fields that hold byte counts in the cardápio scripts.
var brSizeKeys = map[string]bool{"bytes": true, "size": true, "free": true, "total": true, "used": true, "avail": true, "available": true, "total_size": true, "compressed_size": true, "rx_bytes": true, "tx_bytes": true}

// brScalarKV: like brScalar, and byte counts (≥ 1 MiB in a size field)
// read as "35.6 GB" — a person reads sizes, not 11-digit integers.
func brScalarKV(key string, x any) string {
	if f, ok := x.(float64); ok && brSizeKeys[key] && f >= 1<<20 {
		units := []string{"KB", "MB", "GB", "TB"}
		v, u := f/1024, 0
		for v >= 1024 && u < len(units)-1 {
			v /= 1024
			u++
		}
		return fmt.Sprintf("%.1f %s", v, units[u])
	}
	if f, ok := x.(float64); ok && f == float64(int64(f)) && f < 1e18 && f > -1e18 {
		return fmt.Sprintf("%d", int64(f))
	}
	return fmt.Sprint(x)
}

// brScriptKV flattens a map item to `k=v k=v` (≤5 keys) for array items.
func brScriptKV(m map[string]any) string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for i, k := range keys {
		if i == 5 {
			parts = append(parts, "…")
			break
		}
		switch x := m[k].(type) {
		case nil:
			continue // an unknown value is not worth a "pid=<nil>" on screen
		case map[string]any, []any:
			b, _ := json.Marshal(x)
			parts = append(parts, k+"="+scCut(string(b), 60))
		default:
			parts = append(parts, k+"="+oneLine(brScalarKV(k, x), 60))
		}
	}
	return strings.Join(parts, " ")
}

func brResolve(cx *brCtx, in map[string]string) brStep {
	q := cx.str(in, "q")
	kinds := strings.Split(cx.str(in, "kinds"), ",")
	obs := rvNoCmd()
	if q == "" {
		obs.Cands = 0
		return brStep{obs: obs, why: "nome vazio"}
	}
	c := niResolveEntity(cx.env.Root, q, kinds, cx.str(in, "hint"), 8)
	obs.Cands = len(c)
	if len(c) > 0 {
		tied := 1
		for tied < len(c) && c[tied].Score-c[0].Score < 2*fzSlip*8 && !c[0].Exact {
			tied++
		}
		if c[0].Exact {
			tied = 0
			for _, x := range c {
				if x.Exact {
					tied++
				}
			}
		}
		obs.Tied = tied
		var names []string
		for i, x := range c {
			if i == 3 {
				break
			}
			names = append(names, x.Display())
		}
		cx.task.AddEvidence("resolve", fmt.Sprintf("'%s' → %d candidato(s): %s", q, len(c), strings.Join(names, "; ")))
		return brStep{val: c, obs: obs, ok: true}
	}
	if in["empty"] == "ok" {
		cx.task.AddEvidence("resolve", fmt.Sprintf("'%s' → nenhum candidato (%s)", q, strings.Join(kinds, ",")))
		return brStep{val: []niEntity{}, obs: obs, ok: true}
	}
	// name_not_found: show what is close, never invent the name
	sim := niSimilarPaths(cx.env.Root, q, 3*fzEdit)
	msg := fmt.Sprintf("não achei '%s'", q)
	if len(sim) > 0 {
		msg += "; parecidos: " + strings.Join(sim, ", ")
	}
	cx.task.AddEvidence("resolve", msg)
	return brStep{obs: obs, why: msg}
}

// brSafeTypo: the candidate is within one edit (plus a slip), or the typed
// word only dropped or doubled letters of it ("talscle" → "tailscale",
// "parsr" → "parser") — the fast-typing errors. "handler" → "HANDOFF.md"
// is neither, so it is confirmed first.
func brSafeTypo(e niEntity) bool {
	if e.Exact || e.Dist <= fzEdit+fzSlip {
		return true
	}
	q := fzKey(e.Query)
	k := fzKey(e.Name)
	if e.Kind == "file" || e.Kind == "test" || e.Kind == "dir" {
		base := e.Name
		if i := strings.LastIndexByte(base, '/'); i >= 0 {
			base = base[i+1:]
		}
		if j := strings.LastIndexByte(base, '.'); j > 0 && !strings.Contains(e.Query, ".") {
			base = base[:j]
		}
		k = fzKey(base)
	}
	return q != "" && (brSubseq(q, k) || brSubseq(k, q))
}

// brSubseq: every rune of a appears in b, in order.
func brSubseq(a, b string) bool {
	rb := []rune(b)
	j := 0
	for _, r := range a {
		for j < len(rb) && rb[j] != r {
			j++
		}
		if j == len(rb) {
			return false
		}
		j++
	}
	return true
}

// brPick decides whether candidates can be acted on without asking: one
// exact name, a single candidate within the typo limit, or a clear leader.
func brPick(c []niEntity) (int, string) {
	if len(c) == 0 {
		return -1, ""
	}
	exact := 0
	for _, x := range c {
		if x.Exact {
			exact++
		}
	}
	switch {
	case exact == 1 && c[0].Exact:
		return 0, "nome exato"
	case exact > 1:
		return -1, ""
	case len(c) == 1 && brSafeTypo(c[0]):
		return 0, "único nome parecido"
	case len(c) == 1:
		return -1, "" // one far candidate: confirm, never assume
	case c[1].Score-c[0].Score >= fzSlip*8:
		return 0, "bem mais parecido que o segundo (" + c[1].Display() + ")"
	}
	return -1, ""
}

func brChoose(cx *brCtx, in map[string]string) brStep {
	opts, _ := cx.subst(in["options"]).([]niEntity)
	what := in["what"]
	maxN, _ := strconv.Atoi(in["max"])
	if maxN <= 0 || maxN > 5 {
		maxN = 5
	}
	obs := rvNoCmd()
	if len(opts) == 0 {
		if in["empty"] == "ok" {
			return brStep{val: "", obs: obs, ok: true}
		}
		obs.Cands = 0
		return brStep{obs: obs, why: "nenhum " + what}
	}
	if i, why := brPick(opts); i >= 0 {
		e := opts[i]
		why2 := why
		if len(e.Why) > 0 {
			why2 += "; " + strings.Join(e.Why, "; ")
		}
		cx.task.AddEvidence("resolve", fmt.Sprintf("escolhido %s (%s)", e.Display(), why2))
		if !e.Exact {
			cx.say("entendi '%s' como %s (%s)", cx.lastQuery(), e.Display(), why)
		}
		return brStep{val: e.Name, obs: obs, ok: true}
	}
	if len(opts) > maxN {
		opts = opts[:maxN]
	}
	labels := make([]string, len(opts))
	for i, o := range opts {
		labels[i] = o.Display()
	}
	q := fmt.Sprintf("Qual %s você quis dizer com '%s'?", what, cx.lastQuery())
	cx.questions++
	idx := -1
	if cx.env.Ask != nil {
		idx = cx.env.Ask(q, labels)
	}
	if idx < 0 || idx >= len(opts) {
		var b strings.Builder
		b.WriteString(q + "\n")
		for i, l := range labels {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, l)
		}
		b.WriteString("responda com o número (ou o nome certo)")
		obs.Cands, obs.Tied = len(opts), len(opts)
		return brStep{obs: obs, why: b.String(), stop: tkNeedsInput}
	}
	e := opts[idx]
	cx.task.AddEvidence("answer", fmt.Sprintf("usuário escolheu %d: %s", idx+1, e.Display()))
	return brStep{val: e.Name, obs: obs, ok: true}
}

// lastQuery is the raw slot the current resolution came from.
func (cx *brCtx) lastQuery() string {
	for _, k := range []string{"target", "tool", "host", "service", "container", "query"} {
		if v := cx.task.Slots[k]; v != "" {
			return v
		}
	}
	return ""
}

func brAskSlot(cx *brCtx, in map[string]string) brStep {
	slot, q := in["slot"], in["q"]
	cx.questions++
	if cx.env.AskText != nil {
		if a, ok := cx.env.AskText(q); ok && strings.TrimSpace(a) != "" {
			if cx.task.Slots == nil {
				cx.task.Slots = map[string]string{}
			}
			cx.task.Slots[slot] = strings.TrimSpace(a)
			cx.task.AddEvidence("answer", q+" → "+a)
			return brStep{val: a, obs: rvNoCmd(), ok: true}
		}
	}
	return brStep{obs: rvNoCmd(), why: q, stop: tkNeedsInput}
}

// brMaxRead caps what read_file loads (the excerpt is smaller still).
const brMaxRead = 256 << 10

func brReadFile(cx *brCtx, in map[string]string) brStep {
	rel := cx.str(in, "path")
	obs := rvNoCmd()
	abs := filepath.Join(cx.env.Root, filepath.FromSlash(rel))
	fi, err := os.Stat(abs)
	if err != nil {
		obs.Cands = 0
		return brStep{obs: obs, why: "não consegui abrir " + rel + ": " + err.Error()}
	}
	if fi.IsDir() {
		ents, _ := os.ReadDir(abs)
		var names []string
		for i, e := range ents {
			if i == 40 {
				names = append(names, fmt.Sprintf("… +%d", len(ents)-40))
				break
			}
			n := e.Name()
			if e.IsDir() {
				n += "/"
			}
			names = append(names, n)
		}
		cx.say("%s/ (%d entradas)\n  %s", rel, len(ents), strings.Join(names, "  "))
		cx.task.AddEvidence("file", fmt.Sprintf("pasta %s, %d entradas", rel, len(ents)))
		return brStep{val: strings.Join(names, "\n"), obs: obs, ok: true}
	}
	f, err := os.Open(abs)
	if err != nil {
		return brStep{obs: obs, why: err.Error()}
	}
	buf := make([]byte, min(fi.Size(), brMaxRead))
	n, _ := f.Read(buf)
	f.Close()
	buf = buf[:n]
	sum := sha256.Sum256(buf)
	lines := strings.Split(string(buf), "\n")
	syms := brSymbols(rel, buf)
	niTouch(abs)
	cx.say("%s · %d bytes · %d linhas · modificado %s · sha256 %s", rel, fi.Size(), len(lines), fi.ModTime().Format("2006-01-02 15:04"), hex.EncodeToString(sum[:6]))
	if len(syms) > 0 {
		cx.say("símbolos: %s", strings.Join(syms, ", "))
	}
	show := 40
	if len(lines) < show {
		show = len(lines)
	}
	for i := 0; i < show; i++ {
		fmt.Fprintf(&cx.out, "%5d  %s\n", i+1, sanitizeTerm(oneLineKeep(lines[i], 200)))
	}
	if len(lines) > show {
		cx.say("… (%d linhas a mais)", len(lines)-show)
	}
	cx.task.AddEvidence("file", fmt.Sprintf("%s: %d bytes, %d linhas, mtime %s, sha256 %s, %d símbolo(s)", rel, fi.Size(), len(lines), fi.ModTime().Format(time.RFC3339), hex.EncodeToString(sum[:6]), len(syms)))
	return brStep{val: string(buf), obs: obs, ok: true}
}

// oneLineKeep clips a line without collapsing its indentation.
func oneLineKeep(s string, n int) string {
	s = strings.TrimRight(s, "\r")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// brSymbols are the top-level names of a file (nameindex regexes, cold path).
func brSymbols(rel string, b []byte) []string {
	lang := niSymLang(strings.ToLower(filepath.Ext(rel)))
	if lang == "" {
		return nil
	}
	if len(b) > niSymBytes {
		b = b[:niSymBytes]
	}
	var out []string
	seen := map[string]bool{}
	for _, m := range niSymRE[lang].FindAllSubmatch(b, -1) {
		s := string(m[1])
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
		if len(out) == 12 {
			break
		}
	}
	return out
}

// brTestRes is the outcome of run_tests.
type brTestRes struct {
	Cmd    string
	Exit   int
	Out    string
	Parsed *opResult
}

// brTestCmd narrows the project's test command to target: a resolved test
// file becomes its package/path; otherwise fpTestCommand's narrowing.
func brTestCmd(cx *brCtx, target, hint string) string {
	p := cx.proj
	if target == "" {
		return p.Test
	}
	c := niResolveEntity(cx.env.Root, target, []string{"test", "file"}, "test", 4)
	if i, _ := brPick(c); i >= 0 && (c[i].Kind == "test" || c[i].Kind == "file") {
		rel := c[i].Name
		cx.task.AddEvidence("resolve", fmt.Sprintf("alvo '%s' → %s", target, rel))
		switch {
		case p.Test == "go test ./...":
			dir := filepath.ToSlash(filepath.Dir(rel))
			if dir == "." {
				return "go test ."
			}
			return "go test ./" + dir
		case strings.HasPrefix(p.Test, "python3 -m pytest"), strings.HasSuffix(p.Test, " test") && p.PM != "":
			return fpTestCommand(p, rel)
		case strings.HasPrefix(p.Test, "python3 -m unittest"):
			return "python3 -m unittest " + fpShQuote(strings.TrimSuffix(strings.ReplaceAll(rel, "/", "."), ".py"))
		}
	}
	if p.Test == "go test ./..." {
		return "go test -run " + fpShQuote("(?i)"+target) + " ./..."
	}
	return fpTestCommand(p, target)
}

func brRunTests(cx *brCtx, in map[string]string) brStep {
	target := cx.str(in, "target")
	cmd := brTestCmd(cx, target, cx.str(in, "hint"))
	if retry, _ := cx.vars["retry_smaller"].(bool); retry && target != "" {
		cmd = cx.proj.Test // narrowing failed: the whole suite once
	}
	cx.say("$ %s", cmd)
	if f := strings.Fields(cmd); len(f) > 0 && cx.env.Run == nil && !strings.ContainsAny(f[0], "/=$") {
		if _, err := exec.LookPath(f[0]); err != nil { // needs: the runner binary is present
			obs := rvNoCmd()
			obs.Exit, obs.Output = 127, f[0]+": command not found"
			return brStep{obs: obs, why: f[0] + " não está no PATH: não dá pra rodar os testes"}
		}
	}
	out, exit, obs := cx.runCmd(cmd, 10*time.Minute)
	if obs.Exit < 0 || obs.Denied || obs.BudgetLeft == 0 {
		return brStep{obs: obs, why: obs.Output}
	}
	r := brTestRes{Cmd: cmd, Exit: exit, Out: out, Parsed: opParse(out)}
	obs.Parsed = r.Parsed != nil && r.Parsed.Data.Kind == "test"
	if obs.Parsed {
		obs.Failures = r.Parsed.Data.Failed + r.Parsed.Data.Errors
		d := r.Parsed.Data
		cx.say("%s: %d passaram · %d falharam · %d pulados", d.Format, d.Passed, d.Failed+d.Errors, d.Skipped)
		cx.task.AddEvidence("tests", fmt.Sprintf("%s exit %d: %d ok, %d falhas, %d pulados", d.Format, exit, d.Passed, d.Failed+d.Errors, d.Skipped))
	} else {
		cx.say("%s", strings.TrimRight(truncateHeadTail(out, 3000), "\n"))
	}
	cx.vars["result"] = r
	switch {
	case exit == 127 || exit == 126 || strings.HasPrefix(strings.TrimSpace(out), "bwrap:"):
		return brStep{val: r, obs: obs, why: "o comando de teste não rodou (exit " + strconv.Itoa(exit) + ")"}
	case obs.TimedOut:
		return brStep{val: r, obs: obs, why: "os testes estouraram o tempo"}
	}
	// a failing run is a result, not a broken step: test_context shows it
	return brStep{val: r, obs: obs, ok: true}
}

func brTestContext(cx *brCtx, in map[string]string) brStep {
	r, _ := cx.subst(in["result"]).(brTestRes)
	obs := rvNoCmd()
	if r.Parsed == nil || len(r.Parsed.Data.Failures) == 0 {
		return brStep{val: "", obs: obs, ok: true}
	}
	n := 0
	for _, f := range r.Parsed.Data.Failures {
		if n == 3 {
			cx.say("… +%d falha(s)", len(r.Parsed.Data.Failures)-3)
			break
		}
		n++
		loc := f.File
		if rel, err := filepath.Rel(cx.env.Root, f.File); err == nil && filepath.IsAbs(f.File) && !strings.HasPrefix(rel, "..") {
			loc = filepath.ToSlash(rel)
		}
		if f.Line > 0 {
			loc += ":" + strconv.Itoa(f.Line)
		}
		cx.say("✗ %s %s %s", f.Test, loc, oneLine(f.Message, 160))
		if f.File == "" || f.Line <= 0 {
			continue
		}
		abs := f.File
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(cx.env.Root, filepath.FromSlash(f.File))
			if _, err := os.Stat(abs); err != nil { // go prints the file relative to its package
				for _, c := range niResolveEntity(cx.env.Root, filepath.Base(f.File), []string{"test", "file"}, "test", 2) {
					if c.Exact {
						abs = filepath.Join(cx.env.Root, filepath.FromSlash(c.Name))
						break
					}
				}
			}
		}
		b, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		lines := strings.Split(string(b), "\n")
		lo, hi := max(1, f.Line-3), min(len(lines), f.Line+3)
		for k := lo; k <= hi; k++ {
			mark := " "
			if k == f.Line {
				mark = ">"
			}
			fmt.Fprintf(&cx.out, "  %s%4d  %s\n", mark, k, sanitizeTerm(oneLineKeep(lines[k-1], 160)))
		}
		cx.task.AddEvidence("file", fmt.Sprintf("contexto da falha %s:%d", f.File, f.Line))
	}
	return brStep{val: "ok", obs: obs, ok: true}
}

// brCmdRes is a finished read-only command.
type brCmdRes struct {
	Cmd  string
	Exit int
	Out  string
}

func brGitStatus(cx *brCtx, in map[string]string) brStep {
	cmd := "git status --short --branch"
	out, exit, obs := cx.runCmdAs(cmd, 20*time.Second, true)
	if obs.Exit < 0 || obs.Denied {
		return brStep{obs: obs, why: obs.Output}
	}
	cx.say("$ %s\n%s", cmd, strings.TrimRight(truncateHeadTail(out, 4000), "\n"))
	if exit != 0 {
		return brStep{val: brCmdRes{cmd, exit, out}, obs: obs, why: "git status falhou"}
	}
	changed := 0
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l != "" && !strings.HasPrefix(l, "##") {
			changed++
		}
	}
	if changed == 0 {
		cx.say("nada mudou desde o último commit")
	}
	cx.task.AddEvidence("cmd", fmt.Sprintf("git status: %d arquivo(s) com mudança", changed))
	return brStep{val: brCmdRes{cmd, exit, out}, obs: obs, ok: true}
}

func brEnvStatus(cx *brCtx, in map[string]string) brStep {
	p := cx.env.Profile
	if p == nil {
		p = epGet(false)
	}
	cx.say("%s", strings.TrimRight(epText(p), "\n"))
	proj := cx.proj
	if len(proj.Langs) > 0 || proj.Test != "" {
		cx.say("projeto   %s · testes: %s · build: %s · git: %v", strings.Join(proj.Langs, ","), orDash(proj.Test), orDash(proj.Build), proj.Git)
	}
	cx.task.AddEvidence("env", fmt.Sprintf("perfil %s/%s, %d ferramentas, %d hosts, %d portas", p.OS, p.Arch, len(p.Toolchains), len(p.SSHHosts)+len(p.Tailscale), len(p.Ports)))
	return brStep{val: p, obs: rvNoCmd(), ok: true}
}

// brSelfInfo is jazz's knowledge about itself: version, where the binary
// lives, how the request is being answered, and what the deterministic brain
// covers. Same idea as `jazz version` but inside the brain.
func brSelfInfo(cx *brCtx, in map[string]string) brStep {
	exe, _ := os.Executable()
	cx.say("jazz      v%s · %s/%s · %s", version, runtime.GOOS, runtime.GOARCH, exe)
	lib, _ := mhLibrary()
	nMethods := 0
	for _, ms := range lib {
		nMethods += len(ms)
	}
	cx.say("cérebro   %d intents · %d métodos determinísticos — essa resposta não usou modelo", len(lib), nMethods)
	if olForcedEnv() {
		cx.say("modo      offline forçado — modelo nunca é chamado")
	}
	cx.say("dados     %s", dataDir())
	p := cx.env.Profile
	if p == nil {
		p = epGet(false)
	}
	if p != nil && (p.Host != "" || p.Machine != "") {
		where := strings.TrimSpace(strings.Join([]string{p.Host, p.Machine}, " · "))
		cx.say("máquina   %s ('status da máquina' detalha)", where)
	}
	cx.task.AddEvidence("self", fmt.Sprintf("jazz %s %s/%s em %s", version, runtime.GOOS, runtime.GOARCH, exe))
	return brStep{val: version, obs: rvNoCmd(), ok: true}
}

func brToolReport(cx *brCtx, in map[string]string) brStep {
	name, asked := cx.str(in, "name"), cx.str(in, "asked")
	p := cx.env.Profile
	if p == nil {
		p = epGet(false)
	}
	if name == "" {
		cx.say("%s não está instalado: nada com esse nome (nem parecido) entre os %d binários do PATH", asked, len(p.PathBins))
		cx.task.AddEvidence("env", fmt.Sprintf("'%s' ausente do PATH (%d binários)", asked, len(p.PathBins)))
		return brStep{val: p, obs: rvNoCmd(), ok: true}
	}
	for _, t := range p.Toolchains {
		if t.Name == name {
			v := t.Version
			if v == "" {
				v = "versão desconhecida"
			}
			cx.say("%s instalado: %s (%s)", name, v, t.Path)
			cx.task.AddEvidence("env", fmt.Sprintf("%s %s em %s", name, v, t.Path))
			return brStep{val: p, obs: rvNoCmd(), ok: true}
		}
	}
	path := ""
	if lp, err := exec.LookPath(name); err == nil {
		path = lp
	}
	if path == "" && !epHasBin(p, name) {
		cx.say("%s não está no PATH", name)
		cx.task.AddEvidence("env", name+" ausente do PATH")
		return brStep{val: p, obs: rvNoCmd(), ok: true}
	}
	cx.say("%s instalado (%s)", name, orDash(path))
	cx.task.AddEvidence("env", fmt.Sprintf("%s no PATH (%s)", name, orDash(path)))
	return brStep{val: p, obs: rvNoCmd(), ok: true}
}

// brToolStatusCmds is the read-only status command of each tool the brain
// knows. Anything else is answered with the tool's path and version.
var brToolStatusCmds = map[string]string{
	"git":       "git status --short --branch",
	"tailscale": "tailscale status",
	"docker":    "docker ps",
	"podman":    "podman ps",
	"systemctl": "systemctl is-system-running",
}

// brToolStatus runs the read-only status command of the resolved tool
// ("talscle status" → tailscale status). Entity-first: the generic verb is
// scoped to the tool. A tool with no safe status command gets its info.
func brToolStatus(cx *brCtx, in map[string]string) brStep {
	name, asked := cx.str(in, "name"), cx.str(in, "asked")
	p := cx.env.Profile
	if p == nil {
		p = epGet(false)
	}
	if name == "" {
		w := orDash(asked)
		cx.task.AddEvidence("env", "'"+asked+"' ausente do PATH ("+strconv.Itoa(len(p.PathBins))+" binários)")
		cx.cat = rvCommandMissing
		return brStep{obs: rvNoCmd(), why: w + " não está instalado: nada com esse nome (nem parecido) entre os binários do PATH", stop: tkBlocked}
	}
	path := ""
	if lp, err := exec.LookPath(name); err == nil {
		path = lp
	}
	if path == "" && !epHasBin(p, name) && !epHasBin(p, name+".exe") {
		cx.task.AddEvidence("env", name+" ausente do PATH")
		cx.cat = rvCommandMissing
		return brStep{obs: rvNoCmd(), why: name + " não está no PATH: não dá pra ver o status", stop: tkBlocked}
	}
	cmd, known := brToolStatusCmds[name]
	if !known {
		step := brToolReport(cx, map[string]string{"name": name, "asked": asked})
		cx.say("não conheço um comando de status seguro para %s: só informei o caminho e a versão", name)
		if step.ok {
			step.val = name
		}
		return step
	}
	out, exit, obs := cx.runCmdAs(cmd, 20*time.Second, true)
	if obs.Exit < 0 || obs.Denied {
		return brStep{obs: obs, why: obs.Output}
	}
	body := strings.TrimRight(truncateHeadTail(out, 4000), "\n")
	if exit != 0 {
		cx.say("$ %s (saiu com código %d)\n%s", cmd, exit, body)
	} else {
		cx.say("$ %s\n%s", cmd, body)
	}
	cx.task.AddEvidence("cmd", fmt.Sprintf("status de %s: `%s` → exit %d", name, cmd, exit))
	return brStep{val: brCmdRes{cmd, exit, out}, obs: obs, ok: true}
}

func brHostReport(cx *brCtx, in map[string]string) brStep {
	name := cx.str(in, "name")
	p := cx.env.Profile
	if p == nil {
		p = epGet(false)
	}
	if p.Host != "" && strings.EqualFold(name, p.Host) { // asking about the machine we are on
		cx.say("%s é ESTA máquina — o jazz está rodando nela agora (%s · %s/%s); comandos executam aqui, sem ssh. 'status da máquina' detalha.",
			name, orDash(strings.Trim(p.Machine+" · "+p.OSName, " ·")), p.OS, p.Arch)
		cx.task.AddEvidence("env", "hostname local = "+p.Host)
		return brStep{val: name, obs: rvNoCmd(), ok: true}
	}
	for _, h := range p.SSHHosts {
		if h.Name == name {
			d := h.HostName
			if h.User != "" {
				d = h.User + "@" + d
			}
			cx.say("host SSH %s (%s): para entrar, rode !ssh %s", name, orDash(d), name)
			cx.task.AddEvidence("env", "host "+name+" em ~/.ssh/config")
			return brStep{val: name, obs: rvNoCmd(), ok: true}
		}
	}
	for _, t := range p.Tailscale {
		if t.Name == name {
			st := "offline"
			if t.Online {
				st = "online"
			}
			cx.say("máquina Tailscale %s (%s, %s segundo o tailscale status): para entrar, rode !ssh %s", name, orDash(t.IP), st, name)
			cx.task.AddEvidence("env", "peer Tailscale "+name+" "+st)
			return brStep{val: name, obs: rvNoCmd(), ok: true}
		}
	}
	return brStep{obs: rvNoCmd(), why: name + " sumiu do perfil"}
}

func brReport(cx *brCtx, in map[string]string) brStep {
	text := cx.str(in, "text")
	st := tkStatus(in["status"])
	if st == "" {
		st = tkBlocked
	}
	if c := in["category"]; c != "" {
		cx.cat = rvCat(c)
	}
	return brStep{obs: rvNoCmd(), why: text, stop: st}
}

// ---------- verification ----------

// brVerify runs a method's checks: ok, or why not; unavailable = nothing
// could check (the result is "unverified", never "succeeded").
//
// Every check that passes leaves one human proof line in the answer
// ("conferi: README.md existe (49 bytes)"): "feito e verificado" without the
// proof is the kind of verdict the offline brain must not print (P4/P5).
func brVerify(cx *brCtx, m mhMethod) (ok bool, unavailable bool, why string) {
	if len(m.Verify) == 0 {
		return false, true, "este método não tem verificador"
	}
	var proofs []string
	for _, c := range m.Verify {
		arg := cx.subst(c.Arg)
		switch c.Check {
		case "file_exists":
			rel, _ := arg.(string)
			// archive_op create devolve mapa {path,packed,files} — o path
			// continua sendo a prova de existência.
			if rel == "" {
				if mm, ok := arg.(map[string]any); ok {
					rel, _ = mm["path"].(string)
				}
			}
			// Lstat: um symlink criado pela cap verifica mesmo se o alvo
			// ainda não existir — quem existe é o link.
			fi, err := os.Lstat(filepath.Join(cx.env.Root, filepath.FromSlash(rel)))
			if rel == "" || err != nil {
				return false, false, "o arquivo não existe: " + rel
			}
			proofs = append(proofs, brProofFile(rel, fi))
		case "file_gone":
			rel, _ := arg.(string)
			if rel == "" {
				return false, false, "sem caminho pra conferir"
			}
			if _, err := os.Stat(filepath.Join(cx.env.Root, filepath.FromSlash(rel))); err == nil {
				return false, false, "o arquivo ainda existe: " + rel
			}
			proofs = append(proofs, rel+" não existe mais")
		case "arc_contains":
			// prova real de conteúdo: relê o pacote e exige cada fonte
			// declarada lá dentro — archive "criado" mas vazio falha.
			mm, ok := arg.(map[string]any)
			if !ok {
				return false, false, "arc_contains: resultado sem mapa"
			}
			path, _ := mm["path"].(string)
			want, _ := mm["files"].([]any)
			if path == "" || len(want) == 0 {
				return false, false, "arc_contains: sem path/files"
			}
			got, err := brArcList(filepath.Join(cx.env.Root, filepath.FromSlash(path)))
			if err != nil {
				return false, false, "arc_contains: " + err.Error()
			}
			miss := 0
			for _, w := range want {
				ws, ok := w.(string)
				if !ok {
					continue
				}
				ws = strings.TrimPrefix(strings.TrimSuffix(ws, "/"), "./")
				if !got[ws] {
					miss++
				}
			}
			if miss > 0 {
				return false, false, fmt.Sprintf("arc_contains: %d fonte(s) fora do pacote", miss)
			}
			proofs = append(proofs, fmt.Sprintf("pacote %s contém %d fonte(s)", path, len(want)))
		case "list_gone":
			// every path a producer listed must be gone (mass-delete proof)
			mm, _ := arg.(map[string]any)
			data, _ := mm["data"].(map[string]any)
			var items []any
			for _, k := range []string{"files", "items", "results", "matches", "entries"} {
				if arr, ok := data[k].([]any); ok {
					items = arr
					break
				}
			}
			if len(items) == 0 {
				return false, false, "sem lista produzida pra conferir"
			}
			left := 0
			for _, it := range items {
				p := brItemPath(it)
				if p == "" || strings.Contains(p, "..") || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") {
					continue
				}
				if _, err := os.Stat(filepath.Join(cx.env.Root, filepath.FromSlash(p))); err == nil {
					left++
				}
			}
			if left > 0 {
				return false, false, fmt.Sprintf("%d item(ns) da lista ainda existe(m)", left)
			}
			proofs = append(proofs, fmt.Sprintf("%d item(ns) confirmados ausentes", len(items)))
		case "list_lacks":
			// replace fan-out: nenhum item produzido pode conter a agulha.
			mm, _ := arg.(map[string]any)
			data, _ := mm["data"].(map[string]any)
			var items []any
			for _, k := range []string{"files", "items", "results", "matches", "entries"} {
				if arr, ok := data[k].([]any); ok {
					items = arr
					break
				}
			}
			if len(items) == 0 {
				return false, false, "sem lista produzida pra conferir"
			}
			needle, _ := cx.subst(c.Arg2).(string)
			if needle == "" {
				return false, false, "list_lacks sem agulha"
			}
			bad := 0
			for _, it := range items {
				p := brItemPath(it)
				if p == "" || strings.Contains(p, "..") || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") {
					continue
				}
				data, err := os.ReadFile(filepath.Join(cx.env.Root, filepath.FromSlash(p)))
				if err != nil || bytes.IndexByte(data, 0) >= 0 {
					continue // ilegível ou binário: o replace também pula — mesmo universo
				}
				if strings.Contains(string(data), needle) {
					bad++
				}
			}
			if bad > 0 {
				return false, false, fmt.Sprintf("%d item(ns) ainda contém %q", bad, needle)
			}
			proofs = append(proofs, fmt.Sprintf("%d item(ns) sem %q", len(items), needle))
		case "list_present":
			// fan-out de cópia/move: cada item produzido pousou como
			// <arg2>/<base(item)> — prova o destino, não a origem.
			mm, _ := arg.(map[string]any)
			data, _ := mm["data"].(map[string]any)
			var items []any
			for _, k := range []string{"files", "items", "results", "matches", "entries"} {
				if arr, ok := data[k].([]any); ok {
					items = arr
					break
				}
			}
			if len(items) == 0 {
				return false, false, "sem lista produzida pra conferir"
			}
			dir, _ := cx.subst(c.Arg2).(string)
			dAbs, dRel, dErr := brSafePath(cx.env.Root, dir)
			if dErr != nil || dRel == "" {
				return false, false, "dir do list_present fora do projeto: " + dir
			}
			missing := 0
			for _, it := range items {
				p := brItemPath(it)
				if p == "" {
					missing++
					continue
				}
				if _, err := os.Lstat(filepath.Join(dAbs, filepath.Base(filepath.FromSlash(p)))); err != nil {
					missing++
				}
			}
			if missing > 0 {
				return false, false, fmt.Sprintf("%d item(ns) não pousaram em %s", missing, dRel)
			}
			proofs = append(proofs, fmt.Sprintf("conferi: %d item(ns) em %s", len(items), dRel))
		case "list_exists":
			// cada path produzido existe sob o root — prova de extract/
			// fan-out cujo destino já vem completo no item.
			mm, _ := arg.(map[string]any)
			data, _ := mm["data"].(map[string]any)
			var items []any
			for _, k := range []string{"files", "items", "results", "matches", "entries"} {
				if arr, ok := data[k].([]any); ok {
					items = arr
					break
				}
			}
			if len(items) == 0 {
				return false, false, "sem lista produzida pra conferir"
			}
			missing := 0
			for _, it := range items {
				p := brItemPath(it)
				if p == "" || strings.Contains(p, "..") || strings.HasPrefix(p, "/") {
					missing++
					continue
				}
				if _, err := os.Lstat(filepath.Join(cx.env.Root, filepath.FromSlash(p))); err != nil {
					missing++
				}
			}
			if missing > 0 {
				return false, false, fmt.Sprintf("%d item(ns) produzidos não existem", missing)
			}
			proofs = append(proofs, fmt.Sprintf("conferi: %d item(ns) existem", len(items)))
		case "file_mode":
			// arg=path, arg2=octal — prova o chmod comparando bits reais
			rel, _ := arg.(string)
			want, _ := cx.subst(c.Arg2).(string)
			if rel == "" || want == "" {
				return false, false, "file_mode sem caminho ou modo"
			}
			wm, okm := brParseOctal(want)
			if !okm {
				return false, false, "file_mode com modo não-octal: " + want
			}
			fAbs, fRel, fErr := brSafePath(cx.env.Root, rel)
			if fErr != nil {
				return false, false, fErr.Error()
			}
			fi, err := os.Stat(fAbs)
			if err != nil {
				return false, false, "não consegui stat " + fRel
			}
			var special os.FileMode
			if fi.Mode()&os.ModeSetuid != 0 {
				special |= 0o4000
			}
			if fi.Mode()&os.ModeSetgid != 0 {
				special |= 0o2000
			}
			if fi.Mode()&os.ModeSticky != 0 {
				special |= 0o1000
			}
			got := fi.Mode().Perm() | special
			if got != wm {
				return false, false, fmt.Sprintf("%s tem modo %04o, esperava %04o", fRel, got, wm)
			}
			proofs = append(proofs, fmt.Sprintf("%s modo %04o", fRel, got))
		case "file_owner", "file_group":
			// arg=path, arg2=nome — prova o chown/chgrp contra a conta real
			rel, _ := arg.(string)
			want, _ := cx.subst(c.Arg2).(string)
			if rel == "" || want == "" {
				return false, false, c.Check + " sem caminho ou nome"
			}
			fAbs, fRel, fErr := brSafePath(cx.env.Root, rel)
			if fErr != nil {
				return false, false, fErr.Error()
			}
			fi, err := os.Stat(fAbs)
			if err != nil {
				return false, false, "não consegui stat " + fRel
			}
			st, ok := fi.Sys().(*syscall.Stat_t)
			if !ok {
				return false, true, "stat_t indisponível nesta plataforma"
			}
			var gotName string
			var id uint32
			if c.Check == "file_owner" {
				id = st.Uid
				if u, err := user.LookupId(strconv.Itoa(int(st.Uid))); err == nil {
					gotName = u.Username
				}
			} else {
				id = st.Gid
				if g, err := user.LookupGroupId(strconv.Itoa(int(st.Gid))); err == nil {
					gotName = g.Name
				}
			}
			if want != gotName && want != strconv.Itoa(int(id)) {
				return false, false, fmt.Sprintf("%s %s=%s(%d), esperava %s", fRel, c.Check, gotName, id, want)
			}
			proofs = append(proofs, fmt.Sprintf("%s %s=%s", fRel, c.Check, gotName))
		case "list_mode":
			// arg=lista produzida, arg2=octal — prova massa de chmod
			mm, _ := arg.(map[string]any)
			data, _ := mm["data"].(map[string]any)
			var items []any
			for _, k := range []string{"files", "items", "results", "matches", "entries"} {
				if arr, ok := data[k].([]any); ok {
					items = arr
					break
				}
			}
			if len(items) == 0 {
				return false, false, "sem lista produzida pra conferir"
			}
			want, _ := cx.subst(c.Arg2).(string)
			wm, okm := brParseOctal(want)
			if !okm {
				return false, false, "list_mode com modo não-octal: " + want
			}
			bad := 0
			for _, it := range items {
				p := brItemPath(it)
				if p == "" || strings.Contains(p, "..") || strings.HasPrefix(p, "/") {
					bad++
					continue
				}
				fi, err := os.Stat(filepath.Join(cx.env.Root, filepath.FromSlash(p)))
				if err != nil || fi.Mode().Perm() != wm&0o777 {
					bad++
				}
			}
			if bad > 0 {
				return false, false, fmt.Sprintf("%d item(ns) sem modo %04o", bad, wm)
			}
			proofs = append(proofs, fmt.Sprintf("%d item(ns) em modo %04o", len(items), wm))
		case "file_perm", "file_noperm":
			// arg=path, arg2=r|w|x — prova bit de permissão presente/ausente
			// em qualquer classe (executável = algum x ligado).
			rel, _ := arg.(string)
			bit, _ := cx.subst(c.Arg2).(string)
			var mask os.FileMode
			switch bit {
			case "r":
				mask = 0o444
			case "w":
				mask = 0o222
			case "x":
				mask = 0o111
			default:
				return false, false, "file_perm com bit desconhecido: " + bit
			}
			fAbs, fRel, fErr := brSafePath(cx.env.Root, rel)
			if fErr != nil {
				return false, false, fErr.Error()
			}
			fi, err := os.Stat(fAbs)
			if err != nil {
				return false, false, "não consegui stat " + fRel
			}
			has := fi.Mode().Perm()&mask != 0
			if c.Check == "file_perm" && !has {
				return false, false, fmt.Sprintf("%s não tem bit %s", fRel, bit)
			}
			if c.Check == "file_noperm" && has {
				return false, false, fmt.Sprintf("%s ainda tem bit %s", fRel, bit)
			}
			verbo := map[string]string{"file_perm": "tem", "file_noperm": "não tem"}[c.Check]
			proofs = append(proofs, fmt.Sprintf("%s %s bit %s", fRel, verbo, bit))
		case "list_perm":
			// arg=lista, arg2=r|w|x — todo item produzido tem o bit.
			mm, _ := arg.(map[string]any)
			data, _ := mm["data"].(map[string]any)
			var items []any
			for _, k := range []string{"files", "items", "results", "matches", "entries"} {
				if arr, ok := data[k].([]any); ok {
					items = arr
					break
				}
			}
			if len(items) == 0 {
				return false, false, "sem lista produzida pra conferir"
			}
			bit, _ := cx.subst(c.Arg2).(string)
			var mask os.FileMode
			switch bit {
			case "r":
				mask = 0o444
			case "w":
				mask = 0o222
			case "x":
				mask = 0o111
			default:
				return false, false, "list_perm com bit desconhecido: " + bit
			}
			nobit := 0
			for _, it := range items {
				p := brItemPath(it)
				if p == "" || strings.Contains(p, "..") || strings.HasPrefix(p, "/") {
					nobit++
					continue
				}
				fi, err := os.Stat(filepath.Join(cx.env.Root, filepath.FromSlash(p)))
				if err != nil || fi.Mode().Perm()&mask == 0 {
					nobit++
				}
			}
			if nobit > 0 {
				return false, false, fmt.Sprintf("%d item(ns) sem bit %s", nobit, bit)
			}
			proofs = append(proofs, fmt.Sprintf("%d item(ns) com bit %s", len(items), bit))
		case "list_owner", "list_group":
			// arg=lista, arg2=nome — todo item produzido pertence a quem pediu.
			mm, _ := arg.(map[string]any)
			data, _ := mm["data"].(map[string]any)
			var items []any
			for _, k := range []string{"files", "items", "results", "matches", "entries"} {
				if arr, ok := data[k].([]any); ok {
					items = arr
					break
				}
			}
			if len(items) == 0 {
				return false, false, "sem lista produzida pra conferir"
			}
			want, _ := cx.subst(c.Arg2).(string)
			mism := 0
			for _, it := range items {
				p := brItemPath(it)
				if p == "" || strings.Contains(p, "..") || strings.HasPrefix(p, "/") {
					mism++
					continue
				}
				fi, err := os.Stat(filepath.Join(cx.env.Root, filepath.FromSlash(p)))
				if err != nil {
					mism++
					continue
				}
				st, ok := fi.Sys().(*syscall.Stat_t)
				if !ok {
					return false, true, "stat_t indisponível nesta plataforma"
				}
				var gotName string
				var id uint32
				if c.Check == "list_owner" {
					id = st.Uid
					if u, err := user.LookupId(strconv.Itoa(int(st.Uid))); err == nil {
						gotName = u.Username
					}
				} else {
					id = st.Gid
					if g, err := user.LookupGroupId(strconv.Itoa(int(st.Gid))); err == nil {
						gotName = g.Name
					}
				}
				if want != gotName && want != strconv.Itoa(int(id)) {
					mism++
				}
			}
			if mism > 0 {
				return false, false, fmt.Sprintf("%d item(ns) com %s diferente de %s", mism, c.Check, want)
			}
			proofs = append(proofs, fmt.Sprintf("%d item(ns) com %s=%s", len(items), c.Check, want))
		case "file_contains", "file_lacks":
			// arg=path, arg2=needle — a escrita do replace prova-se lendo
			rel, _ := arg.(string)
			needle, _ := cx.subst(c.Arg2).(string)
			if rel == "" || needle == "" {
				return false, false, "file_contains sem caminho ou agulha"
			}
			fAbs, fRel, fErr := brSafePath(cx.env.Root, rel)
			if fErr != nil {
				return false, false, fErr.Error()
			}
			data, err := os.ReadFile(fAbs)
			if err != nil {
				return false, false, "não consegui ler " + fRel
			}
			has := strings.Contains(string(data), needle)
			if c.Check == "file_contains" && !has {
				return false, false, fmt.Sprintf("%s não contém %q", fRel, needle)
			}
			if c.Check == "file_lacks" && has {
				return false, false, fmt.Sprintf("%s ainda contém %q", fRel, needle)
			}
			proofs = append(proofs, fmt.Sprintf("%s %s %q", fRel,
				map[string]string{"file_contains": "contém", "file_lacks": "não contém"}[c.Check], needle))
		case "read_ok":
			if _, isStr := arg.(string); !isStr {
				return false, false, "o conteúdo não foi lido"
			}
			proofs = append(proofs, "conteúdo lido")
		case "exit_zero":
			r, isRes := arg.(brCmdRes)
			if !isRes || r.Exit != 0 {
				return false, false, "o comando não terminou com 0"
			}
			proofs = append(proofs, brProofCmd(r.Cmd, "saiu com exit 0"))
		case "cmd_ran":
			// "o comando respondeu" — informative answers may exit non-zero
			// (systemctl status = 3 on inactive units); 126/127 = didn't run
			r, isRes := arg.(brCmdRes)
			if !isRes || r.Exit < 0 || r.Exit == 126 || r.Exit == 127 {
				return false, false, "o comando não rodou"
			}
			proofs = append(proofs, brProofCmd(r.Cmd, fmt.Sprintf("respondeu (exit %d)", r.Exit)))
		case "status_obtained":
			if arg == nil || arg == "" {
				return false, false, "não consegui o status da ferramenta"
			}
			proofs = append(proofs, fmt.Sprintf("status de %v obtido", arg))
		case "each_clean":
			// for_each emitiu contagens: todo item do conjunto produzido foi
			// processado — zero falhas e ao menos um feito (o conjunto não
			// foi varrido pela metade nem vazio de propósito)
			m, _ := arg.(map[string]any)
			data, _ := m["data"].(map[string]any)
			done, failed, total := brToFloat(data["done"]), brToFloat(data["failed"]), brToFloat(data["total"])
			if failed > 0 || done == 0 {
				return false, false, fmt.Sprintf("for_each: %.0f falha(s) de %.0f itens", failed, total)
			}
			proofs = append(proofs, fmt.Sprintf("for_each limpo: %.0f/%.0f processados", done, total))
		case "file_sorted":
			// Arg = arquivo; Arg2 = spec "flags=numeric,reverse key=N delim=X"
			// — o verificador repete o comparador do sort, não confia no exit.
			spec, _ := cx.subst(c.Arg2).(string)
			flags, key, delim := "", 0, ""
			for _, kv := range strings.Fields(spec) {
				switch {
				case strings.HasPrefix(kv, "flags="):
					flags = "," + kv[6:] + ","
				case strings.HasPrefix(kv, "key="):
					key, _ = strconv.Atoi(kv[4:])
				case strings.HasPrefix(kv, "delim="):
					delim = kv[6:]
				}
			}
			numeric, reverse := strings.Contains(flags, ",numeric,"), strings.Contains(flags, ",reverse,")
			sp, _ := arg.(string)
			if sp == "" {
				return false, true, "sort check sem arquivo"
			}
			data2, err := os.ReadFile(filepath.Join(cx.env.Root, filepath.FromSlash(sp)))
			if err != nil {
				return false, false, "sort check: " + err.Error()
			}
			lns := strings.Split(strings.TrimRight(string(data2), "\n"), "\n")
			keyOf := func(l string) string {
				if key <= 0 {
					return l
				}
				var f []string
				if delim != "" {
					f = strings.Split(l, delim)
				} else {
					f = strings.Fields(l)
				}
				if key > len(f) {
					return ""
				}
				return f[key-1]
			}
			strictLess := func(a, b string) bool {
				ka, kb := keyOf(a), keyOf(b)
				if numeric {
					fa, _ := strconv.ParseFloat(strings.TrimSpace(ka), 64)
					fb, _ := strconv.ParseFloat(strings.TrimSpace(kb), 64)
					if fa != fb {
						return fa < fb
					}
					return ka < kb
				}
				return ka < kb
			}
			bad := -1
			for i := 1; i < len(lns); i++ {
				// desordem = o anterior viria DEPOIS do atual na ordem pedida
				outOfOrder := strictLess(lns[i], lns[i-1])
				if reverse {
					outOfOrder = strictLess(lns[i-1], lns[i])
				}
				if outOfOrder {
					bad = i
					break
				}
			}
			if bad >= 0 {
				return false, false, fmt.Sprintf("arquivo não está ordenado na linha %d", bad+1)
			}
			proofs = append(proofs, fmt.Sprintf("%s está ordenado (%d linhas)", arg, len(lns)))
		case "out_sorted":
			// o cap devolveu as linhas que imprimiu + a spec que usou — o
			// verificador repete a checagem de ordem com código próprio:
			// um comparador quebrado no sort não engana este check.
			om, _ := arg.(map[string]any)
			od, _ := om["data"].(map[string]any)
			var lns []string
			switch arr := od["lines"].(type) {
			case []string:
				lns = arr
			case []any:
				for _, x := range arr {
					if s, ok := x.(string); ok {
						lns = append(lns, s)
					}
				}
			}
			if len(lns) == 0 {
				return false, false, "o sort não devolveu linhas pra conferir"
			}
			onum, _ := od["reverse"].(bool)
			oflags, _ := od["flags"].(string)
			okey := int(brToFloat(od["key"]))
			odelim, _ := od["delim"].(string)
			onumeric := strings.Contains(","+oflags+",", ",numeric,")
			okeyOf := func(l string) string {
				if okey <= 0 {
					return l
				}
				var f []string
				if odelim != "" {
					f = strings.Split(l, odelim)
				} else {
					f = strings.Fields(l)
				}
				if okey > len(f) {
					return ""
				}
				return f[okey-1]
			}
			oLess := func(a, b string) bool {
				ka, kb := okeyOf(a), okeyOf(b)
				if onumeric {
					fa, _ := strconv.ParseFloat(strings.TrimSpace(ka), 64)
					fb, _ := strconv.ParseFloat(strings.TrimSpace(kb), 64)
					if fa != fb {
						return fa < fb
					}
					return ka < kb
				}
				return ka < kb
			}
			for i := 1; i < len(lns); i++ {
				oo := oLess(lns[i], lns[i-1])
				if onum {
					oo = oLess(lns[i-1], lns[i])
				}
				if oo {
					return false, false, fmt.Sprintf("saída fora de ordem na linha %d", i+1)
				}
			}
			proofs = append(proofs, fmt.Sprintf("saída ordenada conferida (%d linhas)", len(lns)))
		case "script_ok":
			sm, _ := arg.(map[string]any)
			if ok, _ := sm["ok"].(bool); !ok {
				return false, false, "o script não confirmou sucesso"
			}
			proofs = append(proofs, "script confirmou sucesso")
		case "script_exit_zero":
			// a script that runs a command reports data.exit_code — parse
			// scripts emit ok:true even when the command failed, so the
			// exit code is what separates "ran clean" from "found errors"
			em, _ := arg.(map[string]any)
			data, _ := em["data"].(map[string]any)
			code, has := data["exit_code"].(float64)
			if !has {
				return false, true, "o script não informou o exit code do comando"
			}
			if code != 0 {
				return false, false, fmt.Sprintf("o comando saiu com exit %d (diagnóstico acima)", int(code))
			}
			proofs = append(proofs, "script rodou com exit 0")
		case "profile_present":
			if arg == nil || arg == "" {
				return false, false, "sem perfil do ambiente"
			}
			proofs = append(proofs, "perfil do ambiente lido")
		case "tests_passed":
			r, isRes := arg.(brTestRes)
			if !isRes {
				return false, true, "os testes não rodaram"
			}
			if r.Parsed == nil || r.Parsed.Data.Kind != "test" {
				if r.Exit == 0 {
					return false, true, "o comando passou, mas não reconheci a saída de testes: não dá pra afirmar quantos rodaram"
				}
				return false, false, fmt.Sprintf("os testes falharam (exit %d)", r.Exit)
			}
			d := r.Parsed.Data
			if d.Failed+d.Errors > 0 || r.Exit != 0 {
				return false, false, fmt.Sprintf("%d teste(s) falharam", max(1, d.Failed+d.Errors))
			}
			if d.Passed == 0 && d.Total == 0 && len(d.Suites) == 0 {
				return false, true, "nenhum teste rodou"
			}
			proofs = append(proofs, brProofTests(d))
		default:
			return false, true, "verificador desconhecido: " + c.Check
		}
	}
	for _, p := range proofs { // the proof goes at the end of the body, before the status line
		cx.say("conferi: %s", p)
	}
	return true, false, ""
}

// brProofFile is the evidence of a file_exists check, in human language.
func brProofFile(rel string, fi os.FileInfo) string {
	if fi.IsDir() {
		return rel + " existe (pasta)"
	}
	return fmt.Sprintf("%s existe (%s)", rel, brProofSize(fi.Size()))
}

func brProofSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d bytes", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}

// brProofCmd names the command that answered, when the method recorded it.
func brProofCmd(cmd, what string) string {
	if cmd == "" {
		return "comando " + what
	}
	return "`" + cmd + "` " + what
}

// brProofTests is the evidence of a tests_passed check.
func brProofTests(d opData) string {
	if d.Passed > 0 {
		return fmt.Sprintf("%d teste(s) passaram · 0 falhas", d.Passed)
	}
	if len(d.Suites) > 0 {
		return "suíte passou · 0 falhas"
	}
	return "testes passaram (exit 0)"
}
