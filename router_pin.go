package main

// router_pin.go: the model the user chose is a contract (F1.3, PROV-1), and
// every swap is said out loud.
//
//   - Pin (-m, /model or "model" in the config): the chain is the pin +
//     cfg.Fallback (configured = consent, omni.go pinnedCandidates). The pin
//     is never dropped for being dead: it is said ("sem cota até 15:00 —
//     tentando assim mesmo") and tried. --strict-model: the pin alone.
//   - The pin failed (and its fallback): interactive asks "usar X neste
//     turno?" (s / sempre / não); headless fails with acPinError — exit 75
//     (StopModelUnavailable), message naming the pin, the cause and the fix.
//     fallback_on_pinned: auto switches on its own (announced), off never.
//   - Unpinned: the requested model leaving the chain and any answer served
//     by another model are announced once per turn (acAnnounceDrop, rtOnce).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// cfgModelChosen: the user chose the model — -m given, or a "model" in the
// config other than the keyless default. One rule for the binary (main.go)
// and the replay suite, so a replayed "-m X" is the same contract.
func cfgModelChosen(flagModel string, cfg *Config) bool {
	return flagModel != "" || (cfg.Model != "" && cfg.Model != "kilo/"+kiloDefaultModel)
}

// acRoute is the routing state of one agent.
type acRoute struct {
	mu     sync.Mutex
	swapOK bool            // this turn: the user accepted another model after the pin failed
	once   map[string]bool // announcements already made this turn (served pairs, dropped refs, pin state)
	said   map[string]bool // announcements made once per session (a reorder, not a failure)
	served map[string]int  // successful requests per ref (run.end telemetry)
}

// acPin is the pinned ref for a request of role ("" = not pinned): the main
// agent's own turns and compactions, while the user has not accepted another
// model for this turn.
func (a *Agent) acPin(role string) string {
	if a.sub || a.cfg == nil || !a.cfg.Pinned || a.Model == "" || isOmniRef(a.Model) {
		return ""
	}
	if role != "main" && role != "compact" {
		return ""
	}
	if a.cfg.FallbackOnPinned == "auto" && !a.cfg.StrictModel {
		return ""
	}
	a.rt.mu.Lock()
	ok := a.rt.swapOK
	a.rt.mu.Unlock()
	if ok {
		return ""
	}
	return a.Model
}

// rtBeginTurn: a "yes, use X this turn" ends with the turn, and every swap
// is said again in the new turn (once per turn, not once per step).
func (a *Agent) rtBeginTurn() {
	a.rt.mu.Lock()
	a.rt.swapOK = false
	a.rt.once = nil
	a.rt.mu.Unlock()
}

// rtOnce reports whether key is new in this turn (and records it).
func (a *Agent) rtOnce(key string) bool {
	a.rt.mu.Lock()
	defer a.rt.mu.Unlock()
	if a.rt.once[key] {
		return false
	}
	if a.rt.once == nil {
		a.rt.once = map[string]bool{}
	}
	a.rt.once[key] = true
	return true
}

// rtOnceTurnSaid reports whether key was already said this turn (records nothing).
func (a *Agent) rtOnceTurnSaid(key string) bool {
	a.rt.mu.Lock()
	defer a.rt.mu.Unlock()
	return a.rt.once[key]
}

// rtOnceSession reports whether key is new in this session (and records it).
func (a *Agent) rtOnceSession(key string) bool {
	a.rt.mu.Lock()
	defer a.rt.mu.Unlock()
	if a.rt.said[key] {
		return false
	}
	if a.rt.said == nil {
		a.rt.said = map[string]bool{}
	}
	a.rt.said[key] = true
	return true
}

func (a *Agent) rtServe(ref string) {
	a.rt.mu.Lock()
	if a.rt.served == nil {
		a.rt.served = map[string]int{}
	}
	a.rt.served[ref]++
	a.rt.mu.Unlock()
}

// rtServed is a copy of the successful requests per ref.
func (a *Agent) rtServed() map[string]int {
	a.rt.mu.Lock()
	defer a.rt.mu.Unlock()
	out := make(map[string]int, len(a.rt.served))
	for k, v := range a.rt.served {
		out[k] = v
	}
	return out
}

// acSkipWait: a candidate cooling for longer than this is skipped while
// another one is ready (the same threshold as "a long wait: next model").
const acSkipWait = 20 * time.Second

// acCandDown: the candidate cannot take this request now.
func acCandDown(st rlState) bool {
	return st.Dead || st.TooBig || (!st.Ready && st.Wait > acSkipWait)
}

// acUsableAfter: some candidate in rest can take the request now.
func (a *Agent) acUsableAfter(rest []string, need Need, lim Limiter, avoid string) bool {
	for _, r := range rest {
		if r == avoid {
			continue
		}
		if ok, _ := lim.Allow(r); ok && !acCandDown(a.cfg.pvState(r, need.Tokens)) {
			return true
		}
	}
	return false
}

// acEarliest is the index in rest of the candidate that frees up first (all
// of them are down): the only one that gets a request.
func acEarliest(rest []string, need Need, c *Config, avoid string) int {
	best, bestWake := 0, time.Duration(1<<62)
	for i, r := range rest {
		if r == avoid {
			continue
		}
		st := c.pvState(r, need.Tokens)
		wake := st.Wait
		if st.Dead && wake <= 0 || st.TooBig {
			wake = 1 << 61
		}
		if wake < bestWake {
			best, bestWake = i, wake
		}
	}
	return best
}

// acStateWhy says in PT-BR why a candidate is down.
func acStateWhy(st rlState) string {
	why := st.Why
	if why == "" {
		why = "indisponível"
	}
	switch {
	case st.Dead && st.Wait > 0 && st.Wait < 48*time.Hour:
		why += " até " + time.Now().Add(st.Wait).Format("15:04")
	case !st.Dead && st.Wait > 0:
		why += " (libera em " + st.Wait.Round(time.Second).String() + ")"
	}
	return why
}

// acPinStateWarn: the pinned model looks dead, and is tried anyway.
func (a *Agent) acPinStateWarn(ref string, st rlState, quiet bool) {
	if quiet || !a.rtOnce("pinstate:"+ref+":"+st.Why) {
		return
	}
	a.ui.Warn(uiShortModel(ref) + ": " + acStateWhy(st) + " — tentando assim mesmo (é o modelo que você escolheu)")
}

// acAnnounceDrop: unpinned, the requested model left the chain before any
// request (dead, too big, cooling): say so once, with the reason and who
// answers instead.
func (a *Agent) acAnnounceDrop(model string, cands []string, need Need) {
	if model == "" || isOmniRef(model) || len(cands) == 0 || cands[0] == model {
		return
	}
	for _, c := range cands {
		if c == model {
			return // reordered, still in the chain: the served notice covers it
		}
	}
	st := a.cfg.pvState(model, need.Tokens)
	if !a.rtOnce("drop:" + model + ":" + st.Why) {
		return
	}
	a.ui.Warn(fmt.Sprintf("%s indisponível (%s) → usando %s", uiShortModel(model), acStateWhy(st), uiShortModel(cands[0])))
}

// acWaitBudget is how long one ref may spend in backoff in one chain (PROV-5):
// a person waiting gets ~30 s, headless ~120 s.
var acWaitBudget = func(interactive bool) time.Duration {
	if interactive {
		return 30 * time.Second
	}
	return 120 * time.Second
}

// acRetryWhy names a retryable failure in PT-BR for the "tentando de novo" line.
func acRetryWhy(err error) string {
	if errors.Is(err, errStreamIdle) {
		return "o modelo parou de responder"
	}
	var ae *APIError
	if !errors.As(err, &ae) {
		return oneLine(err.Error(), 80)
	}
	switch {
	case ae.Status == 429:
		return "limite de requisições"
	case ae.Status == 0:
		return "falha de conexão"
	case ae.Status >= 500:
		return fmt.Sprintf("provedor instável (HTTP %d)", ae.Status)
	}
	return fmt.Sprintf("HTTP %d: %s", ae.Status, pvPlainErr(ae.Body))
}

// acPinError: the pinned model (and its configured fallback) failed and the
// jazz did not switch to another model on its own.
type acPinError struct {
	Ref string // the pinned model
	Alt string // what would have answered ("" = nothing else is up)
	Err error
}

func (e *acPinError) Error() string {
	return fmt.Sprintf("modelo escolhido %s falhou: %v", e.Ref, e.Err)
}

func (e *acPinError) Unwrap() error { return e.Err }

// acPinSwappable: the failure is about the model being available — another
// model could answer. A refusal, a broken tool call, an overflow, no network
// or a reply cut mid-stream are not.
func acPinSwappable(resp *ChatResponse, err error) bool {
	if acPartialOnly(resp) != nil || errors.Is(err, errNoNet) {
		return false
	}
	var re *RefusalError
	var tce *acToolCallError
	if errors.As(err, &re) || errors.As(err, &tce) {
		return false
	}
	switch pvClassify(err) {
	case "toolcall", "overflow", "cancel":
		return false
	}
	return true
}

// acPinAlternative is the model that would answer if the pin were not a
// contract: the first live candidate of the unpinned chain outside the pin
// and its configured fallback (already tried).
func (a *Agent) acPinAlternative(pin string) string {
	need := Need{Tokens: a.estTokens(), Tools: !a.textMode(pin), Role: "main"}
	tried := map[string]bool{pin: true}
	for _, r := range a.cfg.Fallback {
		tried[r] = true
	}
	for _, r := range a.cfg.CandidatesFor(need, pin) {
		if tried[r] || isOmniRef(r) {
			continue
		}
		if !acCandDown(a.cfg.pvState(r, need.Tokens)) {
			return r
		}
	}
	return ""
}

// acPinFailed decides what happens when the pinned model failed: ask (TUI),
// switch announced (fallback_on_pinned: auto never gets here), or fail clearly.
func (a *Agent) acPinFailed(ctx context.Context, pin string, o acChatOpt, used string, err error) (*ChatResponse, string, error) {
	alt := a.acPinAlternative(pin)
	pe := &acPinError{Ref: pin, Alt: alt, Err: err}
	if a.cfg.StrictModel || a.cfg.FallbackOnPinned == "off" || alt == "" || !a.Interactive {
		dbg("route", map[string]any{"pin_failed": pin, "alt": alt, "ask": false, "err": oneLine(err.Error(), 200)})
		return nil, used, pe
	}
	detail := fmt.Sprintf("%s falhou: %s.\nUsar %s neste turno? (no próximo eu volto a tentar %s)", uiShortModel(pin), acPinWhy(err), uiShortModel(alt), uiShortModel(pin))
	ap, _ := a.ui.Approve(ctx, "modelo", detail, "")
	dbg("route", map[string]any{"pin_failed": pin, "alt": alt, "ask": true, "answer": int(ap)})
	switch ap {
	case Allow:
		a.rt.mu.Lock()
		a.rt.swapOK = true
		a.rt.mu.Unlock()
	case AllowAlways:
		a.cfg.Pinned = false
		a.ui.Notice("ok: nesta sessão, quando " + uiShortModel(pin) + " falhar eu troco sozinho — sempre avisando")
	default:
		return nil, used, pe
	}
	o.avoid = pin
	return a.acChain(ctx, alt, o)
}

// acPinWhy is the short PT-BR cause of a pin failure (for the question).
func acPinWhy(err error) string {
	switch pvClassify(err) {
	case "daily":
		var ae *APIError
		if errors.As(err, &ae) && ae.Quota {
			return "a cota grátis acabou"
		}
		return "cota diária esgotada"
	case "rate":
		return "limite de requisições"
	case "auth":
		return "chave recusada"
	case "gone":
		return "modelo aposentado pelo provedor"
	case "credit":
		return "sem crédito"
	case "transient":
		return "provedor fora do ar ou instável"
	case "toobig":
		return "pedido grande demais pra ele"
	}
	if errors.Is(err, errFirstByte) {
		return "não respondeu a tempo"
	}
	return oneLine(friendlyErr(err), 120)
}

// pinErrText is friendlyErr for acPinError: the pin, the cause, and the fix —
// never a silent switch.
func pinErrText(e *acPinError) string {
	cause := strings.TrimPrefix(friendlyErr(e.Err), e.Ref+": ")
	msg := fmt.Sprintf("o modelo que você escolheu (%s) falhou: %s — não troquei de modelo sozinho", e.Ref, cause)
	if e.Alt != "" {
		return msg + ". Pra usar outro: -m " + e.Alt + ", ou ponha \"fallback\" (ou \"fallback_on_pinned\": \"auto\") no config"
	}
	return msg + ". Nenhum outro modelo está disponível agora: tente mais tarde ou salve outra chave (jazz key openrouter)"
}
