package main

// hooks.go: user hooks compatible with Claude Code settings.json (G10). Owner: platform.
// Contract surface (CONTRACT.md): Hook* event names, HookResult, HookSet,
// LoadHooks, Has, Run. All methods are nil-safe (nil = no hooks).
//
// Sources, run in this order: config.json "hooks", ~/.claude/settings.json,
// then (only when the project policy is trusted, see trust.go)
// .claude/settings.json, .claude/settings.local.json and .jazz/settings.json.
// Each hook runs `bash -c <command>` in the project root with the JSON payload
// on stdin, sequentially (no CPU spikes on small machines), 60 s by default.
// Exit 0: stdout may be JSON {decision, reason, hookSpecificOutput:{...}};
// exit 2: block, stderr is the reason fed to the model; other codes: warning.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	HookPreToolUse       = "PreToolUse"
	HookPostToolUse      = "PostToolUse"
	HookUserPromptSubmit = "UserPromptSubmit"
	HookStop             = "Stop"
	HookSessionStart     = "SessionStart"
)

// HookResult is the merged outcome of every hook that matched one event.
type HookResult struct {
	Block        bool   // exit 2 or decision "block": refuse the tool / prompt, or continue the turn (Stop)
	Approve      bool   // decision "approve": skip the permission prompt (PreToolUse)
	Ask          bool   // permissionDecision "ask": always confirm with the user (PreToolUse); beats Approve
	Reason       string // fed to the model when blocking (stderr on exit 2)
	Context      string // additionalContext / stdout to add for the model
	UpdatedInput string // PreToolUse: replacement tool args as raw JSON ("" = unchanged)
	Warnings     []string
	Ran          []string // commands that ran (for a dim UI line)
}

// pfHookEntry is one matcher group with its origin.
type pfHookEntry struct {
	HookMatcher
	re      *regexp.Regexp // nil = match every tool
	source  string
	project bool // from the project's settings: re-checked against the trust store before each run (F3)
}

// HookSet holds the hooks from config.json, .jazz/settings.json and
// .claude/settings.json (project files only when the project is trusted).
type HookSet struct {
	byEvent map[string][]pfHookEntry
	root    string
	mu      sync.Mutex // hooks run one at a time
}

const pfHookTimeout = 60 * time.Second

// LoadHooks merges the hook sources. trusted=false skips project-level files.
func LoadHooks(cfg *Config, root string, trusted bool) *HookSet {
	h := &HookSet{byEvent: map[string][]pfHookEntry{}, root: root}
	add := func(m map[string][]HookMatcher, src string, project bool) {
		for ev, list := range m {
			for _, hm := range list {
				e := pfHookEntry{HookMatcher: hm, source: src, project: project}
				if mt := strings.TrimSpace(hm.Matcher); mt != "" && mt != "*" {
					re, err := regexp.Compile("^(?:" + mt + ")$")
					if err != nil {
						fmt.Fprintf(os.Stderr, "jazz: hook %s em %s: matcher inválido %q: %v\n", ev, src, mt, err)
						continue
					}
					e.re = re
				}
				h.byEvent[ev] = append(h.byEvent[ev], e)
			}
		}
	}
	if cfg != nil {
		add(cfg.Hooks, "config", false)
	}
	for _, f := range pfGlobalSettingsFiles() {
		if s, ok := pfReadSettings(f); ok {
			add(s.Hooks, f, false)
		}
	}
	if trusted && root != "" {
		for _, f := range pfProjectSettingsFiles(root) {
			if s, ok := pfReadSettings(f); ok {
				add(s.Hooks, f, true)
			}
		}
	}
	return h
}

// Has reports whether any hook is registered for event.
func (h *HookSet) Has(event string) bool {
	return h != nil && len(h.byEvent[event]) > 0
}

func (e pfHookEntry) matches(event, tool string) bool {
	if e.re == nil {
		return true
	}
	switch event {
	case HookPreToolUse, HookPostToolUse:
		return e.re.MatchString(pfCCName(tool)) || e.re.MatchString(tool)
	}
	return true // matchers only filter tool events
}

// pfHookOut is the JSON a hook may print on stdout (Claude Code + crush shapes).
type pfHookOut struct {
	Decision      string          `json:"decision"` // block | approve
	Reason        string          `json:"reason"`
	Continue      *bool           `json:"continue"`
	StopReason    string          `json:"stopReason"`
	SystemMessage string          `json:"systemMessage"`
	Context       string          `json:"context"`
	UpdatedInput  json.RawMessage `json:"updated_input"`
	HSO           struct {
		PermissionDecision       string          `json:"permissionDecision"` // allow | deny | ask
		PermissionDecisionReason string          `json:"permissionDecisionReason"`
		AdditionalContext        string          `json:"additionalContext"`
		UpdatedInput             json.RawMessage `json:"updatedInput"`
	} `json:"hookSpecificOutput"`
	UpdatedInput2 json.RawMessage `json:"updatedInput"`
}

// Run executes the matching hooks sequentially. tool is the jazz tool name
// ("" for non-tool events); payload carries session_id, tool_input,
// tool_response, prompt as applicable (Run adds cwd, hook_event_name, tool_name).
func (h *HookSet) Run(ctx context.Context, event, tool string, payload map[string]any) HookResult {
	var res HookResult
	if !h.Has(event) {
		return res
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	in := map[string]any{}
	for k, v := range payload {
		in[k] = v
	}
	in["cwd"] = h.root
	in["hook_event_name"] = event
	if tool != "" {
		in["tool_name"] = pfCCName(CanonicalTool(tool))
	}
	var ctxParts []string
	projectOK := -1 // unknown until a project hook matches
	for _, e := range h.byEvent[event] {
		if !e.matches(event, CanonicalTool(tool)) {
			continue
		}
		if e.project {
			// the hook or a script it runs may have changed since the user
			// trusted it (git pull, or the model editing it): re-check
			if projectOK < 0 {
				projectOK = 0
				if pfProjectPolicyTrusted(h.root) {
					projectOK = 1
				} else {
					res.Warnings = append(res.Warnings, "hooks do projeto mudaram desde que você confiou neles: ignorados (rode jazz trust)")
				}
			}
			if projectOK == 0 {
				continue
			}
		}
		for _, hc := range e.Hooks {
			if hc.Type != "" && hc.Type != "command" || strings.TrimSpace(hc.Command) == "" {
				continue
			}
			body, _ := json.Marshal(in)
			code, stdout, stderr, err := h.exec(ctx, hc, body)
			res.Ran = append(res.Ran, hc.Command)
			dbg("hook", map[string]any{"event": event, "tool": tool, "command": hc.Command, "exit": code, "stdout": pfClip(stdout, 2000), "stderr": pfClip(stderr, 2000), "err": pfErrString(err)})
			if event == HookPreToolUse && ctx.Err() != nil {
				// a hook killed by Esc must not fail open: the tool does not run
				res.Block, res.Reason = true, "cancelled"
				res.Context = strings.Join(ctxParts, "\n")
				return res
			}
			if err != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("hook %q: %v", oneLine(hc.Command, 60), err))
				continue
			}
			switch code {
			case 0:
			case 2:
				res.Block = true
				res.Reason = strings.TrimSpace(stderr)
				if res.Reason == "" {
					res.Reason = "blocked by hook: " + oneLine(hc.Command, 80)
				}
				res.Context = strings.Join(ctxParts, "\n")
				return res
			default:
				res.Warnings = append(res.Warnings, fmt.Sprintf("hook %q saiu com %d: %s", oneLine(hc.Command, 60), code, oneLine(stderr, 200)))
				continue
			}
			out := strings.TrimSpace(stdout)
			if out == "" {
				continue
			}
			var j pfHookOut
			if !strings.HasPrefix(out, "{") || json.Unmarshal([]byte(out), &j) != nil {
				// plain stdout is context for prompt/session events (Claude Code behaviour)
				if event == HookUserPromptSubmit || event == HookSessionStart {
					ctxParts = append(ctxParts, out)
				}
				continue
			}
			if j.SystemMessage != "" {
				res.Warnings = append(res.Warnings, j.SystemMessage)
			}
			for _, c := range []string{j.HSO.AdditionalContext, j.Context} {
				if strings.TrimSpace(c) != "" {
					ctxParts = append(ctxParts, strings.TrimSpace(c))
				}
			}
			for _, u := range []json.RawMessage{j.HSO.UpdatedInput, j.UpdatedInput2, j.UpdatedInput} {
				if len(u) > 0 && string(u) != "null" {
					res.UpdatedInput = string(u)
					var m map[string]any
					if json.Unmarshal(u, &m) == nil {
						in["tool_input"] = m
					}
					break
				}
			}
			block := j.Decision == "block" || j.HSO.PermissionDecision == "deny" || (j.Continue != nil && !*j.Continue)
			if block {
				res.Block = true
				res.Reason = pfFirstNonEmpty(j.Reason, j.HSO.PermissionDecisionReason, j.StopReason, "blocked by hook: "+oneLine(hc.Command, 80))
				res.Context = strings.Join(ctxParts, "\n")
				return res
			}
			if j.HSO.PermissionDecision == "ask" && !res.Ask {
				// deny > ask > allow: the first "ask" replaces any earlier allow reason
				res.Ask = true
				res.Reason = pfFirstNonEmpty(j.HSO.PermissionDecisionReason, j.Reason)
			} else if j.Decision == "approve" || j.HSO.PermissionDecision == "allow" {
				res.Approve = true
				if res.Reason == "" {
					res.Reason = pfFirstNonEmpty(j.Reason, j.HSO.PermissionDecisionReason)
				}
			}
		}
	}
	if res.Ask {
		res.Approve = false
	}
	res.Context = strings.Join(ctxParts, "\n")
	return res
}

// exec runs one hook command with the payload on stdin.
func (h *HookSet) exec(ctx context.Context, hc HookCmd, body []byte) (code int, stdout, stderr string, err error) {
	to := pfHookTimeout
	if hc.Timeout > 0 {
		to = time.Duration(hc.Timeout) * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	argv := detectShell().oneShotArgv(hc.Command) // bash -c (or sh -c); PowerShell on Windows without bash
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = h.root
	cmd.Env = append(scrubEnv(os.Environ()), "JAZZ_PROJECT_DIR="+h.root, "CLAUDE_PROJECT_DIR="+h.root, "JAZZ_CLI=1")
	cmd.Stdin = bytes.NewReader(body)
	var ob, eb pfCapBuf
	ob.max, eb.max = 64<<10, 16<<10
	cmd.Stdout, cmd.Stderr = &ob, &eb
	procPrepare(cmd, false)
	sbWrapCmd(cmd)
	if err := cmd.Start(); err != nil {
		return -1, "", "", err
	}
	procAttach(cmd)
	defer procForget(cmd.Process.Pid)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-cctx.Done():
		procKillTree(cmd.Process.Pid)
		<-done
		if ctx.Err() != nil {
			return -1, ob.String(), eb.String(), ctx.Err()
		}
		return -1, ob.String(), eb.String(), fmt.Errorf("tempo esgotado (%s)", to)
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), ob.String(), eb.String(), nil
	}
	if err != nil {
		return -1, ob.String(), eb.String(), err
	}
	return 0, ob.String(), eb.String(), nil
}

// pfCapBuf is a bytes.Buffer that silently drops writes past max.
type pfCapBuf struct {
	bytes.Buffer
	max int
}

func (b *pfCapBuf) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); room > 0 {
		if len(p) > room {
			b.Buffer.Write(p[:room])
		} else {
			b.Buffer.Write(p)
		}
	}
	return len(p), nil
}

func pfClip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func pfErrString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func pfFirstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}
