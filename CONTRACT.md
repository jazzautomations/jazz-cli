# jazz-cli — implementation contract (v1)

Four implementers work **in parallel**, each in its own git worktree, on one Go
package (`package main`, module `jazzcli`). This file is the only coordination
point. If you code against exactly what is written here, the four branches merge
without conflicts and still compile.

Baseline: the commit that added this file. At that commit `go build`, `go vet ./...`,
`go test ./...` and the `linux/386` cross-build all pass. Every feature that is not
done yet is a stub marked `TODO(<area>, <finding id>)`.

Read `DESIGN.md` (decisions and things we reject) and `FINDINGS.md` (bugs 1–71,
gaps G1–G24) before writing code.

---

## 0. Ground rules (all areas)

- Toolchain: Go 1.27 (`go`, `gofmt`; on the original dev Mac they lived in `/opt/homebrew/bin`, off PATH).
  Before each commit, run `gofmt -l .` (it must print nothing), then `go vet ./...` and `go test ./...`,
  then `CGO_ENABLED=0 GOOS=linux GOARCH=386 go build -o /tmp/jazz-386 .`, and for the Windows port
  `GOOS=windows GOARCH=amd64 go vet ./...` plus `GOOS=windows GOARCH=arm64 go build -o /dev/null .`.
  OS-specific code goes behind `//go:build windows` / `!windows` (see DESIGN.md, "Porte para Windows").
- No new third-party dependencies (only `golang.org/x/{sys,text}` are allowed). The binary must stay around 10–12 MB.
- Strings the user sees are in **Brazilian Portuguese**. Prompts to the model and code comments are in **English**.
- **Prompt cache:** within a session the system prompt and the tool specs must stay byte-identical.
  Spec order is: `Toolbox.Specs()` (read, write, edit, bash, todo, [fetch]), then `task` (only for the
  parent agent), then `MCPManager.Specs()`. New MCP tools are only appended at the end, never reordered.
  Anything that changes per turn goes into a message (`<system-reminder>`), never into `System`.
- **Frozen files** (nobody edits them on a feature branch): `contract.go`, `CONTRACT.md`.
  If you need a contract change, write it under "Contract change requests" in your final report and
  do not merge it yourself.
- **Edit only files you own** (§1). The only exceptions are the call-site edits listed in §1.2.
- **Package-level names:** you may add unexported identifiers only in files you own. A generic helper
  name (`clamp`, `trunc`, `parseX`, `cache`, …) must carry your area's prefix so that two branches
  cannot declare the same name: agent-core `ac`, ui `ui`, providers `pv`, platform `pf`
  (for example `pvJitter`, `pfMatchGlob`). Exported names listed in this contract are exempt.
  Before you add a name, grep for it.
- **Do not add methods to a type another area owns**, except where §2–§8 say so.
  Ownership: `Agent`/`Session`/`Message` → A; `TermUI`/`Editor`/`HeadlessUI`/`JSONUI` → B;
  `Config`/providers → C; `Toolbox`/`Shell`/`MCP*`/`PermSet`/`HookSet` → D.
- **Tests:** edit the `*_test.go` files of your area and add new ones named `<file>_<topic>_test.go`.
  `mcp_ui_test.go` is shared, and each test function in it belongs to the area of the code it tests:
  MCP, flags, friendlyErr and customCommands → D; theme, markdown, clip and commonPrefix → B;
  instructions, skills and hardwareNote → A. Prefix new test helpers with your area prefix.
  A test that asserts behaviour FINDINGS.md calls a bug is skipped with `t.Skip("pending: <id>")`.
  When you fix that item, un-skip the test and fix its expectation.
- Commit messages are in Portuguese and end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Target machine: a low-end linux/386 laptop (Dell Latitude class, Pentium M, 2 GB RAM, antiX, bash 5, `fdfind` but no `fd`).
  It uses key auth only: never use passwords or sshpass, and never print secrets.

## 1. File ownership

| Area | Owner id | Files (existing + NEW) | Findings (bugs) | Gaps |
|---|---|---|---|---|
| A | **agent-core** | agent.go, session.go, types.go, prompt.go, NEW subagent.go, NEW textcalls.go | 3, 25, 26, 27, 31, 36, 37, 61, 69 (+15 agent side) | G1, G7, G12 (agent side), G13 (agent side), G15, G21 (/context data), G23; call sites of G4, G9, G10, G11 |
| B | **ui** | ui.go, term.go, editor.go, headless.go, NEW jsonui.go (stub exists) | 4, 10, 11, 13, 32, 38, 39, 41, 53, 54, 55, 56, 57, 70 | G5 (JSONUI), G11 (approve `[e]`), G12 (Shift+Tab, status colour), G13 (Prefill), G17, G21 (Bell, $ in TurnEnd) |
| C | **providers** | httpx.go, provider_openai.go, provider_anthropic.go, provider_codex.go, NEW provider_gemini.go, oauth.go, config.go, models.go, NEW omni.go (stub), NEW ratelimit.go (stub) | 16, 17, 18, 19, 34, 35, 42 (Limits side), 58, 60 (SSE), 65, 66 | G1 (Stop mapping), G2, G3 (presets/probe), G4, G9 (serialization), G15 (TextTools, 400 detection), G21 (cost), G22, omni |
| D | **platform** | main.go, mcp.go, safety.go, tools.go, shell.go, web.go, edit.go, truncate.go, NEW hooks.go (stub), NEW perms.go (stub), NEW debug.go (stub), NEW update.go, NEW trust.go (stub), scripts/*, DESIGN.md | 1, 2, 5–9, 12, 14, 15, 20–24, 28–30, 33, 40, 42 (main side), 43–52, 59, 60 (MCP), 62–64, 67, 68, 71 | G3 (`jazz provider add` CLI), G5 (flags/exit codes), G6, G8, G9 (read/MCP image), G10, G11 (rules), G12/G13/G21 slash commands, G14, G16, G18, G19, G20, G24 |

Shared, frozen: `contract.go`, `CONTRACT.md`. Nobody else edits `go.mod` or `.gitignore`.

### 1.1 Who calls whom (seams)

- A calls B only through the `UI` interface.
- A calls C through: `Provider.Chat`, `Config.NewProvider`, `Config.Limits`, `Config.ModelSettings`,
  `Config.Candidates`, `Config.Price`, `Config.SupportsImages`, `Config.MarkTextTools`,
  `RetryDelay`, `MarkExhausted`, `ExhaustedUntil`, `Config.EffMode`.
- A calls D through: the `Toolbox` methods, `parseArgs`/`normalizeArgs`/`argStr`/`argInt`/`argBool`/`CanonicalTool`,
  `DangerReason`, `IsReadOnly`, `protectedPath`, `PermSet`, `HookSet`, `dbg`, `MCPManager`.
- D (main.go) calls A only through the exported `Agent` API (§3), never through its private fields.
- D (main.go) calls B through: `NewTermUI`, `TermUI.{print, ShellOutput, WatchTurn, StatusLine, Ask, SetMode, Jobs, T, Notice, Warn, Error, ToolEnd, Thinking, StopThinking}`,
  `NewEditor`, `Editor.{ReadLine, Prefill, CycleMode, complete}`, `NewJSONUI`, `HeadlessUI`.
- C calls D: only `expandEnv` (mcp.go), `dbg`, `redactSecrets` and `writeFileAtomic` (config.go, C's own).

### 1.2 Call-site edits allowed outside your files

You may not edit another area's files. If your feature needs a new call site there, the owner adds it
from this table. Every area builds against the stubs, so each side can land on its own.

| Call site | Owner who writes it | Calls |
|---|---|---|
| httpx.go `postStream`/`readSSE` → `dbg("request"/"status"/"sse", …)` | C | D's `dbg` |
| agent.go → `dbg("repair"/"nudge"/"compact", …)` | A | D's `dbg` |
| mcp.go → `dbg("mcp", …)` | D | D's `dbg` |
| main.go → `LoadPerms`, `LoadHooks`, `ag.Perms = …`, `ag.Hooks = …` | D | D's own + A's fields |
| main.go → `NewJSONUI` / `HeadlessUI` for `-p` | D | B |
| main.go → `/rewind`, `/fork`, `/export`, `/context`, `/permissions`, `/mode plan` | D | A + B APIs |

---

## 2. Shared types (contract.go — FROZEN)

The exact source is in `contract.go`. Summary:

```go
type UI interface {
	StreamText(s string)
	StreamReasoning(s string)
	Thinking(label string)
	StopThinking()
	ToolStart(id, name, detail string)                         // id = ToolCall.ID ("" for synthetic)
	ToolEnd(id, name string, out ToolOutput, d time.Duration)
	Notice(msg string)
	Warn(msg string)
	Error(msg string)
	Approve(ctx context.Context, tool, detail, reason string) (Approval, string) // string = deny feedback
	TurnEnd(st TurnStats)
	SetMode(mode string) // auto|smart|ask|yolo|plan (effective mode)
	Jobs(n int)          // running background jobs
	Bell()               // long turn finished
}

type Approval int
const ( Deny Approval = iota; Allow; AllowAlways )

type TurnStats struct {
	Dur   time.Duration
	Tools int
	Usage Usage
	Model string
	Ctx, Win int
	Cost  float64 // USD this turn; 0 = unknown/free
	Stop  string  // string(StopReason)
	Steps int     // model requests this turn
}

type StopReason string
const (
	StopDone = "done"; StopStepLimit = "step_limit"; StopLoop = "loop"; StopDenied = "denied"
	StopTruncated = "truncated"; StopInterrupted = "interrupted"; StopError = "error"
	StopUnverified = "unverified"; StopTimeout = "timeout"; StopUntilFailed = "until_failed"
	StopQualityFailed = "quality_failed"
)
func (r StopReason) ExitCode() int // done 0, error 1, (bad usage 2: main), step_limit 3, loop 4, denied 5, truncated 6, unverified 7, until_failed 8, quality_failed 9, timeout 124, interrupted 130

type RunInfo   struct { SessionID, Model, Cwd string; Tools []string }
type RunResult struct { Status StopReason; Text, Err string; Usage Usage; Cost float64; Turns, Tools int; Duration time.Duration; SessionID, Model, Recover string }
type HeadlessSink interface { UI; Begin(info RunInfo); Finish(res RunResult) }
type TurnRef     struct { Index int; Text string }
type ContextPart struct { Label string; Tokens int }
```

Semantics every UI must honour:
- **Approve** returns `(Deny, "")` promptly when `ctx` is cancelled or stdin hits EOF (bug 41).
  Keys typed before the prompt was drawn never count as an answer (bugs 4, 11, 13). The detail is
  shown with `visibleDetail` and never clipped, so the user always sees the whole command (bug 10).
  The returned string is non-empty only when the user chose `[e] não, e dizer o que fazer` (G11).
  When `reason != ""` (dangerous), `AllowAlways` is never offered.
- **ToolStart/ToolEnd** may be called for several calls before any of them ends (parallel read-only
  calls, G23), so a UI must match them by `id`. They must be safe for concurrent calls.
- **Finish** fills `Turns` and `Tools` from the `TurnStats` it received when the result has zeros.

## 3. Agent API (agent-core owns; main.go and subagent use only these)

```go
func NewAgent(cfg *Config, ui UI, tools *Toolbox, mcp *MCPManager, model string) *Agent

type Agent struct {
	Model     string   // primary "provider/model"
	System    string   // built once by EnsureSystem; byte-stable per session
	Msgs      []Message
	Usage     Usage    // session totals (incl. Cost)
	SessionID string
	Perms     *PermSet // set by main before the first Run; nil = no rules
	Hooks     *HookSet // set by main before the first Run; nil = no hooks
	// everything else is private to agent-core
}

func (a *Agent) Run(ctx context.Context, input string) (StopReason, error)
func (a *Agent) EnsureSystem()                 // builds System and opens the session; call before reading SessionID
func (a *Agent) Steer(s string)                // text typed during a turn (thread-safe)
func (a *Agent) Note(s string)                 // context prepended to the next user message
func (a *Agent) Attach(imgs ...ImagePart)      // images for the next user message (@file.png, paste)
func (a *Agent) Compact(ctx context.Context, focus string) error
func (a *Agent) TurnModel() string             // model of current/last turn (sticky fallback), else Model
func (a *Agent) Window() int                   // context window of TurnModel (cfg.Limits)
func (a *Agent) TokenEstimate() int
func (a *Agent) SetModel(ref string)           // switches Model and logs it in the session
func (a *Agent) LoadSession(si SessionInfo) error
func (a *Agent) Close()                        // closes the session file (nil-safe)
func (a *Agent) UserTurns() []TurnRef          // real user messages, oldest first
func (a *Agent) RewindTo(n int) error          // Msgs = Msgs[:n], tools.RewindTo(n), session entry {t:"rewind",n}
func (a *Agent) Fork(n int) (newID string, err error) // new session with Msgs[:n], meta.parent = old id
func (a *Agent) Export(format string) (string, error) // "md" | "html" → document text; caller writes the file
func (a *Agent) ContextBreakdown() []ContextPart      // /context rows (system parts, tools, MCP per server, messages, top tool results)
```

The session helpers stay as they are: `ListSessions(root) []SessionInfo`,
`LoadSession(path) (*Session, []Message, model, system string, err error)` and `NewSession(...)`.
New `sessEntry.T` values: `rewind` (`N int` field), and `meta.parent` for a fork. Images are stored
in `dataDir()/blobs/<sha>.<ext>`, and only references go into the JSONL.

**What Run must do (agent-core implements it; the others rely on it):**
1. It calls `ui.SetMode(cfg.EffMode())` at the start.
2. It runs the `UserPromptSubmit` hook. Block → return `(StopDenied, nil)` with a Warn. Context → prepend it.
3. In plan mode it prepends the plan `<system-reminder>` to the user message (never to System).
4. The user message carries `Images` from `Attach`. When `!cfg.SupportsImages(model)`, each image is replaced by a text note.
5. Each step uses `cfg.Limits(turnModel)` for `MaxTokens` and the compaction threshold; the threshold is the minimum across `cfg.Candidates(...)`.
   With `ModelSettings(ref).Tools == "text"` it sets `TextTools` and sends a rewritten history (textcalls.go).
   It sends `Temperature` and `Effort` (from ModelSettings, falling back to cfg.Effort).
6. **Truncation** (`resp.Truncated()`): tool calls are not executed and each gets
   `TOOL_NOT_EXECUTED: …` (G1). Text-only answers are continued at most twice, and after that the turn ends with `StopTruncated`.
7. Tool permission order, for every call:
   1. `Hooks.Run(PreToolUse)`: Block → error result with the Reason. `UpdatedInput` replaces the args. Approve → skip the prompt (Deny rules still apply).
      Ask (`permissionDecision: "ask"`) → always prompt, with the hook's reason and no `[t]`. A cancelled ctx after the hook
      (or after the prompt) → `TOOL_NOT_STARTED`, the tool never runs.
   2. `Perms.Check(tool, args)`: `PermDeny` → refuse in **every** mode, including yolo ("blocked by rule X").
   3. `yolo` → allow.
   4. `plan` → refuse write/edit ("Plan mode: …"); allow bash only when `IsReadOnly`; MCP and `fetch` with a url only if allowed by a rule (search is fine).
      The research subagent may call an MCP tool only when it is annotated `readOnlyHint` or allowed by a rule.
      A `task` item with `files` is a worker subagent: write/edit and bash go through these same rules (not read-only);
      in plan mode every item is research. Parallel `task` calls are gated one by one, in order, before any of them runs.
   5. Prompt when any of these holds: `DangerReason(cmd) != ""`, write/edit to a `protectedPath(abs, root)` (bug 3),
      read of a `secretPath(abs)`, a hook "ask", `PermAsk`, or mode `ask` with a non-read-only call (fetch included).
      `PermAllow` skips the prompt but **never** skips DangerReason.
   6. `AllowAlways` → `Perms.Remember(tool, args)`, then show the derived rule with `ui.Notice`.
      Deny with feedback → the tool result is `The user denied this and said: <text>`.
8. After each bash call it calls `ui.Jobs(tools.RunningJobs())`. It runs `PostToolUse` hooks (Context appended to out.Text).
   An `out.Image` is passed on as a user message with `Images` (OpenAI tool messages cannot carry images).
9. At the end it calls `ui.TurnEnd(stats)`, then `ui.Bell()` if the turn took more than 20 s. `Stop` hooks can continue the turn (max 3).
10. It returns a reason: `StopDone`, `StopStepLimit` (the limit was hit; tool calls after the limit are never executed, bug 25), `StopLoop`,
    `StopDenied` (the last tool batch had a denial and the model then stopped), `StopTruncated`, `StopInterrupted` (with ctx.Err()),
    or `StopError` (with err).
11. When `NoToolUse` is set at the step limit, tools are still offered so the history stays valid (bug 37).

## 4. UI (ui owns)

Besides the `UI` interface, main.go relies on these concrete APIs. Their signatures are frozen:

```go
func NewTermUI(cfg *Config, keys *KeyReader) *TermUI
func (u *TermUI) print(s string)
func (u *TermUI) ShellOutput(chunk []byte)
func (u *TermUI) WatchTurn(cancel func(), steer func(string)) func()  // typing is visible while the agent runs (G17)
func (u *TermUI) StatusLine(model string, ctxTok, win, mcpReady, mcpTotal int) string // uses SetMode/Jobs state; plan in its own colour
func (u *TermUI) Ask(question, keys string) rune // lower-case key pressed; 0 on Esc/Ctrl+C/EOF/no TTY
// field T Theme stays exported

func NewEditor(ui *TermUI) *Editor
func (e *Editor) ReadLine(prompt string, promptW int) (string, error) // ErrInterrupt / ErrEOF
func (e *Editor) Prefill(s string)                                      // next ReadLine starts with s (/rewind)
// fields: complete func(prefix string) []string (set by main); CycleMode func() (Shift+Tab / Ctrl+T, set by main)
// G17 @path expansion: the editor returns the raw line; expansion helper below
func ExpandMentions(t *Toolbox, line string) (text string, images []ImagePart) // stub in editor.go (ui); main calls it, then ag.Attach(images...)

type HeadlessUI struct { Verbose bool; Last string /* … */ } // implements HeadlessSink (text)
func NewJSONUI(w io.Writer, stream bool) *JSONUI                  // implements HeadlessSink (json / stream-json)
```

`ExpandMentions` is a stub today (it returns the line unchanged), so main can call it in `App.send` right away: `text, imgs := ExpandMentions(a.tools, line); a.agent.Attach(imgs...)`.

stream-json event shapes (G5), one JSON object per line:
`{type:"init",session_id,model,cwd,tools}`, `{type:"text",delta}`, `{type:"tool_start",id,name,args}`,
`{type:"tool_end",id,name,is_error,summary,output(≤4 KB)}`, `{type:"notice"|"warn"|"error",text}`,
`{type:"denied",name,args,reason}`, and `{type:"result",status,text,error,usage,cost,turns,tools,duration_ms,session_id,model,recover?}` (`recover` only when `--recover` handed a turn off: `loop`, `until_repeat`, `tool_errors`).
With `json`, only the final `result` object is printed.

`Bell()` writes BEL and/or OSC 777/9 according to `cfg.Notify` (`bell` (default) | `osc` | `off`), and only when stdout is a TTY.

## 5. Config and providers (providers owns)

Types in config.go. The shapes are frozen; C may add fields:

```go
type ProviderConf struct {
	Type         string            `json:"type"`                // openai | anthropic | codex | gemini
	BaseURL      string            `json:"base_url,omitempty"`
	KeyEnv       string            `json:"key_env,omitempty"`
	DefaultModel string            `json:"default_model,omitempty"`
	NoKey        bool              `json:"no_key,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`   // expandEnv'd, merged into requests
	Ollama       bool              `json:"ollama,omitempty"`    // send options.num_ctx
	Weight       int               `json:"weight,omitempty"`    // omni weight (0 = 1)
}
type ModelConf struct {
	Context     int      `json:"context,omitempty"`
	MaxTokens   int      `json:"max_tokens,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	Tools       string   `json:"tools,omitempty"`  // native | text | none
	Effort      string   `json:"effort,omitempty"`
}
type Permissions struct { Allow, Deny, Ask []string } // json allow/deny/ask
type HookMatcher struct { Matcher string `json:"matcher,omitempty"`; Hooks []HookCmd `json:"hooks"` }
type HookCmd     struct { Type string `json:"type"`; Command string `json:"command"`; Timeout int `json:"timeout,omitempty"` }
type OmniConf    struct { Enabled bool; Strategy string /* free-first|weighted|ordered */; Exclude []string }

type Config struct {
	Model, Mode string; ModeOverride string `json:"-"`; Fallback []string; MaxSteps, ContextWindow, MaxTokens int
	Effort string; ShowThinking, NoVerifyNudge bool; Providers map[string]*ProviderConf
	Models map[string]*ModelConf `json:"models"`; Permissions Permissions `json:"permissions"`
	Hooks map[string][]HookMatcher `json:"hooks"`; Web bool `json:"web"` /* default true */
	Omni OmniConf `json:"omni"`; Notify string `json:"notify"`; UpdateURL string `json:"update_url"`
}
```

**Mode rules (fixes bug 9).** `--yolo`/`--ask`, Shift+Tab and plan mode set `ModeOverride` only, which is never saved.
`/mode X` sets `Mode = X`, clears `ModeOverride` and saves. `plan` is never written to `Mode`: LoadConfig turns it into `auto`.
Everybody reads `cfg.EffMode()`, never `cfg.Mode`, except the `/mode` setter.

Functions and methods C provides. Stubs exist, so callers compile today:

```go
func (c *Config) EffMode() string
func (c *Config) Limits(ref string) (win, maxOut int)         // Models entry → models cache → globals; clamp maxOut ≤ win/4 when win ≤ 32k
func (c *Config) ModelSettings(ref string) ModelConf         // merged user + cache; zero value = defaults
func (c *Config) Candidates(refs ...string) []string         // omni.go: refs, Fallback, implicit free chain/omni; skips exhausted
func (c *Config) NewProvider(name string) (Provider, error)  // + "gemini"
func RetryDelay(err error, attempt int) time.Duration         // ratelimit.go; 0 = move to the next candidate
func MarkExhausted(ref string, reset time.Time)               // dataDir()/exhausted.json
func ExhaustedUntil(ref string) (time.Time, bool)
// stubs already in models.go (safe to call now; C replaces the bodies):
func (c *Config) Price(ref string) (inPerTok, outPerTok float64, ok bool)
func (c *Config) SupportsImages(ref string) bool             // unknown → true
func (c *Config) MarkTextTools(ref string)                   // after a 400 "tools not supported"; persisted in models cache
func DetectWindow(ctx context.Context, cfg *Config, ref string) (int, error) // writes dataDir()/models-cache.json
func ProbeProvider(ctx context.Context, cfg *Config, name string) (reachable bool, win int, err error) // doctor, HasCreds for NoKey locals
func (c *Config) AddProvider(ctx context.Context, name string, pc ProviderConf) ([]ModelInfo, error)  // `jazz provider add`
```

**Provider obligations (`types.go` is owned by A, but these field meanings are frozen):**
- `ChatRequest.Temperature` (nil = omit), `Effort` (Anthropic: thinking budget, G22).
- `ChatRequest.NoToolUse`: still send `tools`, with `tool_choice: none` (OpenAI/Codex) / `{type:"none"}` (Anthropic).
- `ChatRequest.TextTools`: never send `tools`/`tool_choice`. agent-core has already rewritten the history and System.
- `Message.Images`: OpenAI `image_url` data URL parts, Anthropic `image` base64 blocks, Codex `input_image`, Gemini `inline_data`.
- `ChatResponse.Stop`: on truncation it must be one of `length` | `max_tokens` | `max_output_tokens` | `incomplete`
  (Codex: response.incomplete → `incomplete`; Anthropic: `message_delta.stop_reason`). `Truncated()` relies on this.
- `Usage.Cost`: OpenRouter `usage.cost` (send `usage:{include:true}`). Otherwise 0, and agent-core prices it via `Price`.
- `APIError.RetryAfter` / `APIError.Daily`: filled in `postStream` for non-200 responses (G4).
- DeepSeek `reasoning_content` is echoed back (bugs 16, 18). Parallel tool calls with the same index stay separate (bug 19).

## 6. Toolbox, shell, safety (platform owns)

All of these already exist. The signatures are frozen:

```go
func NewToolbox(root string) *Toolbox                 // fields: Root, Shell, Web (fetch tool), OnShellOutput
func (t *Toolbox) Specs() []ToolSpec                  // computed once; byte-stable; replaces the old global toolSpecs
func (t *Toolbox) Exec(ctx context.Context, name, rawArgs string) ToolOutput // refuses repaired (truncated) args for write/edit/bash
func (t *Toolbox) BeginTurn(msgIndex int)             // msgIndex = len(Agent.Msgs) before the user message
func (t *Toolbox) Undo() ([]string, error)
func (t *Toolbox) RewindTo(msgIndex int) ([]string, error)
func (t *Toolbox) MarkRead(p string, data []byte)     // stamp a file as read (used by @mentions)
func (t *Toolbox) MarkInstrSeen(dirs []string)        // dirs whose AGENTS.md is already in System
func (t *Toolbox) TodoList() []Todo                   // copy, thread-safe (do not touch t.Todos/t.mu from outside)
func (t *Toolbox) Reset()                             // /clear: todos, read stamps, checkpoints, seen instructions
func (t *Toolbox) KillJobs() int
func (t *Toolbox) RunningJobs() int

type ToolOutput struct { Text string; IsErr bool; Image *ImagePart; Display Display }

func parseArgs(raw string) (m map[string]any, repaired bool, err error) // repaired = truncated JSON closed heuristically
func normalizeArgs(tool string, m map[string]any) map[string]any
func argStr(m map[string]any, k string) (string, bool); func argInt(…) (int, bool); func argBool(…) bool
func CanonicalTool(name string) string                 // aliases include "task" (spec appended by agent-core)

func DangerReason(cmd string) string    // "" = safe; Portuguese reason otherwise
func IsReadOnly(cmd string) bool         // ask/plan modes and the subagent
func protectedPath(abs, root string) bool // credentials/dotfiles/PATH/project policy: write/edit always ask (except yolo)
func secretPath(abs string) bool          // credentials: read always asks (except yolo)
func sanitizeTerm(s string) string; func visibleDetail(s string) string // UI must use these (bugs 10, 32)
func redactSecrets(s string) string; func scrubEnv(env []string) []string
func truncateHeadTail(s string, max int) string; const maxToolBytes
```

The `task` tool (G7) is owned by agent-core. Its spec (`taskSpec`, about two lines) is appended after `Toolbox.Specs()`,
and its execution is caught in `Agent.execTool` before `Toolbox.Exec`. The subagent shares the parent's
`*Toolbox` and `*MCPManager`. It denies write/edit and allows bash only when it passes `IsReadOnly`.

## 7. MCP (platform owns)

```go
type MCPServerConf struct { …; EnabledTools, DisabledTools []string /* globs, json enabled_tools/disabled_tools */ }
func StartMCP(root string) *MCPManager                 // project .mcp.json only when IsTrusted(root,"mcp",content) (bug 2)
func (m *MCPManager) Specs() []ToolSpec                // stable order; filtered; unique names (bug 28)
func (m *MCPManager) Has(tool string) bool
func (m *MCPManager) Call(ctx context.Context, tool, rawArgs string) ToolOutput // image content → ToolOutput.Image
func (m *MCPManager) Close(); Wait(timeout time.Duration); Done() bool
func (s *MCPServer) State() (status string, err error, tools int) // thread-safe snapshot; UI/main use ONLY this
```

At the baseline, `State()` reads under `stMu`, but the writers do not take that lock yet. Platform
must move every write of `Status`/`Err`/`Tools` under `stMu` (bugs 29, 43). The exported
`Status`/`Err`/`Tools` fields may become private after that, because main uses only `State()`.

## 8. Hooks, permissions, debug, trust, update (platform owns; stubs exist)

```go
// hooks.go
const HookPreToolUse, HookPostToolUse, HookUserPromptSubmit, HookStop, HookSessionStart = "PreToolUse", …
type HookResult struct { Block, Approve, Ask bool; Reason, Context, UpdatedInput string; Warnings, Ran []string }
func LoadHooks(cfg *Config, root string, trusted bool) *HookSet
func (h *HookSet) Has(event string) bool                                                       // nil-safe
func (h *HookSet) Run(ctx context.Context, event, tool string, payload map[string]any) HookResult // nil-safe
//   payload keys set by agent-core: session_id, tool_input (map), tool_response (string), prompt (string)
//   Run adds: cwd, hook_event_name, tool_name (Claude Code names: Bash/Edit/Write/Read/mcp__*)

// perms.go
type PermDecision int; const ( PermDefault PermDecision = iota; PermAllow; PermDeny; PermAsk )
type PermRule struct { Kind, Rule, Source string }
func LoadPerms(cfg *Config, root string, extraAllow []string) *PermSet // extraAllow = --allow-tools
func (p *PermSet) Check(tool string, args map[string]any) (PermDecision, string) // nil-safe; deny > ask > allow
func (p *PermSet) Remember(tool string, args map[string]any) (string, error)     // nil-safe
func (p *PermSet) Rules() []PermRule; func (p *PermSet) Remove(rule string) error

// debug.go
var debugOn bool
func dbg(kind string, v any) // kinds: request, status, sse, repair, alias, nudge, compact, mcp, hook; no-op when off; concurrent-safe
func safeGo(f func())        // goroutine with crash handler (restore terminal, kill shell/MCP, crash file)

// trust.go
func IsTrusted(root, kind string, content []byte) bool // kind: "mcp" | "hooks" | "steering"; baseline stub returns true → platform makes it false-by-default
func Trust(root, kind string, content []byte) error

// update.go (platform-internal): cmdUpdate, background daily check using cfg.UpdateURL
```

Data files: `models-cache.json` and `exhausted.json` → C; `trusted.json`, `logs/`, `crash-*.txt`, `jobs/`, `update.json` → D;
`sessions/` and `blobs/` → A. All of them live under `dataDir()`.

## 9. Headless (main.go: platform; sinks: ui)

New flags: `--output-format text|json|stream-json`, `--max-steps N`, `--allow-tools bash,edit,…` (passed to
`LoadPerms` extraAllow), `--system-append`, `--debug`, and `-c`/`-r` honoured in `-p`.
Flow: build the sink → `ag.EnsureSystem()` → `sink.Begin(RunInfo{SessionID: ag.SessionID, …})` → `ag.Run` →
`sink.Finish(RunResult{…})` → `os.Exit(res.Status.ExitCode())`. Usage errors exit with 2.

## 10. Merge order and integration

Merge order: C → D → A → B. That order is only a preference: the stubs let any order compile. After each
merge, rerun the four checks from §0. When a stub you relied on gets its real implementation, your tests
may change behaviour; fix your own tests and nothing else.

## 11. Baseline notes (what the contract owner changed to get a green tree)

- Callers were adapted to the new signatures: `parseArgs` returns 3 values, `BeginTurn(len(Msgs))`, `Toolbox.Specs()`
  replaces the global `toolSpecs`, `Run` returns `(StopReason, error)`, and `Approve` takes ctx and returns feedback.
- main.go now goes through the exported Agent API only (`LoadSession`, `SetModel`, `Close`, `TokenEstimate`, `Window`)
  and through `MCPServer.State()`, `Toolbox.TodoList()`/`Reset()` and `TermUI.StatusLine()`. Flags set `ModeOverride`.
- web.go: the HTML drop-block regexp used a backreference (`</\1>`), which RE2 rejects. It panicked at init,
  so the binary could not start at all. It is now one regexp per tag.
- tools.go `closeTruncated`: the dangling-key heuristic dropped the last array element (`["a","b"` → `["a"]`).
  It now applies only inside an object.
- Tests skipped as `pending:` because they assert behaviour FINDINGS.md calls a bug:
  `TestAgentDangerousBashApproval`, `TestAgentAllowAlwaysCachesExceptDangerous`, `TestToolsDangerQuotedStringIsFlagged` (33);
  `TestShellClipLine`, `TestEditReadHugeLineClipped` (48). The skips already marked `BUG:` remain as they were.
- Tests updated to the new code: the shell fake uses `shellInst` and the `marker code_pwd_marker E` protocol;
  the undo cap is 100; `task` is a valid alias target.

## 12. v0.3 contract changes (recorded at merge)

- `SetMode` may now receive `smart` (smart.go): workspace edits run without asking and a fast model
  classifies the rest as SAFE/ASK. `Config.Mode` persists `smart` like `auto`/`ask`/`yolo`.
- New stop reason `StopUntilFailed = "until_failed"` (exit code 8): the `--until` command still fails after
  the last allowed round (agent_loop.go).
- New trust kind `"steering"` (sha256 over every project steering file: `.jazz/steering`, `.kiro/steering`,
  `.cursor/rules`). Untrusted project steering rules are not loaded.
- `Toolbox.Checkpoints` (bool, off by default; main.go turns it on in interactive mode) enables shadow-git
  checkpoints in `dataDir()/checkpoints/`, so `Undo`/`RewindTo` also revert bash changes.
- Budgets and quality (budget.go, quality.go, netwait.go, scores.go): new stop reason
  `StopQualityFailed = "quality_failed"` (exit code 9): the Stop checklist still fails after its one
  feedback round. `LoopOpts` gains `Budget` (`lean|normal|off`) and `Quality` (`off|standard|strict`),
  "" = config then default. `Config` gains `budget`, `quality`, `net_wait` (`on|off`) and
  `fallback_order` (`score|config`). `Agent` gains the private `bud *bgState`. No exported signature
  changed; the hooks in agent.go are one-line calls (bgBeginTurn, bgStepLimit, bgAtLimit, bgPreTool,
  bgAfterTool, bgStepNote, bgEndTurn, bgOrderCands, bgNetWait, bgScoreOK/bgScoreStall) and
  acStopGate calls acQualityGate before --until.

## 13. v0.4 contract changes (recorded at merge)

- Budgets/quality: see the last bullet of §12 (they landed in v0.4, recorded there by their branch).
- Recovery and plan cache (agent_recover.go, plancache.go): `RunResult` gains `Recover string`
  (`loop`, `until_repeat`, `tool_errors`; omitted from the JSON result when empty). `LoopOpts` gains
  `Recover`. Exported `(*acTurnLoop).MarkVerified()` and `Agent.LastRecovery()`. `Config` gains
  `recover`, `plan_cache`, `hindsight`.
- Scripts, fast paths, offline (scripts.go, intents.go, offline.go): the `script` tool spec comes after
  the built-in tools and before `task`, built once per Toolbox (cache). New trust kind `"scripts"`
  (`.jazz/scripts`, `.jazz/intents.json`). New permission rule `Script(name)` (globs allowed). Exported
  `Agent.PermitCommand` and `Toolbox.Scripts`. `Config` gains `scripts`, `fastpaths`.
- Output shaping (outparse.go, outshape.go, prefetch.go): `Toolbox.Window func() int` sizes the bash
  result budget. `Config` gains `parse_output`, `grep_trunc`, `tool_budget`, `prefetch`.
- Skills (skills.go): `Agent.sk` (private), `Config.Skills` (`SkillsConf`, string or object). The
  `<craft>` block in `basePrompt` is part of the frozen system prompt; `JAZZ_CRAFT=off` drops it
  for A/B (read when the prompt is built, once per session).
- Integration: `acRecap` ignores `<system-reminder>` blocks; `pcFailed` counts `quality_failed`;
  script calls feed the budget (`bgScriptKind`); a `--recover` handoff calls `bgOnReset`.
- A/B env added at merge: `JAZZ_SCRIPTS`, `JAZZ_CRAFT`.

## 14. OS sandbox (os-sandbox branch)

- `sandbox.go` (platform owns): `Config` gains `sandbox` (`auto`|`strict`|`off`) and `sandbox_net`
  (`on`|`off`); flag `--sandbox`, env `JAZZ_SANDBOX`, `JAZZ_SANDBOX_NET`. `sbSetup(cfg, root, flag)`
  runs once in `main` before any subcommand; `sbCurrent()` is the active policy (nil = none).
- Every child spawn site calls `sbWrapCmd(cmd)` after `Dir`/`Env` and before `Start`: `Shell.start`,
  `Shell.startDetached`, `HookSet.exec`, `MCPServer.startStdio`, `scRegistry.scExec`, `qgRun`. A new
  spawn site of project-controlled code must do the same.
- `Shell.startDetached(command, log, env)` gained `env` (nil = the shell's env); under bwrap
  (`Sandbox.PidNamespace`) background jobs start through it on Unix too.
- Windows: `procAttach` adds job limits (die on unhandled exception, 512 processes, UI restrictions)
  when the sandbox backend is the job object.
