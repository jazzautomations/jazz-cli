package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ModelInfo struct {
	ID      string
	Ctx     int
	Free    bool
	Tools   bool
	Display string
	MaxOut  int     // max output tokens when the catalog says
	Images  *bool   // accepts image input (nil = unknown)
	In, Out float64 // USD per token (0 = free/unknown)
}

func ListModels(ctx context.Context, cfg *Config, provider string) ([]ModelInfo, error) {
	pc, ok := cfg.Providers[provider]
	if !ok {
		return nil, fmt.Errorf("provedor desconhecido %q", provider)
	}
	base := strings.TrimRight(pc.BaseURL, "/")
	if strings.Contains(base, "{ACCOUNT}") {
		id := cfg.AccountID(provider)
		if id == "" {
			return nil, fmt.Errorf("%s precisa do id da conta (%s)", provider, pc.AccountEnv)
		}
		base = strings.ReplaceAll(base, "{ACCOUNT}", id)
	}
	switch pc.Type {
	case "anthropic":
		key := cfg.APIKey(provider)
		if key == "" {
			return nil, fmt.Errorf("sem chave para %s", provider)
		}
		var r struct {
			Data []struct {
				ID          string `json:"id"`
				DisplayName string `json:"display_name"`
			} `json:"data"`
		}
		if err := getJSON(ctx, base+"/v1/models?limit=100", map[string]string{"x-api-key": key, "anthropic-version": "2023-06-01"}, &r); err != nil {
			return nil, err
		}
		var out []ModelInfo
		yes := true
		for _, m := range r.Data {
			out = append(out, ModelInfo{ID: m.ID, Tools: true, Display: m.DisplayName, Ctx: 200000, Images: &yes})
		}
		pvCacheStoreList(provider, out)
		return out, nil
	case "codex":
		cr, err := chatgptCred(ctx, false)
		if err != nil {
			return nil, err
		}
		var r struct {
			Models []struct {
				Slug        string `json:"slug"`
				DisplayName string `json:"display_name"`
				Context     int    `json:"context_window"`
			} `json:"models"`
		}
		h := map[string]string{"Authorization": "Bearer " + cr.Access, "ChatGPT-Account-ID": cr.AccountID, "originator": originator}
		if err := getJSON(ctx, base+"/models?client_version="+version, h, &r); err != nil {
			return nil, err
		}
		var out []ModelInfo
		for _, m := range r.Models {
			out = append(out, ModelInfo{ID: m.Slug, Tools: true, Display: m.DisplayName, Ctx: m.Context})
		}
		pvCacheStoreList(provider, out)
		return out, nil
	}
	h := map[string]string{}
	if k := cfg.APIKey(provider); k != "" {
		h["Authorization"] = "Bearer " + k
	}
	for k, v := range pc.Headers {
		h[k] = expandEnv(v)
	}
	var r struct {
		Data   []pvCatalogModel `json:"data"`
		Models []pvCatalogModel `json:"models"` // Gemini native / some gateways
	}
	if err := getJSON(ctx, base+"/models", h, &r); err != nil {
		return nil, err
	}
	all := append(r.Data, r.Models...)
	out := make([]ModelInfo, 0, len(all))
	for _, m := range all {
		out = append(out, m.info())
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Free != out[j].Free {
			return out[i].Free
		}
		return out[i].ID < out[j].ID
	})
	pvCacheStoreList(provider, out)
	return out, nil
}

// pvCatalogModel is the union of the /models shapes we meet: OpenRouter/Kilo
// (context_length, pricing, supported_parameters, architecture), Groq
// (context_window), LM Studio (max_context_length), Gemini ("models/…" ids).
type pvCatalogModel struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	CtxLen    int      `json:"context_length"`
	CtxWin    int      `json:"context_window"`
	MaxCtx    int      `json:"max_context_length"`
	InTok     int      `json:"inputTokenLimit"`
	OutTok    int      `json:"outputTokenLimit"`
	MaxOut    int      `json:"max_completion_tokens"`
	Supported []string `json:"supported_parameters"`
	Features  []string `json:"supported_features"`
	Pricing   *struct {
		Prompt     any `json:"prompt"`
		Completion any `json:"completion"`
	} `json:"pricing"`
	Arch *struct {
		InputModalities []string `json:"input_modalities"`
	} `json:"architecture"`
	Top *struct {
		MaxCompletion int `json:"max_completion_tokens"`
		CtxLen        int `json:"context_length"`
	} `json:"top_provider"`
}

func (m pvCatalogModel) info() ModelInfo {
	id := strings.TrimPrefix(m.ID, "models/")
	if id == "" {
		id = strings.TrimPrefix(m.Name, "models/")
	}
	mi := ModelInfo{ID: id, Display: m.Name, Tools: true}
	mi.Ctx = max(m.CtxLen, m.CtxWin, m.MaxCtx, m.InTok)
	if m.Top != nil {
		if mi.Ctx == 0 {
			mi.Ctx = m.Top.CtxLen
		}
		mi.MaxOut = m.Top.MaxCompletion
	}
	if mi.MaxOut == 0 {
		mi.MaxOut = max(m.MaxOut, m.OutTok)
	}
	if m.Supported != nil {
		mi.Tools = pvHas(m.Supported, "tools")
	} else if m.Features != nil {
		mi.Tools = pvHas(m.Features, "tools")
	}
	if m.Arch != nil && m.Arch.InputModalities != nil {
		img := pvHas(m.Arch.InputModalities, "image")
		mi.Images = &img
	}
	priced := false
	if m.Pricing != nil {
		mi.In, mi.Out = pvPrice(m.Pricing.Prompt), pvPrice(m.Pricing.Completion)
		priced = m.Pricing.Prompt != nil
		mi.Free = priced && mi.In == 0 && mi.Out == 0
	}
	if pvFreeID(id) {
		mi.Free = true
	}
	return mi
}

func pvPrice(v any) float64 {
	switch x := v.(type) {
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	case float64:
		return x
	}
	return 0
}

func pvHas(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ---------- models cache (dataDir()/models-cache.json, 7-day TTL) ----------

type pvCachedModel struct {
	Ctx        int     `json:"ctx,omitempty"`
	MaxOut     int     `json:"max_out,omitempty"`
	Tools      string  `json:"tools,omitempty"` // native | text | none
	Images     *bool   `json:"images,omitempty"`
	In         float64 `json:"in,omitempty"`  // USD per input token
	Out        float64 `json:"out,omitempty"` // USD per output token
	Priced     bool    `json:"priced,omitempty"`
	Free       bool    `json:"free,omitempty"`
	LearnedCtx int     `json:"lctx,omitempty"` // learned from an overflow error
	Warn       string  `json:"warn,omitempty"` // PT-BR warning (Ollama num_ctx)
	At         int64   `json:"at"`             // unix seconds
}

type pvModelsCache struct {
	Models map[string]*pvCachedModel `json:"models"`
	Lists  map[string]int64          `json:"lists,omitempty"` // provider -> unix s of the last full listing
}

var (
	pvCacheMu   sync.Mutex
	pvCachePath string
	pvCache     *pvModelsCache
)

const pvCacheTTL = 7 * 24 * time.Hour

func pvCacheFile() string { return filepath.Join(dataDir(), "models-cache.json") }

func pvCacheLoadLocked() *pvModelsCache {
	p := pvCacheFile()
	if pvCache != nil && pvCachePath == p {
		return pvCache
	}
	pvCachePath = p
	pvCache = &pvModelsCache{Models: map[string]*pvCachedModel{}, Lists: map[string]int64{}}
	if b, err := os.ReadFile(p); err == nil {
		var c pvModelsCache
		if json.Unmarshal(b, &c) == nil {
			if c.Models != nil {
				pvCache.Models = c.Models
			}
			if c.Lists != nil {
				pvCache.Lists = c.Lists
			}
		}
	}
	return pvCache
}

func pvCacheSaveLocked() {
	if pvCache == nil {
		return
	}
	cut := time.Now().Add(-pvCacheTTL).Unix()
	for k, v := range pvCache.Models {
		if v == nil || v.At < cut {
			delete(pvCache.Models, k)
		}
	}
	if err := os.MkdirAll(filepath.Dir(pvCachePath), 0o700); err != nil {
		return
	}
	b, err := json.Marshal(pvCache)
	if err != nil {
		return
	}
	_ = writeFileAtomic(pvCachePath, b, 0o600)
}

// pvCached returns a copy of the fresh cache entry for ref (nil when none).
func pvCached(ref string) *pvCachedModel {
	pvCacheMu.Lock()
	defer pvCacheMu.Unlock()
	c := pvCacheLoadLocked()
	m := c.Models[ref]
	if m == nil || time.Since(time.Unix(m.At, 0)) > pvCacheTTL {
		return nil
	}
	cp := *m
	return &cp
}

// pvCacheUpdate edits (creating) the entry for ref and saves the cache.
func pvCacheUpdate(ref string, f func(m *pvCachedModel)) {
	pvCacheMu.Lock()
	defer pvCacheMu.Unlock()
	c := pvCacheLoadLocked()
	m := c.Models[ref]
	if m == nil || time.Since(time.Unix(m.At, 0)) > pvCacheTTL {
		m = &pvCachedModel{}
		c.Models[ref] = m
	}
	f(m)
	m.At = time.Now().Unix()
	pvCacheSaveLocked()
}

func pvCacheStoreList(provider string, list []ModelInfo) {
	if len(list) == 0 {
		return
	}
	pvCacheMu.Lock()
	defer pvCacheMu.Unlock()
	c := pvCacheLoadLocked()
	now := time.Now().Unix()
	for _, mi := range list {
		ref := provider + "/" + mi.ID
		m := c.Models[ref]
		if m == nil {
			m = &pvCachedModel{}
			c.Models[ref] = m
		}
		if mi.Ctx > 0 {
			m.Ctx = mi.Ctx
		}
		if mi.MaxOut > 0 {
			m.MaxOut = mi.MaxOut
		}
		if m.Tools != "text" { // a learned "text" survives a catalog refresh
			m.Tools = "native"
			if !mi.Tools {
				m.Tools = "text" // no native function calling: usable through text tools
			}
		}
		if mi.Images != nil {
			m.Images = mi.Images
		}
		m.In, m.Out, m.Free = mi.In, mi.Out, mi.Free
		m.Priced = mi.In > 0 || mi.Out > 0 || mi.Free
		m.At = now
	}
	c.Lists[provider] = now
	pvCacheSaveLocked()
}

// pvCacheInvalidate forces a new listing of provider next time (a model vanished).
func pvCacheInvalidate(provider string) {
	pvCacheMu.Lock()
	defer pvCacheMu.Unlock()
	c := pvCacheLoadLocked()
	if _, ok := c.Lists[provider]; ok {
		delete(c.Lists, provider)
		pvCacheSaveLocked()
	}
}

// pvListedAt reports when provider's catalog was last fetched.
func pvListedAt(provider string) time.Time {
	pvCacheMu.Lock()
	defer pvCacheMu.Unlock()
	if at := pvCacheLoadLocked().Lists[provider]; at > 0 {
		return time.Unix(at, 0)
	}
	return time.Time{}
}

// pvCachedRefs lists cached refs of a provider (discovery for omni/free).
func pvCachedRefs(provider string) map[string]pvCachedModel {
	pvCacheMu.Lock()
	defer pvCacheMu.Unlock()
	out := map[string]pvCachedModel{}
	cut := time.Now().Add(-pvCacheTTL).Unix()
	for ref, m := range pvCacheLoadLocked().Models {
		if p, _ := SplitModel(ref); p == provider && m != nil && m.At >= cut {
			out[ref] = *m
		}
	}
	return out
}

func pvLearnCtx(ref string, ctx int) {
	pvCacheUpdate(ref, func(m *pvCachedModel) {
		if m.LearnedCtx == 0 || ctx < m.LearnedCtx {
			m.LearnedCtx = ctx
		}
	})
}

// ---------- per-model settings (G2) ----------

// Limits returns the context window and output cap for a model ref: the Models
// entry (user or preset), then the models cache (catalog / detection), then the
// globals. A window learned from an overflow error lowers it. maxOut is clamped to
// win/4 when win ≤ 32k. An omni alias reports the limits of its first candidate.
func (c *Config) Limits(ref string) (win, maxOut int) {
	if isOmniRef(ref) {
		if cands := c.CandidatesFor(Need{}, ref); len(cands) > 0 && !isOmniRef(cands[0]) {
			return c.Limits(cands[0])
		}
		return c.ContextWindow, c.MaxTokens
	}
	win, maxOut = c.ContextWindow, c.MaxTokens
	cm := pvCached(ref)
	mc := c.Models[ref]
	switch {
	case mc != nil && mc.Context > 0:
		win = mc.Context
	case cm != nil && cm.Ctx > 0:
		win = cm.Ctx
	}
	if cm != nil && cm.LearnedCtx > 0 && cm.LearnedCtx < win {
		win = cm.LearnedCtx
	}
	switch {
	case mc != nil && mc.MaxTokens > 0:
		maxOut = mc.MaxTokens
	case cm != nil && cm.MaxOut > 0 && cm.MaxOut < maxOut:
		maxOut = cm.MaxOut
	}
	if win > 0 && win <= 32768 && maxOut > win/4 {
		maxOut = win / 4
	}
	return win, maxOut
}

// outCeiling is the most output tokens ref accepts, 0 when unknown: the
// models catalog (cached), else the preset/config max_out; max_output_tokens
// from the config lowers it (or sets it when nothing else is known).
func (c *Config) outCeiling(ref string) int {
	if isOmniRef(ref) {
		if cands := c.CandidatesFor(Need{}, ref); len(cands) > 0 && !isOmniRef(cands[0]) {
			return c.outCeiling(cands[0])
		}
		return c.MaxOutputTokens
	}
	ceil := 0
	if cm := pvCached(ref); cm != nil && cm.MaxOut > 0 {
		ceil = cm.MaxOut
	} else if mc := c.Models[ref]; mc != nil && mc.MaxOut > 0 {
		ceil = mc.MaxOut
	}
	if u := c.MaxOutputTokens; u > 0 && (ceil == 0 || u < ceil) {
		ceil = u
	}
	return ceil
}

// BoostedOut is the output budget for one request after a cutoff: twice the
// normal cap, never above the provider's known limit nor half the window.
// Unknown limit: the normal cap (a 400 for a too-large max_tokens would cost
// more than the cut).
func (c *Config) BoostedOut(ref string) int {
	win, base := c.Limits(ref)
	ceil := c.outCeiling(ref)
	if base <= 0 || ceil <= base {
		return base
	}
	b := min(2*base, ceil)
	if win > 0 && b > win/2 {
		b = max(base, win/2)
	}
	return b
}

// ModelSettings returns the effective per-model settings: the Models entry
// (user or preset) completed by the models cache (learned tools mode, window, images).
func (c *Config) ModelSettings(ref string) ModelConf {
	var out ModelConf
	if mc := c.Models[ref]; mc != nil {
		out = *mc
	}
	if cm := pvCached(ref); cm != nil {
		if cm.Tools == "text" && out.Tools != "none" {
			out.Tools = "text"
		}
		if out.Context == 0 && cm.Ctx > 0 {
			out.Context = cm.Ctx
		}
		if out.Images == nil {
			out.Images = cm.Images
		}
	}
	return out
}

// ModelWarning returns a PT-BR warning recorded by window detection (Ollama num_ctx), or "".
func (c *Config) ModelWarning(ref string) string {
	if cm := pvCached(ref); cm != nil {
		return cm.Warn
	}
	return ""
}

// Price returns USD per input/output token for ref when known (models cache, G21).
// Free models are known with price 0.
func (c *Config) Price(ref string) (inPerTok, outPerTok float64, ok bool) {
	if cm := pvCached(ref); cm != nil && cm.Priced {
		return cm.In, cm.Out, true
	}
	if p, m := SplitModel(ref); p != "" {
		if pc := c.Providers[p]; pc != nil && (pc.Local || (pc.Free && (!pc.FreeIDs || pvFreeID(m)))) {
			return 0, 0, true
		}
	}
	if pr, ok := pvKnownPrices[ref]; ok {
		return pr[0] / 1e6, pr[1] / 1e6, true
	}
	return 0, 0, false
}

// pvKnownPrices: USD per million tokens [in, out] for paid presets without a priced catalog.
var pvKnownPrices = map[string][2]float64{
	"deepseek/deepseek-flash":  {0.15, 0.60},
	"deepseek/deepseek-v4-pro": {0.66, 1.98},
}

// SupportsImages reports whether ref accepts image input (unknown → true).
func (c *Config) SupportsImages(ref string) bool {
	if ms := c.ModelSettings(ref); ms.Images != nil {
		return *ms.Images
	}
	return true
}

// MarkTextTools records that ref rejects native tools (persisted in the models cache, G15).
func (c *Config) MarkTextTools(ref string) {
	if ref == "" || isOmniRef(ref) {
		return
	}
	pvCacheUpdate(ref, func(m *pvCachedModel) { m.Tools = "text" })
}

// ---------- window detection (G2) ----------

var pvNumCtxRE = lazyRx(`(?m)^\s*num_ctx\s+(\d+)`)

// DetectWindow probes the context window of ref and caches it. Order: the
// provider catalog (OpenRouter, Kilo, Groq, Gemini…); for local/self-hosted
// OpenAI-compatible servers Ollama /api/show, llama.cpp /props, LM Studio
// /api/v0/models, then /models. Ollama with a running num_ctx under 16k gets a
// warning (ModelWarning) asking for OLLAMA_CONTEXT_LENGTH.
func DetectWindow(ctx context.Context, cfg *Config, ref string) (int, error) {
	prov, id := SplitModel(ref)
	pc := cfg.Providers[prov]
	if pc == nil {
		return 0, fmt.Errorf("provedor desconhecido %q", prov)
	}
	if id == "" {
		return 0, errors.New("modelo vazio")
	}
	if pc.Type == "anthropic" {
		pvCacheUpdate(ref, func(m *pvCachedModel) { m.Ctx = 200000 })
		return 200000, nil
	}
	if pc.Type == "openai" && (pc.Local || pc.Ollama || !pvKnownCloud(pc.BaseURL)) {
		if win, warn := pvDetectLocal(ctx, cfg, prov, id); win > 0 {
			pvCacheUpdate(ref, func(m *pvCachedModel) { m.Ctx, m.Warn = win, warn })
			return win, nil
		}
	}
	ms, err := ListModels(ctx, cfg, prov)
	if err != nil {
		return 0, err
	}
	for _, m := range ms {
		if m.ID == id && m.Ctx > 0 {
			return m.Ctx, nil
		}
	}
	return 0, fmt.Errorf("janela de %s não informada pelo provedor", ref)
}

func pvKnownCloud(base string) bool {
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	h := u.Hostname()
	if pvPrivateHost(h) {
		return false
	}
	for _, d := range []string{"openrouter.ai", "openai.com", "groq.com", "kilo.ai", "googleapis.com", "nvidia.com", "mistral.ai",
		"cloudflare.com", "sambanova.ai", "z.ai", "cohere.ai", "cohere.com", "ollama.com", "vercel.sh", "cerebras.ai", "deepseek.com", "opencode.ai", "anthropic.com", "chatgpt.com"} {
		if h == d || strings.HasSuffix(h, "."+d) {
			return true
		}
	}
	return false
}

// pvPrivateHost: loopback, RFC 1918, CGNAT/tailnet (100.64/10), link-local, *.local, *.ts.net.
func pvPrivateHost(h string) bool {
	if h == "localhost" || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".ts.net") || strings.HasSuffix(h, ".lan") {
		return true
	}
	ip := net.ParseIP(h)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return true
	}
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	return cgnat.Contains(ip)
}

func pvRoot(base string) string {
	b := strings.TrimRight(base, "/")
	return strings.TrimSuffix(b, "/v1")
}

func pvDetectLocal(ctx context.Context, cfg *Config, prov, id string) (int, string) {
	pc := cfg.Providers[prov]
	root := pvRoot(pc.BaseURL)
	h := map[string]string{}
	if k := cfg.APIKey(prov); k != "" {
		h["Authorization"] = "Bearer " + k
	}
	// Ollama
	var show struct {
		ModelInfo  map[string]any `json:"model_info"`
		Parameters string         `json:"parameters"`
	}
	if err := pvPostJSON(ctx, root+"/api/show", h, map[string]any{"model": id}, &show, 5*time.Second); err == nil {
		win := 0
		for k, v := range show.ModelInfo {
			if strings.HasSuffix(k, ".context_length") {
				if f, ok := v.(float64); ok && int(f) > win {
					win = int(f)
				}
			}
		}
		numCtx := 0
		if m := pvNumCtxRE.FindStringSubmatch(show.Parameters); m != nil {
			numCtx, _ = strconv.Atoi(m[1])
		}
		warn := ""
		if numCtx > 0 {
			win = numCtx
		}
		if numCtx == 0 || numCtx < 16384 {
			shown := "o padrão do servidor (2–4k)"
			if numCtx > 0 {
				shown = strconv.Itoa(numCtx)
			}
			warn = fmt.Sprintf("Ollama pode estar cortando o contexto de %s em %s tokens — no servidor, exporte OLLAMA_CONTEXT_LENGTH=32768 (ou mais) e reinicie o ollama", id, shown)
		}
		if win > 0 {
			return win, warn
		}
	}
	// llama.cpp
	var props struct {
		Default struct {
			NCtx int `json:"n_ctx"`
		} `json:"default_generation_settings"`
		NCtx int `json:"n_ctx"`
	}
	if _, err := pvGetJSON(ctx, root+"/props", h, &props, 5*time.Second); err == nil {
		if n := max(props.Default.NCtx, props.NCtx); n > 0 {
			return n, ""
		}
	}
	// LM Studio (native REST): loaded_context_length / max_context_length
	var lms struct {
		Loaded int `json:"loaded_context_length"`
		Max    int `json:"max_context_length"`
	}
	if _, err := pvGetJSON(ctx, root+"/api/v0/models/"+url.PathEscape(id), h, &lms, 5*time.Second); err == nil {
		if lms.Loaded > 0 {
			return lms.Loaded, ""
		}
		if lms.Max > 0 {
			return lms.Max, ""
		}
	}
	return 0, ""
}

// ---------- reachability / probes (G3) ----------

type pvReach struct {
	ok bool
	at time.Time
}

var (
	pvReachMu sync.Mutex
	pvReachC  = map[string]pvReach{}
)

// pvReachable reports (cached: 60 s ok, 20 s fail) whether a local server answers within 1.5 s.
func pvReachable(c *Config, prov string) bool {
	pc := c.Providers[prov]
	if pc == nil {
		return false
	}
	key := pc.BaseURL
	pvReachMu.Lock()
	r, ok := pvReachC[key]
	pvReachMu.Unlock()
	if ok && (time.Since(r.at) < 20*time.Second || (r.ok && time.Since(r.at) < 60*time.Second)) {
		return r.ok
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	_, err := pvGetJSON(ctx, strings.TrimRight(pc.BaseURL, "/")+"/models", nil, nil, 1500*time.Millisecond)
	var ae *APIError
	up := err == nil || (errors.As(err, &ae) && ae.Status > 0)
	pvReachMu.Lock()
	pvReachC[key] = pvReach{ok: up, at: time.Now()}
	pvReachMu.Unlock()
	return up
}

// ProbeProvider checks whether a provider endpoint answers, and its context window if known.
// Any HTTP answer means reachable; 401/403 also returns an error (key refused).
func ProbeProvider(ctx context.Context, cfg *Config, name string) (reachable bool, win int, err error) {
	pc := cfg.Providers[name]
	if pc == nil {
		return false, 0, fmt.Errorf("provedor desconhecido %q", name)
	}
	if pc.Type == "codex" {
		_, err := chatgptCred(ctx, false)
		return err == nil, 0, err
	}
	base := strings.TrimRight(pc.BaseURL, "/")
	if strings.Contains(base, "{ACCOUNT}") {
		id := cfg.AccountID(name)
		if id == "" {
			return false, 0, fmt.Errorf("%s precisa do id da conta (%s)", name, pc.AccountEnv)
		}
		base = strings.ReplaceAll(base, "{ACCOUNT}", id)
	}
	h := map[string]string{}
	if k := cfg.APIKey(name); k != "" {
		if pc.Type == "anthropic" {
			h["x-api-key"], h["anthropic-version"] = k, "2023-06-01"
		} else {
			h["Authorization"] = "Bearer " + k
		}
	}
	u := base + "/models"
	if pc.Type == "anthropic" {
		u = base + "/v1/models?limit=1"
	}
	timeout := 1500 * time.Millisecond
	if !pc.Local {
		timeout = 10 * time.Second
	}
	_, gerr := pvGetJSON(ctx, u, h, nil, timeout)
	var ae *APIError
	switch {
	case gerr == nil:
		reachable = true
	case errors.As(gerr, &ae) && ae.Status > 0:
		reachable = true
		if ae.Status == 401 || ae.Status == 403 {
			err = fmt.Errorf("%s recusou a chave (HTTP %d)", name, ae.Status)
		}
	default:
		return false, 0, gerr
	}
	pvReachMu.Lock()
	pvReachC[pc.BaseURL] = pvReach{ok: reachable, at: time.Now()}
	pvReachMu.Unlock()
	if reachable && pc.DefaultModel != "" {
		ref := name + "/" + pc.DefaultModel
		if cm := pvCached(ref); cm != nil && cm.Ctx > 0 {
			win = cm.Ctx
		} else if pc.Local {
			win, _ = DetectWindow(ctx, cfg, ref)
		}
	}
	return reachable, win, err
}

var pvProvNameRE = lazyRx(`^[a-z0-9][a-z0-9_.-]{0,31}$`)

// AddProvider probes base_url/models, saves the provider to config.json and returns the models found.
// Private/LAN/tailnet hosts become Local (+NoKey when no key env is given); port 11434 is Ollama.
func (c *Config) AddProvider(ctx context.Context, name string, pc ProviderConf) ([]ModelInfo, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if !pvProvNameRE.MatchString(name) || name == OmniProvider {
		return nil, fmt.Errorf("nome de provedor inválido %q (use letras minúsculas, números, - _ .)", name)
	}
	if pc.BaseURL == "" {
		return nil, errors.New("falta a base_url (ex.: http://192.168.0.10:11434/v1)")
	}
	u, err := url.Parse(pc.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("base_url inválida %q", pc.BaseURL)
	}
	pc.BaseURL = strings.TrimRight(pc.BaseURL, "/")
	if pc.Type == "" {
		pc.Type = "openai"
	}
	if pvPrivateHost(u.Hostname()) {
		pc.Local = true
		if pc.KeyEnv == "" {
			pc.NoKey = true
		}
		pc.Free = true
	}
	if u.Port() == "11434" {
		pc.Ollama = true
	}
	if pc.KeyEnv == "" && !pc.NoKey {
		pc.NoKey = true
	}
	old, had := c.Providers[name]
	np := pc
	c.Providers[name] = &np
	ms, err := ListModels(ctx, c, name)
	if err != nil {
		if had {
			c.Providers[name] = old
		} else {
			delete(c.Providers, name)
		}
		return nil, fmt.Errorf("não consegui listar modelos em %s/models: %w", pc.BaseURL, err)
	}
	if np.DefaultModel == "" && len(ms) > 0 {
		np.DefaultModel = ms[0].ID
	}
	if err := c.Save(); err != nil {
		return ms, err
	}
	return ms, nil
}
