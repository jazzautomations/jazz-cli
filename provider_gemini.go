package main

// provider_gemini.go: provider type "gemini" (Google AI Studio). Owner: providers.
//
// Decision: jazz talks to Gemini through Google's OpenAI-compatible endpoint
// (https://generativelanguage.googleapis.com/v1beta/openai), not the native
// generateContent API. The compat layer streams, supports function calling
// (tools / tool_choice), image_url data URLs and reasoning_effort
// (minimal|low|medium|high), so the whole OpenAIProvider path (rate limiter,
// header learning, history sanitation) is reused and the binary stays small.
// The two Gemini-specific details are handled here and in provider_openai.go:
//   - Gemini 3 function calling returns a thought signature per tool call in
//     tool_calls[].extra_content.google.thought_signature; it must be sent back
//     on the next request or the API answers 400. The OpenAI stream parser keeps
//     extra_content per call id in Message.Extra ("tc_extra") and the serializer
//     echoes it, only to the provider that produced it.
//   - /models returns ids as "models/gemini-…"; ListModels and Chat strip the prefix.
// Limits are per Google Cloud project and the daily quota resets at midnight
// Pacific time (Limits.Reset = "PT"); a 429 whose quotaId contains "PerDay" is a
// daily exhaustion (pvIsDaily), and error.details[].retryDelay feeds RetryAfter.

// pvGeminiSetup configures an OpenAIProvider for Gemini's compat endpoint.
func pvGeminiSetup(p *OpenAIProvider) {
	if p.BaseURL == "" {
		p.BaseURL = "https://generativelanguage.googleapis.com/v1beta/openai"
	}
	for _, q := range []string{"gemini-thought-sig", "effort"} {
		if !pvHas(p.Quirks, q) {
			p.Quirks = append(append([]string{}, p.Quirks...), q)
		}
	}
}
