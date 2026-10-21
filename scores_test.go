package main

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestBgScoreValue(t *testing.T) {
	empty := bgScore{}
	if v := empty.value(); v != 0.5 {
		t.Errorf("prior = %v, want 0.5", v)
	}
	good := bgScore{UntilOK: 8, QualOK: 0, OK: 10}
	bad := bgScore{UntilFail: 6, QualFail: 2, OK: 10}
	stally := bgScore{UntilOK: 8, Stalls: 10, OK: 2}
	if !(good.value() > stally.value() && stally.value() > bad.value()) {
		t.Errorf("order good %.2f > stally %.2f > bad %.2f broken", good.value(), stally.value(), bad.value())
	}
}

// bgScoreSeed writes model scores straight into the store of the current JAZZ_HOME.
func bgScoreSeed(t *testing.T, recs map[string]bgScore) {
	t.Helper()
	refs := map[string]*bgScore{}
	for r, s := range recs {
		s := s
		refs[r] = &s
	}
	b, _ := json.Marshal(map[string]any{"v": 1, "refs": refs})
	os.MkdirAll(dataDir(), 0o700)
	if err := os.WriteFile(bgScoresPath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	bgScoreDB.mu.Lock()
	bgScoreDB.path, bgScoreDB.base = "", nil // reload on next use
	bgScoreDB.mu.Unlock()
}

func TestBgSortRunByScoreThenSpeed(t *testing.T) {
	pfTestHome(t)
	bgScoreSeed(t, map[string]bgScore{
		"p/bad":      {UntilFail: 5, OK: 5, MS: 100},
		"p/good":     {UntilOK: 5, OK: 5, MS: 900},
		"p/fastgood": {UntilOK: 5, OK: 5, MS: 200},
		"p/few":      {UntilFail: 2}, // under bgScoreMinObs: keeps the 0.5 prior
	})
	got := bgSortRun([]string{"p/bad", "p/new", "p/good", "p/few", "p/fastgood"})
	want := "[p/fastgood p/good p/new p/few p/bad]"
	if fmt.Sprint(got) != want {
		t.Errorf("order = %v, want %s", got, want)
	}
}

func TestBgOrderCandsKeepsFirstAndPriceClass(t *testing.T) {
	pfTestHome(t)
	bgScoreSeed(t, map[string]bgScore{
		"zen/a":    {UntilFail: 5},
		"zen/b":    {UntilOK: 5},
		"paid/x":   {UntilFail: 9},
		"paid/y":   {UntilOK: 9},
		"zen/main": {UntilFail: 9},
	})
	a := &Agent{cfg: &Config{Providers: map[string]*ProviderConf{}}}
	in := []string{"zen/main", "zen/a", "zen/b", "paid/x", "paid/y"}
	got := a.bgOrderCands(in)
	if fmt.Sprint(got) != "[zen/main zen/b zen/a paid/y paid/x]" {
		t.Errorf("order = %v", got)
	}
	t.Setenv("JAZZ_FALLBACK_ORDER", "config")
	if got := a.bgOrderCands(in); fmt.Sprint(got) != fmt.Sprint(in) {
		t.Errorf("fallback_order config must keep the order: %v", got)
	}
}

func TestBgScoresFlushMerges(t *testing.T) {
	pfTestHome(t)
	bgScoreSeed(t, map[string]bgScore{"p/m": {UntilOK: 2, OK: 1, MS: 1000}})
	bgScoreRecord("p/m", "until", true)
	bgScoreRecord("p/m", "quality", false)
	bgScoreStall("p/m")
	bgScoreOK("p/m", 500*time.Millisecond)
	if s := bgScoreOf("p/m"); s.UntilOK != 3 || s.QualFail != 1 || s.Stalls != 1 || s.OK != 2 {
		t.Errorf("merged before flush: %+v", s)
	}
	// another process saved meanwhile: the flush adds, never overwrites
	b, _ := os.ReadFile(bgScoresPath())
	var f struct {
		Refs map[string]*bgScore `json:"refs"`
	}
	json.Unmarshal(b, &f)
	f.Refs["p/m"].UntilOK += 10
	f.Refs["q/n"] = &bgScore{QualOK: 1}
	b, _ = json.Marshal(map[string]any{"v": 1, "refs": f.Refs})
	os.WriteFile(bgScoresPath(), b, 0o600)
	bgScoresFlush()
	bgScoreDB.mu.Lock()
	bgScoreDB.path, bgScoreDB.base = "", nil
	bgScoreDB.mu.Unlock()
	s := bgScoreOf("p/m")
	if s.UntilOK != 13 || s.QualFail != 1 || s.Stalls != 1 || s.OK != 2 {
		t.Errorf("after flush: %+v", s)
	}
	if bgScoreOf("q/n").QualOK != 1 {
		t.Error("other process's model lost")
	}
}

func TestAgentRecordsStallAndSpeed(t *testing.T) {
	h := newHarness(t, func(n int, r recReq) fakeResp {
		if r.Model == "a:free" {
			return fakeResp{Stall: true}
		}
		return fakeResp{Text: "from b"}
	}, func(c *Config) {
		c.Model = "fake/a:free"
		c.Fallback = []string{"fake/b:free"}
		c.FirstByteTimeout = 1
	})
	h.ag.Model = "fake/a:free"
	if err := h.run(t, "hi"); err != nil {
		t.Fatal(err)
	}
	if s := bgScoreOf("fake/a:free"); s.Stalls != 1 {
		t.Errorf("stall not recorded: %+v", s)
	}
	if s := bgScoreOf("fake/b:free"); s.OK != 1 || s.MS < 0 {
		t.Errorf("success not recorded: %+v", s)
	}
	if _, err := os.Stat(bgScoresPath()); err != nil {
		t.Errorf("scores not saved at the end of the turn: %v", err)
	}
}
