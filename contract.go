package main

// contract.go holds the types shared by more than one implementer area
// (agent-core, ui, providers, platform). It is FROZEN: see CONTRACT.md.
// Do not edit it in a feature branch; propose changes to the contract owner.

import (
	"context"
	"time"
)

// UI is everything the agent loop needs from the terminal (or a headless sink).
// Implementations: TermUI (ui.go), HeadlessUI (headless.go), JSONUI (jsonui.go),
// subUI (subagent.go). All methods may be called from the agent goroutine while
// other goroutines (spinner, key reader) run, so implementations lock internally.
type UI interface {
	StreamText(s string)
	StreamReasoning(s string)
	Thinking(label string) // start/refresh spinner
	StopThinking()
	// ToolStart/ToolEnd carry the tool call id so concurrent (parallel read-only)
	// calls can be matched; id is "" for synthetic entries (e.g. /todo display).
	ToolStart(id, name, detail string)
	ToolEnd(id, name string, out ToolOutput, d time.Duration)
	Notice(msg string)
	Warn(msg string)
	Error(msg string)
	// Approve asks the user to confirm a tool call. It must return (Deny, "")
	// promptly when ctx is cancelled or input hits EOF. The string is optional
	// free-text feedback typed by the user when denying ("" otherwise).
	Approve(ctx context.Context, tool, detail, reason string) (Approval, string)
	TurnEnd(st TurnStats)
	// SetMode tells the UI the effective permission mode (auto|smart|ask|yolo|plan).
	SetMode(mode string)
	// Jobs tells the UI how many background bash jobs are running.
	Jobs(n int)
	// Bell signals that a long turn finished (terminal bell / OSC notification;
	// the implementation honours cfg.Notify and whether stdout is a TTY).
	Bell()
}

type Approval int

const (
	Deny Approval = iota
	Allow
	AllowAlways
)

type TurnStats struct {
	Dur   time.Duration
	Tools int
	Usage Usage
	Model string
	Ctx   int     // estimated tokens in context
	Win   int     // context window of Model
	Cost  float64 // USD for this turn (provider-reported or priced), 0 = unknown/free
	Stop  string  // string(StopReason)
	Steps int     // model requests made in this turn
	// Redacted: distinct secrets first replaced by placeholders this turn (redact.go)
	Redacted int
}

// StopReason says why Agent.Run returned.
type StopReason string

const (
	StopDone        StopReason = "done"         // model gave a final answer
	StopStepLimit   StopReason = "step_limit"   // cfg.MaxSteps reached
	StopLoop        StopReason = "loop"         // repetition guard stopped the turn
	StopDenied      StopReason = "denied"       // turn ended after a tool call was denied
	StopTruncated   StopReason = "truncated"    // output limit hit and continuation budget exhausted
	StopInterrupted StopReason = "interrupted"  // ctx cancelled (Esc, Ctrl+C, SIGTERM)
	StopError       StopReason = "error"        // provider/other error; err != nil
	StopUnverified  StopReason = "unverified"   // final answer claimed actions but no tool ran (anti-fake guard)
	StopTimeout     StopReason = "timeout"      // headless --timeout deadline hit
	StopUntilFailed StopReason = "until_failed" // --until command still failing after the last round (agent_loop.go)
	// StopQualityFailed: the quality checklist still fails after its one feedback round (quality.go).
	StopQualityFailed StopReason = "quality_failed"
)

// ExitCode maps a stop reason to the headless process exit code.
func (r StopReason) ExitCode() int {
	switch r {
	case StopDone:
		return 0
	case StopError:
		return 1
	case StopStepLimit:
		return 3
	case StopLoop:
		return 4
	case StopDenied:
		return 5
	case StopTruncated:
		return 6
	case StopUnverified:
		return 7
	case StopUntilFailed:
		return 8
	case StopQualityFailed:
		return 9
	case StopTimeout:
		return 124
	case StopInterrupted:
		return 130
	}
	return 1
}

// RunInfo is announced once by headless sinks before the first turn.
type RunInfo struct {
	SessionID string
	Model     string
	Cwd       string
	Tools     []string
}

// RunResult is the final record of a headless run.
type RunResult struct {
	Status    StopReason
	Text      string // final assistant text
	Err       string // friendly error text when Status == StopError
	Usage     Usage
	Cost      float64
	Turns     int // model requests made
	Tools     int // tool calls executed
	Duration  time.Duration
	SessionID string
	Model     string
	Recover   string // reason of the last handoff ("" = none; agent_recover.go)
}

// HeadlessSink is a UI used by `jazz -p` (HeadlessUI for text, JSONUI for json/stream-json).
type HeadlessSink interface {
	UI
	Begin(info RunInfo)
	Finish(res RunResult) // prints the final output (text, json object, or result event)
}

// TurnRef identifies a user message for /rewind and /fork.
type TurnRef struct {
	Index int    // index in Agent.Msgs
	Text  string // the user's text (system reminders stripped)
}

// ContextPart is one row of the /context breakdown.
type ContextPart struct {
	Label  string
	Tokens int
}
