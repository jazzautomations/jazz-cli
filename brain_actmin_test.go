package main

import "testing"

// The act threshold is a decision policy on a calibrated probability
// (docs/research/decision-policy-calibrated.md, Chow's rule with
// asymmetric costs): a read-only level-0 reading runs at ≥ brReadActMin,
// anything that mutates or interprets keeps brLevel0Min.
func TestBrainActMinByRiskBand(t *testing.T) {
	cases := []struct {
		name string
		conf float64
		ask  bool
	}{
		{"list_ports", 0.75, false}, // read-only: a wrong answer costs one re-ask
		{"list_ports", 0.65, true},  // below the read band: ask
		{"delete_file", 0.75, true}, // mutation: the full threshold
		{"git_commit", 0.84, true},  // mutation just under 0.85: ask
		{"git_commit", 0.9, false},  // confident mutation: consent is the permission gate
		{"explain", 0.75, true},     // interpretive (grounds the model): full threshold
		{"disk_usage", 0.84, false}, // the calibrated 0.7–0.85 band is where this policy differs
	}
	for _, c := range cases {
		in := Intent{Name: c.name, Confidence: c.conf}
		if got := brNeedsConfirm(in, cbLow); got != c.ask {
			t.Errorf("%s conf %.2f: confirm=%v, want %v (min %.2f)", c.name, c.conf, got, c.ask, brActMin(in))
		}
	}
	if brActMin(Intent{Name: "list_ports", TypoOnly: true, Confidence: 0.9}) != brReadActMin || !brNeedsConfirm(Intent{Name: "list_ports", TypoOnly: true, Confidence: 0.9}, cbLow) {
		t.Error("a typo-only reading always confirms, whatever the band")
	}
}
