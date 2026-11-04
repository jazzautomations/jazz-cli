package main

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

const nuCalibK, nuCalibSeed = 5, 42

// The asset is honest: regenerated from the grammar it ships with, it must
// not be worse than the raw score on fresh held-out samples. With
// JAZZ_UPDATE_CALIB=1 it is rewritten (the forge step).
func TestNLUCalibAsset(t *testing.T) {
	samples := nuCalibSamples(nuCalibK, nuCalibSeed)
	if len(samples) < 200 {
		t.Fatalf("only %d calibration samples; the grammar lost examples?", len(samples))
	}
	hits := 0
	for _, s := range samples {
		if s.Correct {
			hits++
		}
	}
	fit := nuCalibFit(samples, nuCalibK, nuCalibSeed)
	// the reliability diagram is the evidence: where the raw score lies
	for _, b := range ReliabilityBins(samples, 10) {
		if b.N > 0 {
			t.Logf("  raw bin [%.1f,%.1f) n=%3d conf=%.2f acc=%.2f", b.Lo, b.Hi, b.N, b.Conf, b.Acc)
		}
	}
	t.Logf("samples %d (hits %d) · raw ECE %.3f Brier %.3f · best %s · held-out ECE %.3f Brier %.3f",
		len(samples), hits, fit.ECERaw, fit.BrierRaw, fit.Method, fit.ECECal, fit.BrierCal)
	if os.Getenv("JAZZ_UPDATE_CALIB") != "" {
		b, _ := json.MarshalIndent(fit, "", " ")
		if err := os.WriteFile("nlu/calib.json", append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("nlu/calib.json rewritten (%s)", fit.Method)
		return
	}
	shipped := nuCalibGet()
	if shipped.Method == "none" {
		t.Skip("no calibration shipped (method none)")
	}
	// the shipped map, applied to today's samples, must beat the raw score
	mapped := make([]CalibSample, len(samples))
	for i, s := range samples {
		mapped[i] = CalibSample{P: shipped.apply(s.P), Correct: s.Correct}
	}
	eRaw, eCal := ECE(samples, 10), ECE(mapped, 10)
	t.Logf("shipped %s: ECE raw %.3f → cal %.3f (asset says %.3f held-out)", shipped.Method, eRaw, eCal, shipped.ECECal)
	if eCal > eRaw+0.01 {
		t.Errorf("shipped calibration hurts: ECE %.3f > raw %.3f — regenerate with JAZZ_UPDATE_CALIB=1", eCal, eRaw)
	}
}

// The map is a probability map: monotone, inside [0,1], identity when off.
func TestNLUCalibApplyShape(t *testing.T) {
	a := nuCalibGet()
	prev := -1.0
	for i := 0; i <= 100; i++ {
		p := float64(i) / 100
		q := a.apply(p)
		if q < 0 || q > 1 || math.IsNaN(q) {
			t.Fatalf("apply(%.2f) = %v out of [0,1]", p, q)
		}
		if q+1e-9 < prev {
			t.Fatalf("apply not monotone at %.2f: %.4f < %.4f", p, q, prev)
		}
		prev = q
	}
	if (nuCalibAsset{Method: "none"}).apply(0.84) != 0.84 {
		t.Error("method none must be the identity")
	}
	t.Setenv("JAZZ_NLU_CALIB", "off")
	if nuCalibApply(0.84) != 0.84 {
		t.Error("JAZZ_NLU_CALIB=off must be the identity")
	}
}

// Parse exposes both numbers: Raw (what the grammar computed) and
// Confidence (what the brain acts on).
func TestNLUParseRawAndCalibrated(t *testing.T) {
	m, w := nuTestWorld(t)
	if in := m.Parse("portas abertas", w); in.Confidence != in.Raw {
		t.Errorf("calibration is opt-in: default confidence %.2f must equal raw %.2f", in.Confidence, in.Raw)
	}
	t.Setenv("JAZZ_NLU_CALIB", "on")
	in := m.Parse("portas abertas", w)
	if in.Raw <= 0 || in.Confidence <= 0 {
		t.Fatalf("raw %.2f conf %.2f", in.Raw, in.Confidence)
	}
	if want := math.Round(nuCalibApply(in.Raw)*100) / 100; math.Abs(in.Confidence-want) > 0.011 {
		t.Errorf("confidence %.2f is not calib(raw %.2f) = %.2f", in.Confidence, in.Raw, want)
	}
}
