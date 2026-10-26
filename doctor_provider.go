package main

// doctor_provider.go: `jazz doctor --provider <name>[/model]` sends one tiny
// real request through the same provider code the agent uses (headers,
// redaction, streaming) and prints where the key came from, the HTTP status
// and the provider's error body with secrets removed, so a user can tell a bad
// key, an empty balance, a wrong model id or a policy refusal apart without logs.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// pfKeySource names where cfg.APIKey(prov) found the key ("" = none).
func pfKeySource(cfg *Config, prov string) string {
	pc := cfg.Providers[prov]
	if pc == nil {
		return ""
	}
	if pc.KeyEnv != "" {
		if strings.TrimSpace(os.Getenv(pc.KeyEnv)) != "" {
			return "variável " + pc.KeyEnv
		}
		for _, alt := range pvAltKeyEnv[pc.KeyEnv] {
			if strings.TrimSpace(os.Getenv(alt)) != "" {
				return "variável " + alt
			}
		}
	}
	if cr := LoadAuth()[prov]; cr != nil && cr.Type == "api" && cr.Key != "" {
		return authPath()
	}
	return ""
}

// pfDoctorProvider runs the check; the exit code is 0 only when the provider answered.
func pfDoctorProvider(cfg *Config, arg string, w io.Writer) int {
	ref, err := cfg.ResolveModel(strings.TrimSpace(arg))
	if err != nil || isOmniRef(ref) {
		if err == nil {
			err = errors.New("omni é um roteador: teste um provedor concreto (ex.: anthropic)")
		}
		fmt.Fprintln(w, "  ✗", err)
		return 2
	}
	prov, model := SplitModel(ref)
	pc := cfg.Providers[prov]
	fmt.Fprintf(w, "\n  provedor %s (%s) · %s\n  modelo   %s\n", prov, pc.Type, strings.ReplaceAll(pc.BaseURL, "{ACCOUNT}", "…"), model)
	if pc.Type != "codex" && !pc.NoKey {
		src := pfKeySource(cfg, prov)
		if src == "" {
			fmt.Fprintf(w, "  ✗ sem chave: rode jazz key %s (ou exporte %s)\n\n", prov, pc.KeyEnv)
			return 1
		}
		key := cfg.APIKey(prov)
		fmt.Fprintf(w, "  chave    %s (de %s)\n", pfMaskKey(key), src)
		if wn := KeyFormatWarning(cfg, prov, key); wn != "" {
			fmt.Fprintln(w, "  aviso:", wn)
		}
		if st := cfg.pvState(ref, 0); st.Dead {
			fmt.Fprintf(w, "  aviso: o jazz marcou %s como indisponível (%s); um teste bem-sucedido aqui limpa a marca\n", ref, st.Why)
		}
	}
	p, err := cfg.NewProvider(prov)
	if err != nil {
		fmt.Fprintln(w, "  ✗", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	t0 := time.Now()
	resp, err := p.Chat(ctx, ChatRequest{Model: model, MaxTokens: 32,
		Messages: []Message{{Role: "user", Content: "Responda apenas: ok"}}}, nil)
	ms := time.Since(t0).Milliseconds()
	var ae *APIError
	switch {
	case err == nil && resp.Refused():
		why := resp.Refusal
		if why == "" {
			why = firstSentence(resp.Text)
		}
		fmt.Fprintf(w, "  ✗ HTTP 200 em %d ms, mas o modelo recusou (stop_reason %q): %s\n", ms, resp.Stop, redactSecrets(why))
		fmt.Fprintln(w, "    a conexão e a chave funcionam; o recusado foi o conteúdo. Reformule ou troque de modelo (/model).")
		return 1
	case err == nil:
		fmt.Fprintf(w, "  ✓ HTTP 200 em %d ms · stop %s · %d+%d tokens · resposta: %s\n\n", ms, resp.Stop, resp.Usage.In, resp.Usage.Out, oneLine(redactSecrets(resp.Text), 80))
		ResetRateLimits(prov) // a working key clears an old "credencial recusada"
		return 0
	case errors.As(err, &ae):
		fmt.Fprintf(w, "  ✗ HTTP %d em %d ms\n", ae.Status, ms)
		fmt.Fprintf(w, "    corpo: %s\n", oneLine(redactSecrets(ae.Body), 600))
		if h := pfDoctorHint(ae); h != "" {
			fmt.Fprintln(w, "    →", h)
		}
	default:
		fmt.Fprintf(w, "  ✗ falhou em %d ms: %s\n", ms, oneLine(redactSecrets(err.Error()), 600))
	}
	fmt.Fprintln(w)
	return 1
}

// pfDoctorHint turns the common failures into the next step.
func pfDoctorHint(ae *APIError) string {
	b := strings.ToLower(ae.Body)
	switch {
	case ae.Status == 401 || ae.Status == 403:
		return "chave recusada: gere outra no painel do provedor e rode jazz key <provedor>; confira se uma variável de ambiente antiga não está sobrepondo a chave salva"
	case pvClassify(ae) == "credit":
		return "sem saldo/crédito nesse provedor"
	case ae.Status == 404 || strings.Contains(b, "not_found") || strings.Contains(b, "model:"):
		return "modelo inexistente ou sem acesso nesta conta: veja jazz models <provedor>"
	case ae.Status == 429:
		return "limite de requisições: espere ou troque de modelo"
	case ae.Status == 400:
		return "pedido recusado pelo provedor (veja o corpo acima)"
	case ae.Status >= 500 || ae.Status == 0:
		return "falha do lado do provedor ou da rede: tente de novo"
	}
	return ""
}
