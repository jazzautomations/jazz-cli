package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

type ProviderConf struct {
	Type         string            `json:"type"` // openai | anthropic | codex | gemini
	BaseURL      string            `json:"base_url,omitempty"`
	KeyEnv       string            `json:"key_env,omitempty"`
	DefaultModel string            `json:"default_model,omitempty"`
	NoKey        bool              `json:"no_key,omitempty"`  // works without a key (local/free endpoints)
	Headers      map[string]string `json:"headers,omitempty"` // extra HTTP headers; values go through expandEnv
	Ollama       bool              `json:"ollama,omitempty"`  // send options.num_ctx (Ollama's OpenAI endpoint)
	Weight       int               `json:"weight,omitempty"`  // omni routing weight (0 = default 1)

	// Omni / preset metadata (all optional; presets fill them, users may override).
	Free       bool     `json:"free,omitempty"`        // recurring free quota (not a one-time trial)
	Sub        bool     `json:"sub,omitempty"`         // subscription usage (ChatGPT login): not metered per request
	Card       bool     `json:"card,omitempty"`        // free credit only after adding a payment card
	Train      bool     `json:"train,omitempty"`       // free tier may train on / log prompts
	Router     string   `json:"router,omitempty"`      // "openrouter" | "kilo": reasoning_details echo, usage.include
	FreeIDs    bool     `json:"free_ids,omitempty"`    // only ids ending in :free / -free / /free are free (routers, gateways)
	Quirks     []string `json:"quirks,omitempty"`      // mistral-ids9, gemini-thought-sig, cohere-effort, deepseek-reasoning, effort
	Limits     *Limits  `json:"limits,omitempty"`      // provider-wide limits (Scope key) or defaults for its models
	Signup     string   `json:"signup,omitempty"`      // where to get a key (shown by the wizard)
	KeyHint    string   `json:"key_hint,omitempty"`    // PT-BR hint, e.g. "começa com gsk_"
	KeyRE      string   `json:"key_re,omitempty"`      // key format check (warning only)
	Probe      string   `json:"probe,omitempty"`       // key probe: "models" (default) | "chat1" | absolute URL ({ACCOUNT} expanded)
	Account    bool     `json:"account,omitempty"`     // BaseURL contains {ACCOUNT}; id from AccountEnv or Cred.AccountID
	AccountEnv string   `json:"account_env,omitempty"` // env var holding the account id
	Local      bool     `json:"local,omitempty"`       // local/LAN server: HasCreds probes reachability (1.5 s, cached)
	Title      string   `json:"title,omitempty"`       // display name for the wizard/status
}

// ModelConf overrides per model, keyed by the full ref "provider/model".
type ModelConf struct {
	Context     int      `json:"context,omitempty"`     // context window in tokens
	MaxTokens   int      `json:"max_tokens,omitempty"`  // output cap
	Temperature *float64 `json:"temperature,omitempty"` // nil = provider default
	Tools       string   `json:"tools,omitempty"`       // native (default) | text | none
	Effort      string   `json:"effort,omitempty"`      // low | medium | high; "" = cfg.Effort

	Q      int      `json:"q,omitempty"`       // omni quality prior 0-100
	Limits *Limits  `json:"limits,omitempty"`  // per-model limits (Scope model)
	MaxReq int      `json:"max_req,omitempty"` // max prompt+max_tokens a single request may carry (Groq TPM)
	MaxOut int      `json:"max_out,omitempty"` // provider's max output tokens: ceiling of the raise after a cutoff (cutoff.go)
	Tiers  []string `json:"tiers,omitempty"`   // omni tiers: fast | smart | coder | free | long
	Images *bool    `json:"images,omitempty"`  // accepts image input (nil = unknown)
}

// Permissions uses Claude Code rule syntax: Bash(npm test:*), Edit(src/**), mcp__srv__*.
type Permissions struct {
	Allow []string `json:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"`
	Ask   []string `json:"ask,omitempty"`
}

// HookMatcher is one entry of a hook event list (Claude Code settings.json shape).
type HookMatcher struct {
	Matcher string    `json:"matcher,omitempty"` // regexp over the tool name ("Edit|Write"); "" = all
	Hooks   []HookCmd `json:"hooks"`
}

type HookCmd struct {
	Type    string `json:"type"` // "command"
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"` // seconds, default 60
}

// OmniConf routes requests across every usable provider (free-first by default).
type OmniConf struct {
	Enabled    bool                `json:"enabled,omitempty"`
	Strategy   string              `json:"strategy,omitempty"`    // free-first (default) | weighted | ordered | cheapest
	Exclude    []string            `json:"exclude,omitempty"`     // provider names or refs (globs) never used
	Paid       string              `json:"paid,omitempty"`        // never | emergency (default) | always
	NoTraining bool                `json:"no_training,omitempty"` // drop providers whose free tier trains on prompts
	Tiers      map[string][]string `json:"tiers,omitempty"`       // per-alias override: ordered refs, globs allowed
	PrivacyAck bool                `json:"privacy_ack,omitempty"` // the user saw OmniPrivacyNotice
}

// UIConf: input editor options. palette: fuzzy (default) | prefix;
// mention_picker / history_prefix: on (default) | off.
type UIConf struct {
	Palette       string `json:"palette,omitempty"`
	MentionPicker string `json:"mention_picker,omitempty"`
	HistoryPrefix string `json:"history_prefix,omitempty"`
}

// uiOpt resolves one input option: env > config > on. Only an explicit
// off/0/false/no (or, for the palette, "prefix") turns it off.
func uiOpt(env, conf string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(env)))
	if v == "" {
		v = strings.ToLower(strings.TrimSpace(conf))
	}
	switch v {
	case "0", "off", "false", "no", "nao", "não", "prefix":
		return false
	}
	return true
}

type Config struct {
	Model         string                   `json:"model"`          // "provider/model-id"
	Fallback      []string                 `json:"fallback"`       // tried in order on retryable errors
	Mode          string                   `json:"mode,omitempty"` // auto | turbo | smart | ask | yolo (persisted; plan is never saved; "" = DefaultMode)
	ModeOverride  string                   `json:"-"`              // session-only mode (flags, Shift+Tab, plan); wins over Mode
	DefaultMode   string                   `json:"-"`              // mode when config sets none (interactive: turbo with an isolating sandbox)
	MaxSteps      int                      `json:"max_steps"`
	ContextWindow int                      `json:"context_window"`
	MaxTokens     int                      `json:"max_tokens"`
	Effort        string                   `json:"effort,omitempty"`
	ShowThinking  bool                     `json:"show_thinking"`
	NoVerifyNudge bool                     `json:"no_verify_nudge,omitempty"`
	Providers     map[string]*ProviderConf `json:"providers"`
	Models        map[string]*ModelConf    `json:"models,omitempty"`
	Permissions   Permissions              `json:"permissions,omitzero"`
	Hooks         map[string][]HookMatcher `json:"hooks,omitempty"` // event -> matchers
	Web           bool                     `json:"web"`             // expose the fetch tool (default true)
	Omni          OmniConf                 `json:"omni,omitzero"`
	Notify        string                   `json:"notify,omitempty"`     // bell (default) | osc | off
	UpdateURL     string                   `json:"update_url,omitempty"` // self-update source (update.go)
	// Confirmations (confirm.go): trust_default "prompt" (default) asks once per
	// folder, "always" trusts a folder that brings no config of its own without
	// asking; always_scope is what "Sim, e não perguntar de novo" remembers:
	// "auto" (default, as Claude Code: edits for this session, commands and
	// domains for this project), "session" or "project"; mode_intro is the
	// start mode whose one-line explanation was already shown.
	TrustDefault string `json:"trust_default,omitempty"`
	AlwaysScope  string `json:"always_scope,omitempty"`
	ModeIntro    string `json:"mode_intro,omitempty"`
	// FirstByteTimeout (seconds) bounds the wait for the first streamed data of a
	// free model that has a fallback after it; 0 = default (25 s), negative = off.
	FirstByteTimeout int `json:"first_byte_timeout,omitempty"`
	// Skills: built-in skills picked by the harness (skills.go). "auto" (default),
	// "manual" (only /skill <name>) or "off"; or an object with mode/claude_dir/...
	Skills SkillsConf `json:"skills,omitzero"`
	// Harness modes ("" = default; flags --budget/--quality and JAZZ_* env win).
	Budget        string   `json:"budget,omitempty"`         // lean (default) | normal | off (budget.go)
	Quality       string   `json:"quality,omitempty"`        // standard (default) | strict | off (quality.go)
	Guards        string   `json:"guards,omitempty"`         // on (default) | off: honest-completion guards (guard_honest.go)
	GuardsOff     []string `json:"guards_off,omitempty"`     // turn off single guards: tests, hardcode, done, eval
	NetWait       string   `json:"net_wait,omitempty"`       // on (default) | off: wait out network outages (netwait.go)
	FallbackOrder string   `json:"fallback_order,omitempty"` // score (default) | config: fallback order (scores.go)
	// Omni judge (judge.go): model per task type, roles, verifier, hedging,
	// panel and fusion (lead/sidekick). Flags --omni-judge/--verifier/--hedge/
	// --panel/--fusion and JAZZ_* env win.
	Judge JudgeConf `json:"judge,omitzero"`
	// Otlp: OTLP/HTTP JSON export of the event stream (otlp.go); off unless endpoint is set.
	Otlp   OtlpConf   `json:"otlp,omitzero"`
	Roles  RolesConf  `json:"roles,omitzero"`
	Fusion FusionConf `json:"fusion,omitzero"`
	// Subagents (subagent.go): parallel = how many task subagents run at once
	// (0 = auto: 3 when the model is remote, network-bound; 2 for a local
	// server; 1 = one at a time). --subagents-parallel and
	// JAZZ_SUBAGENTS_PARALLEL win.
	Subagents SubagentsConf `json:"subagents,omitzero"`
	// Output shaping (outshape.go reads these from config.json itself; they are
	// declared here so Save keeps them): parse_output (default true),
	// grep_trunc grouped|headtail, tool_budget bytes (0 = auto), prefetch off|grep.
	ParseOutput *bool  `json:"parse_output,omitempty"`
	GrepTrunc   string `json:"grep_trunc,omitempty"`
	ToolBudget  int    `json:"tool_budget,omitempty"`
	Prefetch    string `json:"prefetch,omitempty"`
	// Tools: tool execution settings. parallel: auto (default) | on | off | N
	// (partools.go reads it from config.json itself; declared so Save keeps it).
	Tools ToolsConf `json:"tools,omitzero"`
	// ResumeReplay: how much of a resumed conversation is shown again
	// (sessions_ui.go): N entries (default 200) | "all" | "off";
	// JAZZ_RESUME_REPLAY wins. SessionPicker: "on" (default, arrow-key list
	// for /resume, /sessions, /tree, jazz -r) | "off" (numbered list);
	// JAZZ_SESSION_PICKER wins.
	ResumeReplay  any    `json:"resume_replay,omitempty"`
	SessionPicker string `json:"session_picker,omitempty"`
	// Scripts toggles the script tool (scripts.go): "on" (default) | "off".
	Scripts string `json:"scripts,omitempty"`
	// UILint toggles the post-edit UI lint hook (uilint.go): "on" (default) | "off".
	UILint string `json:"ui_lint,omitempty"`
	// UI: input editor options (editor_palette.go); JAZZ_PALETTE,
	// JAZZ_MENTION_PICKER and JAZZ_HISTORY_PREFIX win.
	UI UIConf `json:"ui,omitzero"`
	// FastPaths toggles deterministic intent fast paths in interactive mode
	// (intents.go): "on" (default) | "off". Headless uses --fast-path.
	FastPaths string `json:"fastpaths,omitempty"`
	// Recover (agent_recover.go): on (default) | handoff | discard | off;
	// --recover and JAZZ_RECOVER win. PlanCache (plancache.go): on (default) |
	// off. Hindsight: off (default) | on | provider/model that writes the hint.
	Recover   string `json:"recover,omitempty"`
	PlanCache string `json:"plan_cache,omitempty"`
	Hindsight string `json:"hindsight,omitempty"`
	// Security round (security.md): PrivacyAck = the user saw the notice about
	// providers that may train on prompts (F4); MaxJobs caps background jobs
	// (0 = 8); InjectionGuard off | warn (default: mark + confirm) | strict
	// (also in yolo); SupplyChain on (default) | off | strict (name-only typosquat block) checks package installs
	// against the registries (guardrails.go).
	PrivacyAck     bool   `json:"privacy_ack,omitempty"`
	MaxJobs        int    `json:"max_jobs,omitempty"`
	InjectionGuard string `json:"injection_guard,omitempty"`
	SupplyChain    string `json:"supply_chain,omitempty"`
	// DiffGuard: on (default) | off | strict (MEDIUM findings ask too) — review of
	// the files the turn changed (diffguard.go); --diff-guard / JAZZ_DIFF_GUARD win.
	DiffGuard string `json:"diff_guard,omitempty"`
	// Sanitize: off | strip (default) | strict — invisible Unicode, terminal
	// escapes, look-alike letters and hidden HTML before content reaches the
	// model (sanitize.go).
	Sanitize string `json:"sanitize,omitempty"`
	// Limits (hardening2.go): ulimit caps every shell command inherits (processes, file size, open files, CPU seconds).
	ShellLimits *LimitsConf `json:"limits,omitempty"`
	// EgressAllow: hosts a bash command may send data to without asking
	// (curl -d, scp, nc …); globs like "*.mycorp.com". Loopback is always
	// allowed. "" = ask for every external destination (safety.go egress gate).
	EgressAllow []string `json:"egress_allow,omitempty"`
	// MaxTurnCost (USD) and MaxTurnTokens cap one turn; 0 = defaults
	// (defTurnCost/defTurnTokens). MaxTurnWriteMB caps bytes written to disk in
	// a turn (0 = defTurnWriteMB). The run stops with StopBudget (agent.go).
	MaxTurnCost    float64 `json:"max_turn_cost,omitempty"`
	MaxTurnTokens  int     `json:"max_turn_tokens,omitempty"`
	MaxTurnWriteMB int     `json:"max_turn_write_mb,omitempty"`
	// BlockUX: on (default) | off — one-line steering block messages and the
	// spiral breaker (blockux.go). AllowOutsideProject turns off the work-dir
	// fence (fence.go); --allow-outside does the same for one run.
	BlockUX             string `json:"block_ux,omitempty"`
	AllowOutsideProject bool   `json:"allow_outside_project,omitempty"`
	// --allow-outside / --block-ux for this run only (never saved).
	AllowOutsideRun bool   `json:"-"`
	BlockUXRun      string `json:"-"`
	DiffGuardRun    string `json:"-"`
	// Sandbox: auto (default: best OS backend, none = run without) | strict
	// (refuse to run without one; children get loopback-only network) | off.
	// SandboxNet: on | off overrides the network rule (sandbox.go).
	Sandbox    string `json:"sandbox,omitempty"`
	SandboxNet string `json:"sandbox_net,omitempty"`
	// CutoffContinue: on (default) | off — a write/edit cut at the output
	// limit is continued and spliced instead of refused (cutoff.go);
	// --cutoff-continue sets CutoffContinueRun for one run. MaxOutputTokens
	// caps the output budget raised after a cutoff (0 = the provider's limit
	// when known, else no raise above max_tokens).
	CutoffContinue    string `json:"cutoff_continue,omitempty"`
	CutoffContinueRun string `json:"-"`
	MaxOutputTokens   int    `json:"max_output_tokens,omitempty"`
	// Lessons (lessons.go): on (default) | off — .jazz/lessons.md and the
	// global lessons.md go into the session-start block. Improve: settings of
	// jazz improve (auto = learn after every -p run; project lessons only).
	Lessons string      `json:"lessons,omitempty"`
	Improve ImproveConf `json:"improve,omitzero"`
	// Docs: the docs tool, Context7 (opt-in), API-error hints and the project
	// API map note (docs.go reads it from config.json itself).
	Docs DocsConf `json:"docs,omitzero"`
}

// pvDefaultFirstByte is the first-byte deadline for free models with a fallback.
const pvDefaultFirstByte = 25 * time.Second

// FirstByte is the first-byte deadline for ref when another candidate follows it
// (0 = none: paid models and the last candidate wait the normal idle time).
func (c *Config) FirstByte(ref string) time.Duration {
	if c.FirstByteTimeout < 0 || !c.pvIsFreeish(ref) {
		return 0
	}
	if c.FirstByteTimeout > 0 {
		return time.Duration(c.FirstByteTimeout) * time.Second
	}
	return pvDefaultFirstByte
}

// EffMode is the permission mode in force: ModeOverride when set, else Mode.
// Flags (--yolo/--ask), Shift+Tab and plan mode only ever set ModeOverride,
// which is never written to config.json (bug 9).
func (c *Config) EffMode() string {
	if c.ModeOverride != "" {
		return c.ModeOverride
	}
	return c.BaseMode()
}

// BaseMode is the mode without the session override: config "mode", else
// DefaultMode (set by the interactive start: turbo when an OS sandbox
// isolates, pfDefaultMode), else auto.
func (c *Config) BaseMode() string {
	switch {
	case c.Mode != "":
		return c.Mode
	case c.DefaultMode != "":
		return c.DefaultMode
	}
	return "auto"
}

// SetMode is the /mode setter: persists Mode and clears the session override.
// "plan" is session-only, so it goes to ModeOverride instead.
func (c *Config) SetMode(m string) {
	if m == "plan" {
		c.ModeOverride = "plan"
		return
	}
	c.Mode, c.ModeOverride = m, ""
}

func defaultProviders() map[string]*ProviderConf {
	m := map[string]*ProviderConf{
		"anthropic": {Type: "anthropic", BaseURL: "https://api.anthropic.com", KeyEnv: "ANTHROPIC_API_KEY", DefaultModel: "claude-sonnet-5-5",
			Signup: "https://console.anthropic.com/settings/keys", KeyHint: "começa com sk-ant-", KeyRE: `^sk-ant-`, Title: "Anthropic (pago)"},
		"openai": {Type: "openai", BaseURL: "https://api.openai.com/v1", KeyEnv: "OPENAI_API_KEY", DefaultModel: "gpt-6-astra",
			Signup: "https://platform.openai.com/api-keys", KeyHint: "começa com sk-", KeyRE: `^sk-`, Quirks: []string{"effort"}, Title: "OpenAI API (pago)"},
		"chatgpt": {Type: "codex", BaseURL: "https://chatgpt.com/backend-api/codex", DefaultModel: "gpt-5.5-codex", Sub: true, Title: "ChatGPT (assinatura)"},
	}
	for k, v := range omniPresets() {
		m[k] = v
	}
	for k, v := range localPresets() {
		m[k] = v
	}
	return m
}

// localPresets are keyless servers on this machine or the LAN/tailnet (G3).
func localPresets() map[string]*ProviderConf {
	ollama := "http://127.0.0.1:11434/v1"
	if h := strings.TrimSpace(os.Getenv("OLLAMA_HOST")); h != "" {
		if !strings.Contains(h, "://") {
			h = "http://" + h
		}
		ollama = strings.TrimRight(h, "/") + "/v1"
	}
	return map[string]*ProviderConf{
		"ollama":   {Type: "openai", BaseURL: ollama, NoKey: true, Local: true, Ollama: true, Free: true, Title: "Ollama (local)"},
		"llamacpp": {Type: "openai", BaseURL: "http://127.0.0.1:8080/v1", NoKey: true, Local: true, Free: true, Title: "llama.cpp (local)"},
		"lmstudio": {Type: "openai", BaseURL: "http://127.0.0.1:1234/v1", NoKey: true, Local: true, Free: true, Title: "LM Studio (local)"},
	}
}

func configDir() string {
	if d := os.Getenv("JAZZ_HOME"); d != "" {
		return d
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "jazz-cli")
	}
	if runtime.GOOS == "windows" {
		// %APPDATA%\jazz-cli (roams with the profile, like the Unix ~/.config)
		if d, err := os.UserConfigDir(); err == nil && d != "" {
			return filepath.Join(d, "jazz-cli")
		}
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "jazz-cli")
}

func dataDir() string {
	if d := os.Getenv("JAZZ_HOME"); d != "" {
		return filepath.Join(d, "data")
	}
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "jazz-cli")
	}
	if runtime.GOOS == "windows" {
		// %LOCALAPPDATA%\jazz-cli: sessions, logs and caches stay on this machine
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "jazz-cli")
		}
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".local", "share", "jazz-cli")
}

func LoadConfig() (*Config, error) {
	c := &Config{MaxSteps: 80, ContextWindow: 128000, MaxTokens: 16000, Web: true}
	p := filepath.Join(configDir(), "config.json")
	b, err := os.ReadFile(p)
	if err == nil {
		if err := json.Unmarshal(b, c); err != nil {
			return nil, fmt.Errorf("config.json inválido (%s): %w", p, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	c.applyDefaults()
	return c, nil
}

// applyDefaults merges compiled-in providers and preset models (user entries win,
// presets fill the gaps) and normalises the scalar settings.
func (c *Config) applyDefaults() {
	if c.Providers == nil {
		c.Providers = map[string]*ProviderConf{}
	}
	for k, v := range defaultProviders() { // user entries override, defaults fill the gaps
		if u, ok := c.Providers[k]; ok && u != nil {
			pvFillProvider(u, v)
		} else {
			c.Providers[k] = v
		}
	}
	// zen's free models only answer the official OpenCode client now (403 FreeTierError):
	// an old saved "no_key": true would route every keyless request into that wall.
	if z := c.Providers["zen"]; z != nil && strings.Contains(z.BaseURL, "opencode.ai") {
		z.NoKey = false
	}
	if c.Models == nil {
		c.Models = map[string]*ModelConf{}
	}
	for ref, mc := range presetModelMap() {
		if u, ok := c.Models[ref]; ok && u != nil {
			pvFillModel(u, mc)
		} else {
			cp := *mc
			c.Models[ref] = &cp
		}
	}
	if c.MaxSteps <= 0 {
		c.MaxSteps = 80
	}
	if c.ContextWindow <= 0 {
		c.ContextWindow = 128000
	}
	if c.MaxTokens <= 0 {
		c.MaxTokens = 16000
	}
	c.Mode = tbModeName(c.Mode) // "full-auto" is turbo
	switch c.Mode {
	case "", "auto", "smart", "ask", "yolo", "turbo":
	default:
		c.Mode = "" // includes "plan": never persisted, never loaded
	}
}

// pvFillProvider copies preset fields into a user entry where the user left them empty.
func pvFillProvider(u, d *ProviderConf) {
	if u.Type == "" {
		u.Type = d.Type
	}
	if u.BaseURL == "" {
		u.BaseURL = d.BaseURL
	}
	if u.KeyEnv == "" {
		u.KeyEnv = d.KeyEnv
	}
	if u.DefaultModel == "" || (u.DefaultModel == pvOldKiloDefault && d.DefaultModel == kiloDefaultModel) {
		u.DefaultModel = d.DefaultModel // the old keyless default stalls: migrate it
	}
	if u.Headers == nil && d.Headers != nil {
		u.Headers = d.Headers
	}
	u.NoKey = u.NoKey || d.NoKey
	u.Ollama = u.Ollama || d.Ollama
	u.Free = u.Free || d.Free
	u.Sub = u.Sub || d.Sub
	u.Card = u.Card || d.Card
	u.Train = u.Train || d.Train
	u.Account = u.Account || d.Account
	u.Local = u.Local || d.Local
	u.FreeIDs = u.FreeIDs || d.FreeIDs
	if u.Weight == 0 {
		u.Weight = d.Weight
	}
	if u.Router == "" {
		u.Router = d.Router
	}
	if u.Quirks == nil {
		u.Quirks = d.Quirks
	}
	if u.Limits == nil {
		u.Limits = d.Limits
	}
	if u.Signup == "" {
		u.Signup = d.Signup
	}
	if u.KeyHint == "" {
		u.KeyHint = d.KeyHint
	}
	if u.KeyRE == "" {
		u.KeyRE = d.KeyRE
	}
	if u.Probe == "" {
		u.Probe = d.Probe
	}
	if u.AccountEnv == "" {
		u.AccountEnv = d.AccountEnv
	}
	if u.Title == "" {
		u.Title = d.Title
	}
}

func pvFillModel(u, d *ModelConf) {
	if u.Context == 0 {
		u.Context = d.Context
	}
	if u.MaxTokens == 0 {
		u.MaxTokens = d.MaxTokens
	}
	if u.Tools == "" {
		u.Tools = d.Tools
	}
	if u.Q == 0 {
		u.Q = d.Q
	}
	if u.Limits == nil {
		u.Limits = d.Limits
	}
	if u.MaxReq == 0 {
		u.MaxReq = d.MaxReq
	}
	if u.MaxOut == 0 {
		u.MaxOut = d.MaxOut
	}
	if u.Tiers == nil {
		u.Tiers = d.Tiers
	}
	if u.Images == nil {
		u.Images = d.Images
	}
}

// Save writes config.json. Entries identical to a compiled-in preset are left out,
// so the file stays small and presets improve when the binary is upgraded.
// ModeOverride (flags, plan) is never written.
func (c *Config) Save() error {
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		return err
	}
	cp := *c
	defs := defaultProviders()
	cp.Providers = map[string]*ProviderConf{}
	for k, v := range c.Providers {
		if d, ok := defs[k]; ok && pvSameJSON(v, d) {
			continue
		}
		cp.Providers[k] = v
	}
	pm := presetModelMap()
	cp.Models = map[string]*ModelConf{}
	for k, v := range c.Models {
		if d, ok := pm[k]; ok && pvSameJSON(v, d) {
			continue
		}
		cp.Models[k] = v
	}
	if len(cp.Models) == 0 {
		cp.Models = nil
	}
	b, err := json.MarshalIndent(&cp, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(configDir(), "config.json"), b, 0o600)
}

func pvSameJSON(a, b any) bool {
	if reflect.DeepEqual(a, b) {
		return true
	}
	x, e1 := json.Marshal(a)
	y, e2 := json.Marshal(b)
	return e1 == nil && e2 == nil && bytes.Equal(x, y)
}

// SplitModel turns "provider/model/id" into ("provider", "model/id").
func SplitModel(ref string) (string, string) {
	i := strings.IndexByte(ref, '/')
	if i < 0 {
		return ref, ""
	}
	return ref[:i], ref[i+1:]
}

// ResolveModel normalises a user-typed ref: "anthropic" -> "anthropic/<default>",
// "omni" -> "omni/coder".
func (c *Config) ResolveModel(ref string) (string, error) {
	prov, model := SplitModel(ref)
	if prov == OmniProvider {
		if model == "" {
			model = "coder"
		}
		if !isOmniTier(model) {
			return "", fmt.Errorf("apelido omni desconhecido %q (use: %s)", model, strings.Join(omniTiers, ", "))
		}
		return OmniProvider + "/" + model, nil
	}
	pc, ok := c.Providers[prov]
	if !ok {
		return "", fmt.Errorf("provedor desconhecido %q (use: %s)", prov, strings.Join(c.ProviderNames(), ", "))
	}
	if model == "" {
		model = pc.DefaultModel
	}
	return prov + "/" + model, nil
}

func (c *Config) ProviderNames() []string {
	var n []string
	for k := range c.Providers {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

// ---- credentials (auth.json, 0600) ----

type Cred struct {
	Type        string `json:"type"` // api | oauth
	Key         string `json:"key,omitempty"`
	Access      string `json:"access,omitempty"`
	Refresh     string `json:"refresh,omitempty"`
	IDToken     string `json:"id_token,omitempty"`
	Expires     int64  `json:"expires,omitempty"` // unix ms
	AccountID   string `json:"account_id,omitempty"`
	LastRefresh int64  `json:"last_refresh,omitempty"` // unix ms
}

var (
	authMu      sync.Mutex
	authLoadErr string // last auth.json parse error (shown once by main/doctor via AuthWarning)
	authWarned  string // the authLoadErr already shown; a new or recurring problem warns again
)

func authPath() string { return filepath.Join(configDir(), "auth.json") }

// AuthWarning returns a one-time warning when auth.json could not be parsed ("" otherwise).
func AuthWarning() string {
	authMu.Lock()
	defer authMu.Unlock()
	if authLoadErr == "" || authWarned == authLoadErr {
		return ""
	}
	authWarned = authLoadErr
	return authLoadErr
}

func LoadAuth() map[string]*Cred {
	authMu.Lock()
	defer authMu.Unlock()
	m, _ := pvReadAuthLocked()
	if m == nil {
		m = map[string]*Cred{}
	}
	return m
}

// pvReadAuthLocked reads auth.json. A missing file is an empty map; a corrupt
// one returns (nil, err) and records the warning. Caller holds authMu.
func pvReadAuthLocked() (map[string]*Cred, error) {
	m := map[string]*Cred{}
	b, err := os.ReadFile(authPath())
	if errors.Is(err, os.ErrNotExist) {
		authLoadErr, authWarned = "", ""
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		authLoadErr = fmt.Sprintf("auth.json vazio (%s) — corrija ou apague; cópia anterior em auth.json.bak", authPath())
		return nil, errors.New(authLoadErr)
	}
	if err := json.Unmarshal(b, &m); err != nil {
		authLoadErr = fmt.Sprintf("auth.json corrompido (%s: %v) — corrija ou apague; cópia anterior em auth.json.bak", authPath(), err)
		return nil, errors.New(authLoadErr)
	}
	if m == nil {
		m = map[string]*Cred{}
	}
	authLoadErr, authWarned = "", "" // file is healthy again: drop the stale warning
	return m, nil
}

// SaveCred stores (or with c == nil removes) one provider's credential. It never
// overwrites a corrupt auth.json (that would silently drop every other credential),
// keeps auth.json.bak with the previous good content, and fsyncs (bug 34).
func SaveCred(provider string, c *Cred) error {
	authMu.Lock()
	defer authMu.Unlock()
	m, err := pvReadAuthLocked()
	if err != nil {
		return err
	}
	if c == nil {
		delete(m, provider)
	} else {
		m[provider] = c
	}
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		return err
	}
	if old, err := os.ReadFile(authPath()); err == nil && len(old) > 0 {
		_ = writeFileAtomic(authPath()+".bak", old, 0o600)
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return writeFileAtomic(authPath(), b, 0o600)
}

// APIKey returns the key for a provider: env var wins, then auth.json.
func (c *Config) APIKey(provider string) string {
	if pc, ok := c.Providers[provider]; ok && pc.KeyEnv != "" {
		if v := strings.TrimSpace(os.Getenv(pc.KeyEnv)); v != "" {
			return v
		}
		for _, alt := range pvAltKeyEnv[pc.KeyEnv] {
			if v := strings.TrimSpace(os.Getenv(alt)); v != "" {
				return v
			}
		}
	}
	if cr := LoadAuth()[provider]; cr != nil && cr.Type == "api" {
		return cr.Key
	}
	return ""
}

// pvAltKeyEnv lists alternative env var names accepted for a key.
var pvAltKeyEnv = map[string][]string{
	"GEMINI_API_KEY": {"GOOGLE_API_KEY"},
	"CO_API_KEY":     {"COHERE_API_KEY"},
}

// AccountID returns the account/workspace id for providers whose URL needs one (Cloudflare).
func (c *Config) AccountID(provider string) string {
	if pc, ok := c.Providers[provider]; ok && pc.AccountEnv != "" {
		if v := strings.TrimSpace(os.Getenv(pc.AccountEnv)); v != "" {
			return v
		}
	}
	if cr := LoadAuth()[provider]; cr != nil {
		return cr.AccountID
	}
	return ""
}

// HasCreds reports whether a provider is usable right now. Local NoKey servers
// (ollama, llamacpp, lmstudio, `provider add` on a LAN host) count only when a
// short probe answers (cached). "omni" is usable when any of its candidates is.
func (c *Config) HasCreds(provider string) bool {
	if provider == OmniProvider {
		return len(c.CandidatesFor(Need{}, OmniProvider+"/free")) > 0
	}
	pc, ok := c.Providers[provider]
	if !ok {
		return false
	}
	if pc.Type == "codex" {
		cr := LoadAuth()[provider]
		return cr != nil && cr.Type == "oauth" && cr.Refresh != ""
	}
	if pc.Account && c.AccountID(provider) == "" {
		return false
	}
	if pc.NoKey {
		if pc.Local {
			return pvReachable(c, provider)
		}
		return true
	}
	return c.APIKey(provider) != ""
}

// PickDefaultModel chooses a model when the config has none: omni when enabled,
// then the first provider with credentials, then a reachable local server, then
// the anonymous Kilo gateway (zen's free tier is closed to third-party clients).
func (c *Config) PickDefaultModel() string {
	if c.Omni.Enabled {
		return OmniProvider + "/coder"
	}
	for _, p := range []string{"anthropic", "chatgpt", "openai", "openrouter", "gemini", "nvidia", "groq", "mistral", "deepseek"} {
		if pc, ok := c.Providers[p]; ok && c.HasCreds(p) {
			return p + "/" + pc.DefaultModel
		}
	}
	for _, p := range []string{"ollama", "llamacpp", "lmstudio"} {
		if pc, ok := c.Providers[p]; ok && pc.DefaultModel != "" && c.HasCreds(p) {
			return p + "/" + pc.DefaultModel
		}
	}
	if pc, ok := c.Providers["kilo"]; ok {
		return "kilo/" + pc.DefaultModel
	}
	return "kilo/" + kiloDefaultModel
}

func (c *Config) NewProvider(provider string) (Provider, error) {
	if provider == OmniProvider {
		return nil, errors.New("omni é um roteador virtual: use um modelo concreto (cfg.Candidates expande omni/…)")
	}
	pc, ok := c.Providers[provider]
	if !ok {
		return nil, fmt.Errorf("provedor desconhecido %q", provider)
	}
	base := pc.BaseURL
	if strings.Contains(base, "{ACCOUNT}") {
		id := c.AccountID(provider)
		if id == "" {
			env := pc.AccountEnv
			if env == "" {
				env = "o id da conta"
			}
			return nil, fmt.Errorf("%s precisa do id da conta — exporte %s ou rode /omni add %s", provider, env, provider)
		}
		base = strings.ReplaceAll(base, "{ACCOUNT}", id)
	}
	hdr := map[string]string{}
	for k, v := range pc.Headers {
		hdr[k] = expandEnv(v)
	}
	switch pc.Type {
	case "openai", "gemini":
		key := c.APIKey(provider)
		if key == "" && !pc.NoKey {
			return nil, fmt.Errorf("sem chave para %s — rode /key %s (ou exporte %s)", provider, provider, pc.KeyEnv)
		}
		p := &OpenAIProvider{Name: provider, BaseURL: base, Key: key, Headers: hdr, Router: pc.Router,
			Quirks: pc.Quirks, Ollama: pc.Ollama, cfg: c}
		if pc.Type == "gemini" {
			pvGeminiSetup(p)
		}
		return p, nil
	case "anthropic":
		key := c.APIKey(provider)
		if key == "" {
			return nil, fmt.Errorf("sem chave para %s — rode /key %s (ou exporte %s)", provider, provider, pc.KeyEnv)
		}
		return &AnthropicProvider{Name: provider, BaseURL: base, Key: key, Headers: hdr, cfg: c}, nil
	case "codex":
		if !c.HasCreds(provider) {
			return nil, fmt.Errorf("não logado no ChatGPT — rode: jazz login chatgpt")
		}
		return &CodexProvider{Name: provider, BaseURL: base}, nil
	}
	return nil, fmt.Errorf("tipo de provedor desconhecido %q", pc.Type)
}

// writeFileAtomic writes via a temp file + rename, fsyncing the file and its
// directory so a power cut cannot leave a zero-length file behind (bug 34).
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, perm); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}
