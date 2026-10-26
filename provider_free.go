package main

// provider_free.go: documented free-tier limits (metadata only) and the
// providers added to the free pool. Owner: providers.
//
// Every FreeLimits entry cites the docs page it came from and the date it was
// read. A zero field means the docs do not say: nothing is guessed. These
// numbers feed `jazz omni status` and docs/FREE-AI.md; the enforcement values
// the router uses live in ProviderConf.Limits (omni.go).

// FreeLimits describes what a provider documents for its free tier.
type FreeLimits struct {
	RPM     int    `json:"rpm,omitempty"`
	RPD     int    `json:"rpd,omitempty"`
	TPM     int    `json:"tpm,omitempty"`
	TPD     int    `json:"tpd,omitempty"`
	Monthly int    `json:"monthly,omitempty"`
	Note    string `json:"note,omitempty"`    // PT-BR: what the free tier is, and what is uncertain
	Source  string `json:"source,omitempty"`  // docs page the numbers came from
	Checked string `json:"checked,omitempty"` // YYYY-MM-DD the page was read
}

const pvFreeChecked = "2026-10-26"

// pvFreeLimits is keyed by provider id. Providers absent here have no verified numbers.
var pvFreeLimits = map[string]*FreeLimits{
	"groq": {RPM: 30, RPD: 1000, TPM: 8000, TPD: 200000, Checked: pvFreeChecked,
		Source: "https://console.groq.com/docs/rate-limits",
		Note:   "valores da tabela 'Developer plan' para gpt-oss-120b/20b e qwen3.8-27b; a aba do plano grátis não pôde ser lida (incerto)"},
	"cerebras": {RPM: 5, TPM: 30000, TPD: 1000000, Checked: pvFreeChecked,
		Source: "https://inference-docs.cerebras.ai/support/rate-limits",
		Note:   "Free Trial = US$5 de crédito que expira em 30 dias (não é cota recorrente); gpt-oss-120b e qwen-3.8-27b; RPD não publicado"},
	"sambanova": {RPM: 20, RPD: 20, TPD: 200000, Checked: pvFreeChecked,
		Source: "https://docs.sambanova.ai/docs/en/models/rate-limits",
		Note:   "vale sem cartão vinculado; DeepSeek-V3.1, Meta-Llama-3.3-70B-Instruct, gpt-oss-120b (por modelo)"},
	"openrouter": {RPM: 20, RPD: 50, Checked: pvFreeChecked,
		Source: "https://openrouter.ai/docs/api-reference/limits",
		Note:   "modelos :free; 50/dia com menos de US$10 comprados, 1000/dia com US$10 ou mais"},
	"cloudflare": {Checked: pvFreeChecked,
		Source: "https://developers.cloudflare.com/workers-ai/platform/pricing/",
		Note:   "10 mil neurons/dia grátis, reset 00:00 UTC (neurons, não pedidos)"},
	"gemini": {Checked: pvFreeChecked,
		Source: "https://ai.google.dev/gemini-api/docs/rate-limits",
		Note:   "a doc não publica números; veja no AI Studio (aistudio.google.com/rate-limit). RPD zera à meia-noite do Pacífico"},
	"mistral": {Checked: pvFreeChecked, Source: "https://docs.mistral.ai/getting-started/models/",
		Note: "a doc não publica limites do plano Experiment (não verificado)"},
	"ollama-cloud": {Checked: pvFreeChecked, Source: "https://docs.ollama.com/cloud",
		Note: "a doc remete aos limites da sua conta em ollama.com/settings/usage"},
	"nvidia": {Checked: pvFreeChecked, Source: "https://docs.api.nvidia.com/nim/docs/api-quickstart",
		Note: "limite por minuto não publicado na doc lida (não verificado)"},
	"kilo": {Checked: pvFreeChecked, Source: "https://kilo.ai/docs/gateway",
		Note: "a doc lida não cita a cota anônima; o 429 PROMOTION_MODEL_LIMIT_REACHED indica promoção por modelo (não verificado)"},
	"huggingface": {Checked: pvFreeChecked, Source: "https://huggingface.co/docs/inference-providers/pricing",
		Note: "usuário grátis NÃO tem crédito mensal (só comprando); PRO ganha US$2/mês. Não entra na rotação grátis"},
}

// pvRetired lists services that used to be free and were shut down; the wizard
// and docs mention them so nobody goes looking.
var pvRetired = map[string]string{
	"github-models": "GitHub Models foi encerrado em 30/07/2026 (https://docs.github.com/en/github-models) — não há endpoint nem `gh auth token` que sirva",
}

// omniPresets is omniPresetsBase plus the free-limit metadata and the providers
// added for the free pool.
func omniPresets() map[string]*ProviderConf {
	m := omniPresetsBase()
	// Hugging Face router: OpenAI-compatible at https://router.huggingface.co/v1,
	// Bearer HF token (docs: huggingface.co/docs/inference-providers/pricing).
	// Free accounts get no credits, so this is NOT free: used only under omni.paid.
	m["huggingface"] = &ProviderConf{Type: "openai", BaseURL: "https://router.huggingface.co/v1", KeyEnv: "HF_TOKEN",
		DefaultModel: "deepseek-ai/DeepSeek-V3-0324", Signup: "https://huggingface.co/settings/tokens",
		KeyHint: "começa com hf_", KeyRE: `^hf_`, Title: "Hugging Face (crédito pago; PRO US$2/mês)"}
	for k, fl := range pvFreeLimits {
		if pc := m[k]; pc != nil {
			pc.FreeLimits = fl
		}
	}
	return m
}

func init() {
	pvAltKeyEnv["HF_TOKEN"] = []string{"HUGGING_FACE_HUB_TOKEN", "HUGGINGFACE_API_KEY"}
	pvSetupOrder = append(pvSetupOrder, "huggingface")
	pvSetupNotes["huggingface"] = "sem crédito grátis para contas free (PRO ganha US$2/mês): só entra em emergência pago"
}
