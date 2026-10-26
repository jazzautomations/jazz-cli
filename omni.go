package main

// omni.go: free-provider presets and the omni router. Owner: providers.
// Contract surface (CONTRACT.md): (*Config).Candidates. Additions for the router
// (used by agent-core's chatWithFallback and platform's `jazz omni`):
// Need, (*Config).CandidatesFor, OmniStatus, OmniSetupSteps, OmniProbeKey,
// OmniRefresh, OmniBeginTurn, OmniExhaustedMessage, OmniPrivacyNotice.
//
// The router never touches System or the tool specs: their bytes stay identical
// across providers. It only reorders which concrete "provider/model" refs to try.

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// OmniProvider is the virtual provider name: "omni/coder", "omni/fast"…
const OmniProvider = "omni"

// omniTiers are the aliases: coder (agent main loop, default), smart (best reasoning),
// fast (titles, short summaries, small subagent queries), free (everything free),
// long (compaction and very large contexts).
var omniTiers = []string{"coder", "smart", "fast", "free", "long"}

func isOmniTier(t string) bool {
	for _, x := range omniTiers {
		if x == t {
			return true
		}
	}
	return false
}

func isOmniRef(ref string) bool {
	p, _ := SplitModel(ref)
	return p == OmniProvider
}

// OmniPrivacyNotice is shown once (cfg.Omni.PrivacyAck) before routing to free tiers.
const OmniPrivacyNotice = "Aviso de privacidade: vários planos grátis (Gemini, Mistral Experiment, OpenRouter/Kilo :free, NVIDIA) " +
	"podem registrar seus prompts ou usá-los para treinar modelos. Não mande dados pessoais, segredos ou código confidencial " +
	"por eles. Para evitar esses provedores: /omni notrain (omni.no_training = true)."

// Need describes one request for the router.
type Need struct {
	Tokens int    // estimated prompt + max output tokens (0 = unknown)
	Tools  bool   // the request carries native tool specs
	Role   string // main | compact | fast | sub
	Prefer string // sticky ref (last pick) for prompt-cache affinity
}

// ---------- presets ----------

func pvLim(l Limits) *Limits { return &l }

// Kilo presets: Context/MaxOut (context_length / top_provider.max_completion_tokens)
// read from https://api.kilo.ai/api/gateway/models on 2026-10-20; MaxOut is the
// ceiling of the output raise after a cutoff (cutoff.go).

// kiloDefaultModel is the keyless default. Measured 2026-10-19: it answers in
// ~2 s with native tool calls, while kilo-auto/free often stalls for minutes.
const kiloDefaultModel = "poolside/laguna-s-2.1:free"

// pvOldKiloDefault was the keyless default before 2026-10; saved configs that
// still carry it are migrated to kiloDefaultModel (it stays selectable with -m).
const pvOldKiloDefault = "kilo-auto/free"

// kiloFastFree are the other free Kilo models that answered with native tool
// calls in 1-2 s (2026-10-19); they follow the default in the implicit chain.
var kiloFastFree = []string{
	"minimax/minimax-m3.1-flash:free",
	"cohere/north-mini-code:free",
	"nvidia/nemotron-3-super-120b-a12b:free",
	"nvidia/nemotron-3.5-lightning:free",
}

// pvManualOnly refs are never picked automatically (omni tiers, implicit chain);
// they still work when the user names them (-m, model, fallback, omni.tiers).
var pvManualOnly = map[string]bool{"kilo/" + pvOldKiloDefault: true}

// omniPresets are the free (and cheap emergency) providers known in 2026-10.
func omniPresetsBase() map[string]*ProviderConf {
	return map[string]*ProviderConf{
		"kilo": {Type: "openai", BaseURL: "https://api.kilo.ai/api/gateway", KeyEnv: "KILO_API_KEY", DefaultModel: kiloDefaultModel,
			NoKey: true, Free: true, Train: true, Router: "kilo", FreeIDs: true, Probe: "chat1",
			Limits: pvLim(Limits{RPH: 200, Scope: "key"}), Signup: "https://app.kilo.ai", Title: "Kilo Gateway"},
		"openrouter": {Type: "openai", BaseURL: "https://openrouter.ai/api/v1", KeyEnv: "OPENROUTER_API_KEY", DefaultModel: "nvidia/nemotron-3-ultra-550b-a55b:free",
			Free: true, Train: true, Router: "openrouter", FreeIDs: true,
			Limits: pvLim(Limits{RPM: 20, RPD: 50, Scope: "key"}), Signup: "https://openrouter.ai/settings/keys",
			KeyHint: "começa com sk-or-", KeyRE: `^sk-or-`, Title: "OpenRouter"},
		"gemini": {Type: "gemini", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai", KeyEnv: "GEMINI_API_KEY", DefaultModel: "gemini-3.8-flash",
			Free: true, Train: true, Quirks: []string{"gemini-thought-sig", "effort"},
			Limits: pvLim(Limits{Reset: "PT"}), Signup: "https://aistudio.google.com/apikey",
			KeyHint: "começa com AIza", KeyRE: `^AIza`, Title: "Google AI Studio (Gemini)"},
		"groq": {Type: "openai", BaseURL: "https://api.groq.com/openai/v1", KeyEnv: "GROQ_API_KEY", DefaultModel: "openai/gpt-oss-120b",
			Free: true, Quirks: []string{"effort"},
			Limits: pvLim(Limits{RPM: 30, RPD: 1000, TPM: 8000, TPD: 200000, MaxReq: 7500}), Signup: "https://console.groq.com/keys",
			KeyHint: "começa com gsk_", KeyRE: `^gsk_`, Title: "Groq"},
		"nvidia": {Type: "openai", BaseURL: "https://integrate.api.nvidia.com/v1", KeyEnv: "NVIDIA_API_KEY", DefaultModel: "z-ai/glm-5.3",
			Free: true, Train: true, Probe: "chat1",
			Limits: pvLim(Limits{RPM: 40, Scope: "key"}), Signup: "https://build.nvidia.com/settings/api-keys",
			KeyHint: "começa com nvapi-", KeyRE: `^nvapi-`, Title: "NVIDIA API Catalog"},
		"mistral": {Type: "openai", BaseURL: "https://api.mistral.ai/v1", KeyEnv: "MISTRAL_API_KEY", DefaultModel: "devstral-medium-latest",
			Free: true, Train: true, Quirks: []string{"mistral-ids9"},
			Limits: pvLim(Limits{RPM: 60}), Signup: "https://console.mistral.ai/api-keys", Title: "Mistral (Experiment)"},
		"cloudflare": {Type: "openai", BaseURL: "https://api.cloudflare.com/client/v4/accounts/{ACCOUNT}/ai/v1", KeyEnv: "CLOUDFLARE_API_TOKEN",
			DefaultModel: "@cf/moonshotai/kimi-k2.5", Free: true, Account: true, AccountEnv: "CLOUDFLARE_ACCOUNT_ID",
			Probe:  "https://api.cloudflare.com/client/v4/accounts/{ACCOUNT}/ai/models/search?per_page=1",
			Limits: pvLim(Limits{Scope: "key"}), Signup: "https://dash.cloudflare.com/profile/api-tokens", Title: "Cloudflare Workers AI"},
		"sambanova": {Type: "openai", BaseURL: "https://api.sambanova.ai/v1", KeyEnv: "SAMBANOVA_API_KEY", DefaultModel: "DeepSeek-V3.1",
			Free: true, Probe: "chat1",
			Limits: pvLim(Limits{RPM: 20, RPD: 20, TPD: 200000}), Signup: "https://cloud.sambanova.ai/apis", Title: "SambaNova Cloud"},
		"zai": {Type: "openai", BaseURL: "https://api.z.ai/api/paas/v4", KeyEnv: "ZAI_API_KEY", DefaultModel: "glm-4.7-flash",
			Free: true, Limits: pvLim(Limits{Conc: 1, Scope: "key"}), Signup: "https://z.ai/manage-apikey/apikey-list", Title: "Z.ai (GLM Flash)"},
		"cohere": {Type: "openai", BaseURL: "https://api.cohere.ai/compatibility/v1", KeyEnv: "CO_API_KEY", DefaultModel: "north-mini-code",
			Free: true, Quirks: []string{"cohere-effort"}, Probe: "https://api.cohere.com/v1/models",
			Limits: pvLim(Limits{RPM: 20, Monthly: 1000, Scope: "key"}), Signup: "https://dashboard.cohere.com/api-keys", Title: "Cohere (trial)"},
		"ollama-cloud": {Type: "openai", BaseURL: "https://ollama.com/v1", KeyEnv: "OLLAMA_API_KEY", DefaultModel: "glm-5.3-flash",
			Free: true, Probe: "chat1", Limits: pvLim(Limits{Conc: 1, Scope: "key"}), Signup: "https://ollama.com/settings/keys", Title: "Ollama Cloud"},
		"vercel": {Type: "openai", BaseURL: "https://ai-gateway.vercel.sh/v1", KeyEnv: "AI_GATEWAY_API_KEY", DefaultModel: "poolside/laguna-s-2.1-free",
			Free: true, FreeIDs: true, Signup: "https://vercel.com/dashboard", Title: "Vercel AI Gateway"},
		"codestral": {Type: "openai", BaseURL: "https://codestral.mistral.ai/v1", KeyEnv: "CODESTRAL_API_KEY", DefaultModel: "codestral-latest",
			Free: true, Probe: "chat1", Limits: pvLim(Limits{RPM: 30, RPD: 2000, Scope: "key"}), Signup: "https://console.mistral.ai/codestral", Title: "Mistral Codestral"},
		"cerebras": {Type: "openai", BaseURL: "https://api.cerebras.ai/v1", KeyEnv: "CEREBRAS_API_KEY", DefaultModel: "gpt-oss-120b",
			Card: true, Quirks: []string{"effort"}, Limits: pvLim(Limits{RPM: 5, TPM: 30000, TPD: 1000000}),
			Signup: "https://cloud.cerebras.ai", KeyHint: "começa com csk-", KeyRE: `^csk-`, Title: "Cerebras (crédito com cartão)"},
		"deepseek": {Type: "openai", BaseURL: "https://api.deepseek.com", KeyEnv: "DEEPSEEK_API_KEY", DefaultModel: "deepseek-flash",
			Quirks: []string{"deepseek-reasoning"}, Signup: "https://platform.deepseek.com/api_keys", KeyHint: "começa com sk-", Title: "DeepSeek (pago, barato)"},
		// OpenCode Zen's free models answer 403 FreeTierError to any other client; only paid models work, with a key.
		"zen": {Type: "openai", BaseURL: "https://opencode.ai/zen/v1", KeyEnv: "OPENCODE_API_KEY", DefaultModel: "deepseek-v4.1-flash",
			Quirks: []string{"deepseek-reasoning"}, Signup: "https://opencode.ai/auth", Title: "OpenCode Zen (pago)"},
	}
}

type presetModel struct {
	ref string
	mc  ModelConf
}

func pvTiers(t ...string) []string { return t }

// presetModels: Q is a prior (to be calibrated by an eval); tiers decide the aliases.
var presetModels = []presetModel{
	{"nvidia/z-ai/glm-5.3", ModelConf{Q: 86, Tiers: pvTiers("coder", "smart")}},
	{"nvidia/moonshotai/kimi-k2.6", ModelConf{Q: 85, Tiers: pvTiers("coder", "smart")}},
	{"gemini/gemini-3.8-flash", ModelConf{Q: 84, Context: 1048576, Tiers: pvTiers("smart", "coder", "long"), Limits: pvLim(Limits{RPD: 20, Reset: "PT"})}},
	{"gemini/gemini-3.6-flash", ModelConf{Q: 80, Context: 1048576, Tiers: pvTiers("smart", "coder", "long"), Limits: pvLim(Limits{RPD: 20, Reset: "PT"})}},
	{"gemini/gemini-2.5-pro", ModelConf{Q: 78, Context: 1048576, Tiers: pvTiers("smart"), Limits: pvLim(Limits{RPD: 50, Reset: "PT"})}},
	{"gemini/gemini-3.5-flash-lite", ModelConf{Q: 65, Context: 1048576, Tiers: pvTiers("fast", "long", "free"), Limits: pvLim(Limits{RPD: 500, Reset: "PT"})}},
	{"nvidia/deepseek-ai/deepseek-v4.1-flash", ModelConf{Q: 80, Tiers: pvTiers("coder", "smart", "fast")}},
	{"cloudflare/@cf/moonshotai/kimi-k2.5", ModelConf{Q: 80, Tiers: pvTiers("coder")}},
	{"mistral/devstral-medium-latest", ModelConf{Q: 78, Tiers: pvTiers("coder")}},
	{"openrouter/nvidia/nemotron-3-ultra-550b-a55b:free", ModelConf{Q: 75, Context: 1000000, Tiers: pvTiers("smart", "coder", "free", "long")}},
	{"openrouter/minimax/minimax-m3.1-flash:free", ModelConf{Q: 68, Context: 1000000, Tiers: pvTiers("free", "long")}},
	{"openrouter/cohere/north-mini-code:free", ModelConf{Q: 70, Context: 256000, Tiers: pvTiers("coder", "free")}},
	{"openrouter/nvidia/nemotron-3.5-lightning:free", ModelConf{Q: 55, Tiers: pvTiers("fast", "free")}},
	{"sambanova/DeepSeek-V3.1", ModelConf{Q: 74, Context: 131072, Tiers: pvTiers("coder", "free")}},
	{"sambanova/gpt-oss-120b", ModelConf{Q: 66, Context: 131072, Tiers: pvTiers("fast", "free")}},
	{"kilo/" + kiloDefaultModel, ModelConf{Q: 72, MaxOut: 32768, Tiers: pvTiers("coder", "free")}},
	{"kilo/minimax/minimax-m3.1-flash:free", ModelConf{Q: 66, Context: 1000000, MaxOut: 524288, Tiers: pvTiers("free", "fast", "long")}},
	{"kilo/cohere/north-mini-code:free", ModelConf{Q: 70, Context: 256000, MaxOut: 64000, Tiers: pvTiers("coder", "free")}},
	{"kilo/nvidia/nemotron-3-super-120b-a12b:free", ModelConf{Q: 68, MaxOut: 235929, Tiers: pvTiers("coder", "free")}},
	{"kilo/nvidia/nemotron-3.5-lightning:free", ModelConf{Q: 55, MaxOut: 65536, Tiers: pvTiers("fast", "free")}},
	{"kilo/" + pvOldKiloDefault, ModelConf{Q: 60, Context: 256000}}, // manual only: stalls often
	{"zai/glm-4.7-flash", ModelConf{Q: 70, Tiers: pvTiers("coder", "free")}},
	{"groq/openai/gpt-oss-120b", ModelConf{Q: 66, Context: 131072, MaxReq: 7500, Tiers: pvTiers("fast")}},
	{"groq/qwen/qwen3.8-27b", ModelConf{Q: 62, Context: 131072, MaxReq: 7500, MaxTokens: 16384, Tiers: pvTiers("fast")}},
	{"groq/openai/gpt-oss-20b", ModelConf{Q: 52, Context: 131072, MaxReq: 7500, Tiers: pvTiers("fast")}},
	{"ollama-cloud/glm-5.3-flash", ModelConf{Q: 74, Tiers: pvTiers("coder")}},
	{"cohere/north-mini-code", ModelConf{Q: 70, Context: 256000, Tiers: pvTiers("coder")}},
	{"vercel/poolside/laguna-s-2.1-free", ModelConf{Q: 60, Tiers: pvTiers("free")}},
	{"codestral/codestral-latest", ModelConf{Q: 45, Tiers: pvTiers("fast")}},
	{"cerebras/gpt-oss-120b", ModelConf{Q: 66, Context: 65536, MaxReq: 28000, Tiers: pvTiers("fast")}},
	// paid / subscription: only in an emergency under the default policy
	{"chatgpt/gpt-5.5-codex", ModelConf{Q: 92, Tiers: pvTiers("coder", "smart")}},
	{"anthropic/claude-sonnet-5-5", ModelConf{Q: 94, Context: 200000, Tiers: pvTiers("coder", "smart")}},
	{"deepseek/deepseek-flash", ModelConf{Q: 82, Context: 1000000, Tiers: pvTiers("coder", "smart", "long")}},
}

func presetModelMap() map[string]*ModelConf {
	m := make(map[string]*ModelConf, len(presetModels))
	for _, p := range presetModels {
		mc := p.mc
		m[p.ref] = &mc
	}
	return m
}

// ---------- classification helpers ----------

// isFreeRef: a recurring free quota or a local server.
func (c *Config) isFreeRef(ref string) bool {
	prov, model := SplitModel(ref)
	pc := c.Providers[prov]
	if pc == nil {
		return false
	}
	return pc.Local || (pc.Free && (!pc.FreeIDs || pvFreeID(model)))
}

func (c *Config) isSubRef(ref string) bool {
	prov, _ := SplitModel(ref)
	pc := c.Providers[prov]
	return pc != nil && pc.Sub
}

func pvGlobMatch(pat, s string) bool {
	if pat == s {
		return true
	}
	ok, err := path.Match(pat, s)
	if err == nil && ok {
		return true
	}
	// "*" in path.Match stops at '/': also try a slash-blind version.
	if strings.Contains(pat, "*") {
		re := "^" + strings.ReplaceAll(regexp.QuoteMeta(pat), `\*`, `.*`) + "$"
		if m, err := regexp.MatchString(re, s); err == nil && m {
			return true
		}
	}
	return false
}

func (c *Config) omniExcluded(ref string) bool {
	prov, _ := SplitModel(ref)
	for _, g := range c.Omni.Exclude {
		if pvGlobMatch(g, prov) || pvGlobMatch(g, ref) {
			return true
		}
	}
	return false
}

// omniPool lists every ref the router may consider: Models entries (user + preset),
// each provider's default model, and free tool-capable models discovered in the
// OpenRouter/Kilo catalogs (ctx ≥ 64k; Q 50; ≥ 500k also joins long).
func (c *Config) omniPool() map[string]ModelConf {
	pool := map[string]ModelConf{}
	for ref := range c.Models {
		if !isOmniRef(ref) {
			pool[ref] = c.ModelSettings(ref)
		}
	}
	for name, pc := range c.Providers {
		if pc == nil || pc.DefaultModel == "" {
			continue
		}
		ref := name + "/" + pc.DefaultModel
		if _, ok := pool[ref]; !ok {
			ms := c.ModelSettings(ref)
			if ms.Q == 0 {
				ms.Q = 50
				if pc.Local {
					ms.Q = 45
				}
			}
			pool[ref] = ms
		}
	}
	for _, prov := range []string{"openrouter", "kilo"} {
		if c.Providers[prov] == nil {
			continue
		}
		for ref, cm := range pvCachedRefs(prov) {
			if _, ok := pool[ref]; ok || !cm.Free || cm.Tools != "native" || cm.Ctx < 65536 {
				continue
			}
			ms := ModelConf{Q: 50, Context: cm.Ctx, Tiers: []string{"free"}}
			if cm.Ctx >= 500000 {
				ms.Tiers = append(ms.Tiers, "long")
			}
			pool[ref] = ms
		}
	}
	return pool
}

// ---------- the router ----------

var (
	omniSeedMu sync.Mutex
	omniSeed   = uint64(time.Now().UnixNano())
)

// OmniBeginTurn re-seeds the weighted strategy. Call once per turn so a turn
// never hops between providers request by request.
func OmniBeginTurn() {
	omniSeedMu.Lock()
	omniSeed = rand.Uint64()
	omniSeedMu.Unlock()
}

type omniCand struct {
	ref   string
	q     int
	score float64
	st    rlState
	free  bool
	sub   bool
	text  bool
	price float64
	order int // position in a user tier list
}

// Candidates returns the ordered, de-duplicated model refs to try for one request:
// the given refs first (sticky turn model, then the primary), then cfg.Fallback,
// then the implicit free chain / omni routing. Exhausted refs (ExhaustedUntil)
// are skipped unless every candidate is exhausted. Never returns an empty slice
// when refs is non-empty.
func (c *Config) Candidates(refs ...string) []string {
	return c.CandidatesFor(Need{}, refs...)
}

// CandidatesFor is Candidates for a known request size/shape. Omni aliases
// ("omni/coder"…) expand into concrete refs ranked by the strategy; concrete refs
// pass through (minus dead ones). With omni enabled and no alias in the list, the
// omni/free chain is appended; without omni, a free primary with no fallback gets
// the implicit free chain (kilo, groq when keyed, reachable local servers).
func (c *Config) CandidatesFor(n Need, refs ...string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(r string) {
		if r != "" && !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	hasOmni := false
	primary := ""
	for _, r := range append(append([]string{}, refs...), c.Fallback...) {
		if r == "" {
			continue
		}
		if primary == "" {
			primary = r
		}
		if isOmniRef(r) {
			hasOmni = true
			_, tier := SplitModel(r)
			if tier == "" {
				tier = "coder"
			}
			for _, x := range c.omniRank(tier, n) {
				add(x)
			}
			continue
		}
		add(r)
	}
	if c.Omni.Enabled && !hasOmni {
		for _, x := range c.omniRank("free", n) {
			add(x)
		}
	} else if !c.Omni.Enabled && len(c.Fallback) == 0 && primary != "" && !isOmniRef(primary) && c.pvIsFreeish(primary) {
		for _, x := range c.implicitFreeChain() {
			add(x)
		}
	}
	// drop dead concrete refs (quota, refused key, gone) unless all are dead
	var live []string
	for _, r := range out {
		if st := c.pvState(r, 0); !st.Dead {
			live = append(live, r)
		}
	}
	if len(live) > 0 {
		return live
	}
	return out
}

func (c *Config) pvIsFreeish(ref string) bool {
	prov, model := SplitModel(ref)
	return c.isFreeRef(ref) || pvFreeID(model) || prov == "zen" || prov == "kilo"
}

// implicitFreeChain (G4): kilo's keyless default and its fast free models, groq
// when it has a key, reachable local servers.
func (c *Config) implicitFreeChain() []string {
	var out []string
	if pc := c.Providers["kilo"]; pc != nil && pc.DefaultModel != "" && !c.omniExcluded("kilo") {
		out = append(out, "kilo/"+pc.DefaultModel)
		for _, m := range kiloFastFree {
			if r := "kilo/" + m; m != pc.DefaultModel && !c.omniExcluded(r) {
				out = append(out, r)
			}
		}
	}
	if pc := c.Providers["groq"]; pc != nil && pc.DefaultModel != "" && c.HasCreds("groq") {
		out = append(out, "groq/"+pc.DefaultModel)
	}
	for _, name := range c.ProviderNames() {
		if pc := c.Providers[name]; pc != nil && pc.Local && pc.DefaultModel != "" && c.HasCreds(name) {
			out = append(out, name+"/"+pc.DefaultModel)
		}
	}
	return out
}

// omniRank expands one alias into concrete refs (at most 12), best first.
func (c *Config) omniRank(tier string, n Need) []string {
	cands, dead := c.omniCollect(tier, n)
	if len(cands) == 0 {
		// everything dead: still return something (earliest reset first) so the
		// caller gets a real error instead of an empty chain
		sort.SliceStable(dead, func(i, j int) bool { return dead[i].st.Until.Before(dead[j].st.Until) })
		var out []string
		for _, d := range dead {
			out = append(out, d.ref)
		}
		return pvCap(out, 4)
	}
	strategy := c.Omni.Strategy
	if n.Role == "sub" && strategy == "" {
		strategy = "weighted"
	}
	paid := c.Omni.Paid
	if paid == "" {
		paid = "emergency"
	}
	userOrder := len(c.Omni.Tiers[tier]) > 0
	var out []string
	switch strategy {
	case "ordered":
		sort.SliceStable(cands, func(i, j int) bool {
			if userOrder {
				return cands[i].order < cands[j].order
			}
			return cands[i].q > cands[j].q
		})
		var later []string
		for _, x := range cands {
			if x.st.Ready {
				out = append(out, x.ref)
			} else {
				later = append(later, x.ref)
			}
		}
		out = append(out, later...)
	case "weighted":
		out = c.omniWeighted(cands, paid)
	default: // free-first, cheapest
		out = c.omniFreeFirst(cands, paid, strategy == "cheapest")
	}
	return pvCap(out, 12)
}

func pvCap(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// omniCollect filters the pool for a tier and attaches limiter state.
func (c *Config) omniCollect(tier string, n Need) (live, dead []omniCand) {
	pool := c.omniPool()
	override := c.Omni.Tiers[tier]
	creds := map[string]bool{}
	hasCreds := func(p string) bool {
		v, ok := creds[p]
		if !ok {
			v = c.HasCreds(p)
			creds[p] = v
		}
		return v
	}
	lastProv, _ := SplitModel(rlLastOK())
	var refs []string
	order := map[string]int{}
	if len(override) > 0 {
		all := make([]string, 0, len(pool))
		for r := range pool {
			all = append(all, r)
		}
		sort.Strings(all)
		for i, g := range override {
			if !strings.ContainsAny(g, "*?[") {
				if _, ok := order[g]; !ok {
					order[g] = i
					refs = append(refs, g)
				}
				continue
			}
			for _, r := range all {
				if _, ok := order[r]; !ok && pvGlobMatch(g, r) {
					order[r] = i
					refs = append(refs, r)
				}
			}
		}
	} else {
		for r, ms := range pool {
			if pvManualOnly[r] {
				continue
			}
			if pvHas(ms.Tiers, tier) || (tier == "free" && c.isFreeRef(r)) {
				refs = append(refs, r)
			}
		}
		sort.Strings(refs)
	}
	paid := c.Omni.Paid
	for _, ref := range refs {
		prov, _ := SplitModel(ref)
		pc := c.Providers[prov]
		if pc == nil || isOmniRef(ref) || !hasCreds(prov) || c.omniExcluded(ref) {
			continue
		}
		if c.Omni.NoTraining && pc.Train {
			continue
		}
		free, sub := c.isFreeRef(ref), c.isSubRef(ref)
		if tier == "free" && !free {
			continue
		}
		if paid == "never" && !free && !sub {
			continue
		}
		ms, ok := pool[ref]
		if !ok {
			ms = c.ModelSettings(ref)
		}
		if n.Tokens > 0 {
			if win, _ := c.Limits(ref); win > 0 && win < n.Tokens {
				continue
			}
		}
		if n.Tools && ms.Tools == "none" {
			continue
		}
		cd := omniCand{ref: ref, q: ms.Q, free: free, sub: sub, text: n.Tools && ms.Tools == "text", order: order[ref]}
		if cd.q == 0 {
			cd.q = 50
		}
		cd.st = c.pvState(ref, n.Tokens)
		if cd.st.TooBig {
			continue
		}
		if in, out, ok := c.Price(ref); ok {
			cd.price = in*3 + out // rough blended price per token
		} else if !free && !sub {
			cd.price = 1 // unknown paid: after known cheap ones
		}
		strikes, _, _ := rlInfo(ref)
		cd.score = float64(cd.q)
		if ref == n.Prefer {
			cd.score += 10
		}
		if lastProv != "" && prov == lastProv {
			cd.score += 5
		}
		if strikes > 0 {
			cd.score -= 15
		}
		if cd.st.Frac < 0.1 {
			cd.score -= 10
		}
		if cd.st.Dead {
			dead = append(dead, cd)
			continue
		}
		live = append(live, cd)
	}
	return live, dead
}

// omniFreeFirst: Ready free by score, then free waiting ≤ 20 s by wait, then the
// ChatGPT subscription, then paid Ready by price, then everything else by wait.
// Text-tools candidates come after every native one.
func (c *Config) omniFreeFirst(cands []omniCand, paid string, cheapest bool) []string {
	const shortWait = 20 * time.Second
	groupOf := func(x omniCand) int {
		g := 5
		switch {
		case x.st.Ready && (x.free || (paid == "always" && !x.sub)):
			g = 0
		case !x.st.Ready && x.free && x.st.Wait <= shortWait:
			g = 1
		case x.st.Ready && x.sub:
			g = 2
		case x.st.Ready:
			g = 3
		}
		if x.text {
			g += 10
		}
		return g
	}
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		ga, gb := groupOf(a), groupOf(b)
		if ga != gb {
			return ga < gb
		}
		switch ga % 10 {
		case 0:
			if cheapest {
				if a.price != b.price {
					return a.price < b.price
				}
				return a.st.Frac > b.st.Frac
			}
			return a.score > b.score
		case 1, 5:
			if a.st.Wait != b.st.Wait {
				return a.st.Wait < b.st.Wait
			}
			return a.score > b.score
		case 3:
			if a.price != b.price {
				return a.price < b.price
			}
			return a.score > b.score
		}
		return a.score > b.score
	})
	out := make([]string, 0, len(cands))
	for _, x := range cands {
		out = append(out, x.ref)
	}
	return out
}

// omniWeighted spreads load: weighted random among Ready candidates with
// Q ≥ bestQ−15, weight = Q · provider weight · remaining fraction; seeded per turn.
func (c *Config) omniWeighted(cands []omniCand, paid string) []string {
	rest := c.omniFreeFirst(append([]omniCand{}, cands...), paid, false)
	best := 0
	for _, x := range cands {
		if x.st.Ready && !x.text && (x.free || paid == "always") && x.q > best {
			best = x.q
		}
	}
	type wc struct {
		ref string
		w   float64
	}
	var pool []wc
	for _, x := range cands {
		if !x.st.Ready || x.text || !(x.free || paid == "always") || x.q < best-15 {
			continue
		}
		prov, _ := SplitModel(x.ref)
		w := float64(x.q)
		if pc := c.Providers[prov]; pc != nil && pc.Weight > 0 {
			w *= float64(pc.Weight)
		}
		w *= max(x.st.Frac, 0.05)
		pool = append(pool, wc{x.ref, w})
	}
	sort.Slice(pool, func(i, j int) bool { return pool[i].ref < pool[j].ref })
	omniSeedMu.Lock()
	r := rand.New(rand.NewPCG(omniSeed, 0x6a617a7a))
	omniSeedMu.Unlock()
	var out []string
	seen := map[string]bool{}
	for len(pool) > 0 {
		total := 0.0
		for _, p := range pool {
			total += p.w
		}
		x := r.Float64() * total
		i := 0
		for ; i < len(pool)-1; i++ {
			x -= pool[i].w
			if x < 0 {
				break
			}
		}
		out = append(out, pool[i].ref)
		seen[pool[i].ref] = true
		pool = append(pool[:i], pool[i+1:]...)
	}
	for _, ref := range rest {
		if !seen[ref] {
			out = append(out, ref)
		}
	}
	return out
}

// OmniExhaustedMessage explains (PT-BR) when every candidate of an alias is dead,
// listing the earliest reset per provider. "" when something is usable.
func OmniExhaustedMessage(cfg *Config, ref string) string {
	tier := "coder"
	if isOmniRef(ref) {
		if _, t := SplitModel(ref); t != "" {
			tier = t
		}
	}
	live, dead := cfg.omniCollect(tier, Need{})
	if len(live) > 0 {
		return ""
	}
	if len(dead) == 0 {
		return "nenhum provedor configurado para omni/" + tier + " — rode: jazz omni setup"
	}
	first := map[string]time.Time{}
	for _, d := range dead {
		p, _ := SplitModel(d.ref)
		if t, ok := first[p]; !ok || d.st.Until.Before(t) {
			first[p] = d.st.Until
		}
	}
	var names []string
	for p := range first {
		names = append(names, p)
	}
	sort.Slice(names, func(i, j int) bool { return first[names[i]].Before(first[names[j]]) })
	var parts []string
	for _, p := range names {
		parts = append(parts, p+" "+pvWhen(first[p]))
	}
	return "todos os provedores grátis esgotados — libera: " + strings.Join(parts, ", ") + "; use /omni para detalhes"
}

// pvWhen formats a reset time: "em 3m" when close, else local "21:00" (or "dd/mm 21:00").
func pvWhen(t time.Time) string {
	d := time.Until(t)
	switch {
	case d <= 0:
		return "agora"
	case d < time.Hour:
		return "em " + d.Round(time.Minute).String()
	case d > 20*365*24*time.Hour:
		return "nunca (ação manual)"
	case d < 24*time.Hour:
		return t.Local().Format("15:04")
	}
	return t.Local().Format("02/01 15:04")
}

// ---------- status / setup (for `jazz omni`, /omni and /model) ----------

// OmniRow is one line of the omni status table.
type OmniRow struct {
	Provider string
	Model    string
	Title    string
	HasKey   bool
	Free     bool
	Paid     bool
	State    string // pronto | aguardando 12s | esgotado até 21:00 | credencial recusada | sem chave | …
	Budget   string // "37/50 hoje", "120/1000 no mês", "?" when unknown
	LastErr  string
	Q        int
	Tiers    []string
}

// OmniStatus returns one row per provider default model and per tiered model of
// a provider (keyed or not), sorted by the setup order.
func OmniStatus(cfg *Config) []OmniRow {
	order := map[string]int{}
	for i, s := range pvSetupOrder {
		order[s] = i + 1
	}
	pool := cfg.omniPool()
	var rows []OmniRow
	for ref, ms := range pool {
		prov, model := SplitModel(ref)
		pc := cfg.Providers[prov]
		if pc == nil {
			continue
		}
		if len(ms.Tiers) == 0 && ref != prov+"/"+pc.DefaultModel {
			continue
		}
		r := OmniRow{Provider: prov, Model: model, Title: pc.Title, Free: cfg.isFreeRef(ref), Q: ms.Q, Tiers: ms.Tiers}
		r.Paid = !r.Free && !pc.Sub
		r.HasKey = cfg.HasCreds(prov)
		st := cfg.pvState(ref, 0)
		switch {
		case strings.Contains(pc.BaseURL, "models.github.ai"):
			r.State = "retirado em 30/07/2026"
		case !r.HasKey && pc.Local:
			r.State = "fora do ar"
		case !r.HasKey:
			r.State = "sem chave"
		case st.Dead && strings.HasPrefix(st.Why, "credencial"):
			r.State = "credencial recusada"
		case st.Dead:
			r.State = "esgotado até " + pvWhen(st.Until)
		case !st.Ready:
			r.State = fmt.Sprintf("aguardando %s", st.Wait.Round(time.Second))
		default:
			r.State = "pronto"
		}
		r.Budget = cfg.rlBudget(ref)
		_, r.LastErr, _ = rlInfo(ref)
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool {
		oi, oj := order[rows[i].Provider], order[rows[j].Provider]
		if oi == 0 {
			oi = 1000
		}
		if oj == 0 {
			oj = 1000
		}
		if oi != oj {
			return oi < oj
		}
		if rows[i].Provider != rows[j].Provider {
			return rows[i].Provider < rows[j].Provider
		}
		if rows[i].Q != rows[j].Q {
			return rows[i].Q > rows[j].Q
		}
		return rows[i].Model < rows[j].Model
	})
	return rows
}

// FormatOmniStatus renders OmniStatus as a plain PT-BR table (no colours).
func FormatOmniStatus(rows []OmniRow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-13s %-44s %-5s %-24s %s\n", "provedor", "modelo", "chave", "estado", "cota")
	for _, r := range rows {
		key := "não"
		if r.HasKey {
			key = "sim"
		}
		m := r.Model
		if len(m) > 44 {
			m = m[:43] + "…"
		}
		fmt.Fprintf(&b, "%-13s %-44s %-5s %-24s %s\n", r.Provider, m, key, r.State, r.Budget)
		if r.LastErr != "" {
			fmt.Fprintf(&b, "%13s └ último erro: %s\n", "", r.LastErr)
		}
	}
	return b.String()
}

// OmniStep is one step of the `jazz omni setup` wizard.
type OmniStep struct {
	Name       string // provider id
	Title      string
	Signup     string // where to create the key
	KeyEnv     string
	Hint       string // key format hint (PT-BR)
	Note       string // what the free tier gives (PT-BR)
	NoKey      bool   // works without a key
	Account    bool   // also needs an account id (AccountEnv)
	AccountEnv string
	Free       bool
	Card       bool // needs a payment card
	Train      bool // free tier may train on prompts
	Done       bool // credentials already present
}

var pvSetupOrder = []string{"kilo", "openrouter", "gemini", "groq", "nvidia", "mistral", "sambanova", "cloudflare", "zai",
	"cohere", "ollama-cloud", "vercel", "codestral", "cerebras", "deepseek", "chatgpt", "anthropic", "openai", "zen",
	"ollama", "llamacpp", "lmstudio"}

var pvSetupNotes = map[string]string{
	"kilo":         "sem chave: 200 pedidos/hora por IP nos modelos :free (qualidade varia; não mande dados sensíveis)",
	"openrouter":   "50 pedidos/dia grátis (1000/dia depois de comprar US$10 uma única vez); 20 por minuto",
	"gemini":       "grátis sem cartão; limite por projeto (flash ~20/dia, flash-lite ~500/dia); o Google usa os dados para treino",
	"groq":         "1000 pedidos/dia por modelo, mas só ~7,5 mil tokens por pedido (8k por minuto): ótimo para tarefas rápidas",
	"nvidia":       "~40 pedidos/min; GLM-5.3, Kimi K2.6, DeepSeek V4.1; o uso é registrado pela NVIDIA",
	"mistral":      "plano Experiment: precisa de telefone e aceita treino com seus dados; ~1 pedido/s",
	"sambanova":    "20 pedidos/dia por modelo (5 modelos); sem cartão",
	"cloudflare":   "10 mil neurons/dia; precisa também do Account ID (CLOUDFLARE_ACCOUNT_ID)",
	"zai":          "GLM-4.7-Flash grátis; 1 pedido por vez; use chave da API padrão (não a do Coding Plan)",
	"cohere":       "chave trial: 1000 chamadas por mês, só para avaliação",
	"ollama-cloud": "cota inicial pequena; 1 pedido por vez; não treina com seus dados",
	"vercel":       "crédito mensal só para modelos -free; nunca compre créditos nessa conta ou o grátis acaba",
	"codestral":    "focado em completar código; fraco como agente",
	"cerebras":     "crédito único de US$5 depois de cadastrar cartão; expira em 30 dias",
	"deepseek":     "pago e barato (uso de emergência): ~US$0,15/0,60 por milhão de tokens",
	"chatgpt":      "use seu plano do ChatGPT: jazz login chatgpt",
	"anthropic":    "pago (sem plano grátis)",
	"openai":       "pago (sem plano grátis)",
	"zen":          "os modelos grátis do Zen só funcionam dentro do OpenCode; aqui só os pagos, com chave",
	"ollama":       "servidor local (ou na sua rede): sem limite, sem chave",
	"llamacpp":     "servidor local llama.cpp (llama-server): sem limite, sem chave",
	"lmstudio":     "servidor local do LM Studio: sem limite, sem chave",
}

// OmniSetupSteps returns the wizard steps in the order worth doing them.
func OmniSetupSteps(cfg *Config) []OmniStep {
	var out []OmniStep
	seen := map[string]bool{}
	addStep := func(name string) {
		pc := cfg.Providers[name]
		if pc == nil || seen[name] {
			return
		}
		seen[name] = true
		title := pc.Title
		if title == "" {
			title = name
		}
		out = append(out, OmniStep{Name: name, Title: title, Signup: pc.Signup, KeyEnv: pc.KeyEnv, Hint: pc.KeyHint,
			Note: pvSetupNotes[name], NoKey: pc.NoKey, Account: pc.Account, AccountEnv: pc.AccountEnv,
			Free: pc.Free || pc.Local, Card: pc.Card, Train: pc.Train, Done: cfg.HasCreds(name)})
	}
	for _, n := range pvSetupOrder {
		addStep(n)
	}
	for _, n := range cfg.ProviderNames() {
		addStep(n)
	}
	return out
}

// KeyFormatWarning returns a PT-BR warning when key does not look like the provider's keys.
func KeyFormatWarning(cfg *Config, prov, key string) string {
	pc := cfg.Providers[prov]
	if pc == nil || pc.KeyRE == "" {
		return ""
	}
	re, err := regexp.Compile(pc.KeyRE)
	if err != nil || re.MatchString(strings.TrimSpace(key)) {
		return ""
	}
	hint := pc.KeyHint
	if hint == "" {
		hint = "formato diferente do esperado"
	}
	return fmt.Sprintf("essa chave não parece de %s (%s) — confira se colou a chave certa", prov, hint)
}

// OmniProbeKey checks a provider's credentials without spending much quota:
// GET /models (default), a 1-token chat ("chat1", for providers whose /models is
// public), or an absolute probe URL. A 429 means the key works but is limited.
func OmniProbeKey(ctx context.Context, cfg *Config, prov string) error {
	pc := cfg.Providers[prov]
	if pc == nil {
		return fmt.Errorf("provedor desconhecido %q", prov)
	}
	if pc.Type == "codex" {
		_, err := chatgptCred(ctx, false)
		return err
	}
	if pc.Local {
		ok, _, err := ProbeProvider(ctx, cfg, prov)
		if err == nil && !ok {
			err = fmt.Errorf("%s não respondeu em %s", prov, pc.BaseURL)
		}
		return err
	}
	key := cfg.APIKey(prov)
	if key == "" && !pc.NoKey {
		return fmt.Errorf("sem chave para %s (%s)", prov, pc.KeyEnv)
	}
	if pc.Account && cfg.AccountID(prov) == "" {
		return fmt.Errorf("%s precisa do id da conta (%s)", prov, pc.AccountEnv)
	}
	h := map[string]string{}
	if key != "" {
		if pc.Type == "anthropic" {
			h["x-api-key"], h["anthropic-version"] = key, "2023-06-01"
		} else {
			h["Authorization"] = "Bearer " + key
		}
	}
	base := strings.TrimRight(strings.ReplaceAll(pc.BaseURL, "{ACCOUNT}", cfg.AccountID(prov)), "/")
	var err error
	switch {
	case strings.HasPrefix(pc.Probe, "http"):
		_, err = pvGetJSON(ctx, strings.ReplaceAll(pc.Probe, "{ACCOUNT}", cfg.AccountID(prov)), h, nil, 15*time.Second)
	case pc.Probe == "chat1":
		body := map[string]any{"model": pc.DefaultModel, "max_tokens": 1, "stream": false,
			"messages": []map[string]any{{"role": "user", "content": "hi"}}}
		err = pvPostJSON(ctx, base+"/chat/completions", h, body, nil, 30*time.Second)
	case pc.Type == "anthropic":
		_, err = pvGetJSON(ctx, base+"/v1/models?limit=1", h, nil, 15*time.Second)
	default:
		_, err = pvGetJSON(ctx, base+"/models", h, nil, 15*time.Second)
	}
	var ae *APIError
	if errors.As(err, &ae) {
		switch {
		case ae.Status == 429:
			return nil
		case ae.Status == 401 || ae.Status == 403:
			return fmt.Errorf("%s recusou a chave (HTTP %d) — gere outra em %s", prov, ae.Status, pc.Signup)
		}
	}
	if err == nil {
		ResetRateLimits(prov) // a new working key clears an old "credencial recusada"
	}
	return err
}

var (
	pvRefreshMu sync.Mutex
	pvORQuotaAt time.Time
)

// OmniRefresh updates the free catalogs (OpenRouter, Kilo; at most daily) and
// OpenRouter's exact daily free-request count (GET /key; at most every 30 min).
// Safe to run in the background at startup.
func OmniRefresh(ctx context.Context, cfg *Config) {
	for _, prov := range []string{"openrouter", "kilo"} {
		if cfg.Providers[prov] == nil || !cfg.HasCreds(prov) {
			continue
		}
		if time.Since(pvListedAt(prov)) > 24*time.Hour {
			_, _ = ListModels(ctx, cfg, prov)
		}
	}
	pvRefreshORQuota(ctx, cfg)
}

func pvRefreshORQuota(ctx context.Context, cfg *Config) {
	pc := cfg.Providers["openrouter"]
	key := cfg.APIKey("openrouter")
	if pc == nil || key == "" {
		return
	}
	pvRefreshMu.Lock()
	if time.Since(pvORQuotaAt) < 30*time.Minute {
		pvRefreshMu.Unlock()
		return
	}
	pvORQuotaAt = time.Now()
	pvRefreshMu.Unlock()
	var r struct {
		Data struct {
			Free *struct {
				Used, Limit, Remaining int
			} `json:"free_model_daily_requests"`
			IsFreeTier *bool `json:"is_free_tier"`
		} `json:"data"`
		Free *struct {
			Used, Limit, Remaining int
		} `json:"free_model_daily_requests"`
	}
	if _, err := pvGetJSON(ctx, strings.TrimRight(pc.BaseURL, "/")+"/key", map[string]string{"Authorization": "Bearer " + key}, &r, 15*time.Second); err != nil {
		return
	}
	f := r.Data.Free
	if f == nil {
		f = r.Free
	}
	switch {
	case f != nil && f.Limit > 0:
		cfg.rlSetDaily("openrouter/x:free", f.Used, f.Limit)
	case r.Data.IsFreeTier != nil && !*r.Data.IsFreeTier:
		cfg.rlSetDaily("openrouter/x:free", -1, 1000) // bought credits once: 1000/day
	}
}
