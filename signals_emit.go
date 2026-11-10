package main

// signals_emit.go: the points where the offline brain writes to the ledger
// (signals.go). A signal is a fact the forge can learn from — never an
// instruction, never a prompt — so each one carries the request, the
// reading (intent, calibrated and raw confidence, alternatives, evidence)
// and what happened:
//
//	unknown_intent   the grammar did not understand the request
//	miss             understood, but under the act threshold (below_act), or
//	                 the owner said "not this" (rejected) / "let me rephrase"
//	                 (edit) at the confirm box
//	new_method       understood, no offline method for it (or a known intent
//	                 that is not level 0 and went to the model)
//	verifier_missing the method ran but nothing could verify the result
//	correction       the owner named the intent: accepted the reading at the
//	                 confirm box (how=confirm) or picked one from the P2 menu
//	                 (how=menu). Chosen = the intent — a labelled example
//	                 (text → intent) for `jazz forge`.
//
// brRun stays pure: the outcome carries MissWhy/NoMethod/Confirmed/Sub and
// the CALLERS (brHeadless, brInteractive, brRunReading, the two level-0
// gates) flush it. The offline eval and the tests call brRun directly, so
// they never touch the owner's ledger; on top of that emission is off while
// a test binary runs, in --fresh, under JAZZ_SIGNALS=off and while the eval
// runs (sigMuted). Best-effort: an error is swallowed, the answer never
// waits on the ledger. The text stays in the owner's data dir (secrets
// redacted, one line, 300 chars) and is never sent anywhere.

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
)

// sigMuted silences emission: the eval runs the brain on fixtures that must
// not land in the owner's ledger.
var sigMuted atomic.Bool

// sigTestOn re-enables emission inside a test binary (signals tests only,
// with sigPath on a temp dir).
var sigTestOn atomic.Bool

// jzUnderTest reports whether this process is a `go test` binary. The
// testing package registers -test.v before any test runs; checking the flag
// keeps the testing package out of the real binary.
func jzUnderTest() bool { return flag.Lookup("test.v") != nil }

func sigEnabled() bool {
	if sigMuted.Load() || pfFresh() || olOffWord(os.Getenv("JAZZ_SIGNALS")) {
		return false
	}
	return !jzUnderTest() || sigTestOn.Load()
}

// sigRawExtra are the Extra keys whose values are machine tokens the forge
// needs verbatim (enum, number, session id, "pos:intent" menu): they never
// quote the request, so they skip redactSecrets. Every OTHER extra value is
// request-derived (evidence quotes slot values and typo repairs) and gets
// redacted like the text — a secret typed as a slot must not land in the
// ledger in clear.
var sigRawExtra = map[string]bool{
	"how": true, "status": true, "where": true, "next": true, "reason": true,
	"intent": true, "confidence": true, "raw": true, "alt": true, "typo_only": true,
	"method": true, "src": true, "sess": true, "pos": true, "menu": true,
}

// sigInlineKV catches "NAME=value" secrets that are not at line start —
// redactSecrets anchors its env-KV pass at line start (right for code, but
// a request is one line of prose where a pasted "API_KEY=…" hides anywhere).
// The prefix is optional so a bare API_KEY=/SECRET= matches too.
var sigInlineKV = lazyRx(`(([A-Z][A-Z0-9_]*)?(?:API_?KEY|TOKEN|SECRET|PASSWORD|PASSWD|ACCESS_KEY|PRIVATE_KEY|_DSN|WEBHOOK_URL|DATABASE_URL)[A-Z0-9_]*[ \t]*=[ \t]*["']?)[^\s"'$#]{8,}`)

// sigRedact redacts for the ledger: the anchored pass on the ORIGINAL text
// (line starts intact — oneLine runs after, in sigOf), then the inline pass
// for mid-sentence "KEY=value".
func sigRedact(s string) string {
	return rdReplaceKeepPH(sigInlineKV.R(), redactSecrets(s), "${1}[REDACTED]")
}

// sigOf builds one signal from a reading. Pure (no I/O): the tests check
// what would be written without a ledger.
func sigOf(kind, text string, in Intent, extra map[string]string) Signal {
	s := Signal{Kind: kind, Class: "intent", Text: oneLine(sigRedact(strings.TrimSpace(text)), 300), Extra: map[string]string{}}
	if in.Name != "" {
		s.Extra["intent"] = in.Name
		s.Extra["confidence"] = fmt.Sprintf("%.2f", in.Confidence)
		if in.Raw > 0 && in.Raw != in.Confidence {
			s.Extra["raw"] = fmt.Sprintf("%.2f", in.Raw)
		}
		if len(in.Alt) > 0 {
			s.Extra["alt"] = strings.Join(in.Alt, ",")
		}
		if len(in.Evidence) > 0 {
			s.Extra["evidence"] = oneLine(sigRedact(strings.Join(in.Evidence, " | ")), 200)
		}
		if in.TypoOnly {
			s.Extra["typo_only"] = "true"
		}
	}
	for k, v := range extra {
		if v != "" {
			if !sigRawExtra[k] {
				v = sigRedact(v)
			}
			s.Extra[k] = v
		}
	}
	if len(s.Extra) == 0 {
		s.Extra = nil
	}
	return s
}

// brSignals turns a brain outcome into the signals it implies (a multi
// request: one set per part). A verified success the brain acted on by
// itself is not a signal: nothing to learn.
func brSignals(text string, r brResult) []Signal {
	if len(r.Sub) > 0 {
		var out []Signal
		for _, p := range r.Sub {
			out = append(out, brSignals(p.Req, p)...)
		}
		return out
	}
	if r.Req != "" {
		text = r.Req
	}
	var out []Signal
	switch {
	case r.MissWhy == "not_understood":
		out = append(out, sigOf("unknown_intent", text, r.Intent, nil))
	case r.MissWhy != "":
		out = append(out, sigOf("miss", text, r.Intent, map[string]string{"reason": r.MissWhy}))
	case r.NoMethod:
		out = append(out, sigOf("new_method", text, r.Intent, nil))
	}
	if r.Handled && r.Status == tkUnverified {
		out = append(out, sigOf("verifier_missing", text, r.Intent, map[string]string{"method": r.Method}))
	}
	if r.Confirmed != nil && r.Handled {
		s := sigOf("correction", text, *r.Confirmed, map[string]string{"how": "confirm", "status": string(r.Status)})
		s.Chosen, s.Verified = r.Intent.Name, r.Status == tkSucceeded
		out = append(out, s)
	}
	return out
}

// sigFlush writes the signals of r (extra is merged into each one).
func sigFlush(text string, r brResult, extra map[string]string) {
	if !sigEnabled() {
		return
	}
	text = nlCredScrub(text) // uma senha sudo digitada nunca vai pro ledger (consent.go)
	for _, s := range brSignals(text, r) {
		for k, v := range extra {
			if s.Extra == nil {
				s.Extra = map[string]string{}
			}
			s.Extra[k] = v
		}
		_ = sigAppend(s)
	}
}

// sigPickSignals: the owner picked a reading from the P2 menu (orig = what
// the brain had read). The pick is the strong label; a confirm box the
// picked reading may still raise (mutants always ask) is the same decision,
// so it does not count twice. extra is the pick's provenance (src=tty,
// sess, pos, menu — set by the interactive caller only): the forge only
// learns from a human pick at the terminal, never from piped input.
func sigPickSignals(text string, orig *Intent, r brResult, extra map[string]string) []Signal {
	r.Confirmed = nil
	out := brSignals(text, r)
	read := Intent{}
	if orig != nil {
		read = *orig
	}
	s := sigOf("correction", text, read, map[string]string{"how": "menu", "status": string(r.Status)})
	s.Chosen, s.Verified = r.Intent.Name, r.Handled && r.Status == tkSucceeded
	for k, v := range extra {
		if v != "" {
			s.Extra[k] = v
		}
	}
	return append(out, s)
}

func sigPick(text string, orig *Intent, r brResult, extra map[string]string) {
	if !sigEnabled() {
		return
	}
	for _, s := range sigPickSignals(text, orig, r, extra) {
		_ = sigAppend(s)
	}
}

// sigMenuSkips: the readings the menu offered and the owner did NOT pick —
// Joachims 2005: skipped options are weak negatives. menu is the pick's
// provenance ("pos:intent,..."); chosen is the intent he picked ("" when the
// pick was edit/contextual — every brain reading was rejected, the "nenhuma
// destas" case). The forge treats a later correction to a skipped reading as
// a contradiction (spec 0.14b item 6).
func sigMenuSkips(text, menu, chosen string, extra map[string]string) {
	if !sigEnabled() || menu == "" || text == "" {
		return
	}
	for _, it := range strings.Split(menu, ",") {
		i := strings.IndexByte(it, ':')
		if i < 0 {
			continue
		}
		intent := it[i+1:]
		if intent == "" || intent == chosen {
			continue
		}
		s := sigOf("menu_skip", text, Intent{}, nil)
		s.Chosen = intent // the reading the owner saw and rejected
		for k, v := range extra {
			if v != "" {
				s.Extra[k] = v
			}
		}
		_ = sigAppend(s)
	}
}

// brLevel0Reason classifies a request the level-0 gate handed to the model:
// not understood (unknown_intent), understood but under the act threshold
// (miss/below_act), or a known intent outside the level-0 set (new_method).
func brLevel0Reason(in Intent) (kind string, extra map[string]string) {
	switch {
	case !in.Understood():
		return "unknown_intent", map[string]string{"where": "level0", "next": "model"}
	case in.Confidence < brActMin(in):
		return "miss", map[string]string{"where": "level0", "next": "model", "reason": "below_act"}
	default:
		return "new_method", map[string]string{"where": "level0", "next": "model", "reason": "not_level0"}
	}
}

func brLevel0Signal(prompt string, in Intent) {
	if !sigEnabled() {
		return
	}
	kind, extra := brLevel0Reason(in)
	_ = sigAppend(sigOf(kind, prompt, in, extra))
}

// brLevel0Extra tags what the level-0 gate ran: where, and whether the
// model took the request after all.
func brLevel0Extra(ok bool) map[string]string {
	if ok {
		return map[string]string{"where": "level0"}
	}
	return map[string]string{"where": "level0", "next": "model"}
}
