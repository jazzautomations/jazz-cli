package main

import (
	"strings"
	"testing"
	"time"
)

// A free model that sends only keep-alives must be abandoned at the first-byte
// deadline and the next candidate used, without retrying the stalled one.
func TestAgentFirstByteStallFailsOverQuickly(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if r.Model == "a:free" {
			return fakeResp{Stall: true}
		}
		return fakeResp{Text: "oi de b"}
	}, func(c *Config) {
		c.Model = "fake/a:free"
		c.Fallback = []string{"fake/b:free"}
		c.FirstByteTimeout = 1
	})
	h.ag.Model = "fake/a:free"
	start := time.Now()
	if err := h.run(t, "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("failover took %s", d)
	}
	if got := h.fs.countModel("a:free"); got != 1 {
		t.Errorf("stalled model must be tried once, got %d", got)
	}
	if got := h.fs.countModel("b:free"); got != 1 {
		t.Errorf("fallback used %d times", got)
	}
	if !anyContains(h.ui.warns, "não respondeu em 1s") {
		t.Errorf("stall warning missing: %v", h.ui.warns)
	}
	// next turn: the stalled model goes to the back, so b answers first
	if err := h.run(t, "de novo"); err != nil {
		t.Fatal(err)
	}
	if got := h.fs.countModel("a:free"); got != 1 {
		t.Errorf("stalled model retried on the next turn: %d", got)
	}
}

func TestConfigFirstByte(t *testing.T) {
	c := &Config{Providers: map[string]*ProviderConf{"p": {Type: "openai"}}}
	if d := c.FirstByte("p/x:free"); d != pvDefaultFirstByte {
		t.Errorf("free default %s", d)
	}
	if d := c.FirstByte("p/paid-model"); d != 0 {
		t.Errorf("paid model gets no first-byte deadline: %s", d)
	}
	c.FirstByteTimeout = 7
	if d := c.FirstByte("p/x:free"); d != 7*time.Second {
		t.Errorf("configured %s", d)
	}
	c.FirstByteTimeout = -1
	if d := c.FirstByte("p/x:free"); d != 0 {
		t.Errorf("disabled %s", d)
	}
}

func TestKeylessDefaultAndImplicitChain(t *testing.T) {
	c := pvTestCfg(t)
	got := c.Candidates("kilo/" + kiloDefaultModel)
	want := "kilo/poolside/laguna-s-2.1:free,kilo/minimax/minimax-m3.1-flash:free,kilo/cohere/north-mini-code:free," +
		"kilo/nvidia/nemotron-3-super-120b-a12b:free,kilo/nvidia/nemotron-3.5-lightning:free"
	if strings.Join(got, ",") != want {
		t.Fatalf("chain %v", got)
	}
	for _, tier := range []string{"free", "coder", "fast"} {
		for _, r := range c.CandidatesFor(Need{}, "omni/"+tier) {
			if r == "kilo/kilo-auto/free" {
				t.Fatalf("kilo-auto/free picked automatically for omni/%s", tier)
			}
		}
	}
	// still selectable by hand
	if got := c.Candidates("kilo/kilo-auto/free"); got[0] != "kilo/kilo-auto/free" {
		t.Fatalf("manual pick %v", got)
	}
	// an old saved default is migrated
	u := &ProviderConf{DefaultModel: "kilo-auto/free"}
	pvFillProvider(u, omniPresets()["kilo"])
	if u.DefaultModel != kiloDefaultModel {
		t.Fatalf("not migrated: %s", u.DefaultModel)
	}
}
