package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// omniHealthProbe sends one tiny real request to every free model in the
// catalog (presetModels, tier "free") and classifies it. Free ids rotate
// often — providers remove or rename them (laguna/minimax/nemotron-super all
// 404'd in 2026-11) — so this is how a human (`jazz omni health`) or CI learns
// the catalog rotted before users hit it. Returns the report and the count of
// models that HAD credentials but answered 404 (a removed id: the CI signal).
// Keyed providers without a key are skipped (not configured, not a failure).
func omniHealthProbe(cfg *Config, timeout time.Duration) (string, int) {
	type res struct {
		ref, status string
		ms          int64
	}
	var rows []res
	dead := 0
	pres := omniPresets()
	for _, pm := range presetModels {
		if !acHasTier(pm.mc.Tiers, "free") {
			continue
		}
		prov, model := SplitModel(pm.ref)
		pc := pres[prov]
		if pc == nil {
			continue
		}
		if !pc.NoKey && pc.Type != "codex" && strings.TrimSpace(cfg.APIKey(prov)) == "" {
			rows = append(rows, res{pm.ref, "— sem chave (pulado)", 0})
			continue
		}
		p, err := cfg.NewProvider(prov)
		if err != nil {
			rows = append(rows, res{pm.ref, "? erro de provider: " + oneLine(err.Error(), 40), 0})
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		t0 := time.Now()
		_, err = p.Chat(ctx, ChatRequest{Model: model, MaxTokens: 8,
			Messages: []Message{{Role: "user", Content: "Responda apenas: ok"}}}, nil)
		ms := time.Since(t0).Milliseconds()
		cancel()
		kind, label := omniHealthKind(err)
		rows = append(rows, res{pm.ref, label, ms})
		if kind == "dead" {
			dead++
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ref < rows[j].ref })
	var b strings.Builder
	fmt.Fprintf(&b, "jazz omni health · %d modelos free no catálogo\n", len(rows))
	for _, r := range rows {
		fmt.Fprintf(&b, "  %-48s %s", r.ref, r.status)
		if r.ms > 0 {
			fmt.Fprintf(&b, " (%dms)", r.ms)
		}
		b.WriteByte('\n')
	}
	if dead > 0 {
		fmt.Fprintf(&b, "\n%d modelo(s) free MORTO(s) (404) — atualize presetModels/kiloFastFree em omni.go\n", dead)
	} else {
		b.WriteString("\nnenhum modelo free morto (404) entre os que têm credencial\n")
	}
	return b.String(), dead
}

// omniHealthKind reads the provider's answer like a human would — status AND
// body — and names the cause. Order matters: a bad key is checked before a
// dead model, because Gemini answers a bad key with HTTP 400 "Please pass a
// valid API key" and other providers say "model" in auth errors; marking a
// live model dead because the key expired would rot the catalog for nothing.
// kinds: alive | key | credit | dead | quota | down | other.
func omniHealthKind(err error) (kind, label string) {
	if err == nil {
		return "alive", "✓ vivo"
	}
	var ae *APIError
	if !errors.As(err, &ae) {
		return "down", "? rede/timeout: " + oneLine(err.Error(), 50)
	}
	b := strings.ToLower(ae.Body)
	snip := oneLine(strings.TrimSpace(ae.Body), 70)
	has := func(ks ...string) bool {
		for _, k := range ks {
			if strings.Contains(b, k) {
				return true
			}
		}
		return false
	}
	switch {
	case ae.Status == 401 || ae.Status == 403 ||
		has("api key", "api_key", "apikey", "invalid key", "valid api key", "credential", "unauthorized", "unauthenticated", "authentication", "invalid_api", "permission denied"):
		return "key", fmt.Sprintf("✗ %d chave recusada/expirada", ae.Status)
	case pvClassify(ae) == "credit":
		return "credit", fmt.Sprintf("✗ %d sem crédito/saldo", ae.Status)
	case ae.Status == 404 ||
		has("not_found", "not found", "does not exist", "model:", "unknown model", "no such model", "decommissioned", "deprecated", "no longer available", "no longer supported", "has been retired", "invalid model", "model_not_found"):
		return "dead", fmt.Sprintf("✗ %d MORTO/descontinuado — %s", ae.Status, snip)
	case ae.Status == 429:
		return "quota", "· 429 cota/rate (vivo, só limitado)"
	case ae.Status >= 500 || ae.Status == 0:
		return "down", fmt.Sprintf("? %d provedor/rede fora (transitório)", ae.Status)
	case ae.Status == 400:
		return "other", "? 400 pedido recusado — " + snip
	}
	return "other", fmt.Sprintf("? %d — %s", ae.Status, snip)
}
