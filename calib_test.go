package main

import (
	"math"
	"testing"
)

// TestECEPerfectlyCalibrated builds a model whose per-bin accuracy equals
// its per-bin confidence exactly: 100 predictions at P=0.25 of which 25
// are correct, and 100 at P=0.75 of which 75 are correct. With nbins=2
// every sample sits inside its own bin, so ECE must be ~0.
func TestECEPerfectlyCalibrated(t *testing.T) {
	var samples []CalibSample
	for i := 0; i < 100; i++ {
		samples = append(samples, CalibSample{P: 0.25, Correct: i < 25})
	}
	for i := 0; i < 100; i++ {
		samples = append(samples, CalibSample{P: 0.75, Correct: i < 75})
	}
	if ece := ECE(samples, 2); math.Abs(ece) > 1e-9 {
		t.Fatalf("ECE = %v, want ~0", ece)
	}
	if mce := MCE(samples, 2); math.Abs(mce) > 1e-9 {
		t.Fatalf("MCE = %v, want ~0", mce)
	}
}

// TestECEMiscalibrated uses a model that always predicts 0.9 but is right
// only half the time: one non-empty bin with |0.5 - 0.9| = 0.4.
func TestECEMiscalibrated(t *testing.T) {
	var samples []CalibSample
	for i := 0; i < 200; i++ {
		samples = append(samples, CalibSample{P: 0.9, Correct: i%2 == 0})
	}
	if ece := ECE(samples, 10); math.Abs(ece-0.4) > 1e-9 {
		t.Fatalf("ECE = %v, want 0.4", ece)
	}
	if mce := MCE(samples, 10); math.Abs(mce-0.4) > 1e-9 {
		t.Fatalf("MCE = %v, want 0.4 (single non-empty bin)", mce)
	}
}

// TestPlattImprovesCalibration builds an overconfident model: confidences
// spread over [0.6, 0.9] while accuracy is ~55% regardless of P. Platt
// scaling should collapse the map toward the base rate and cut ECE.
func TestPlattImprovesCalibration(t *testing.T) {
	var samples []CalibSample
	for i := 0; i < 400; i++ {
		p := 0.6 + 0.3*float64(i%10)/9
		samples = append(samples, CalibSample{P: p, Correct: (i*7)%20 < 11})
	}
	const nbins = 10
	before := ECE(samples, nbins)
	a, b := PlattFit(samples)
	calibrated := make([]CalibSample, len(samples))
	for i, s := range samples {
		calibrated[i] = CalibSample{P: PlattApply(s.P, a, b), Correct: s.Correct}
	}
	after := ECE(calibrated, nbins)
	if !(after < before) {
		t.Fatalf("Platt did not improve ECE: before=%v after=%v (a=%v b=%v)", before, after, a, b)
	}
	if after > 0.05 {
		t.Fatalf("ECE after Platt = %v, want < 0.05", after)
	}
}

// TestBrierScore: (0.9-1)^2 = 0.01 and (0.7-0)^2 = 0.49, mean = 0.25.
func TestBrierScore(t *testing.T) {
	samples := []CalibSample{
		{P: 0.9, Correct: true},
		{P: 0.7, Correct: false},
	}
	if bs := BrierScore(samples); math.Abs(bs-0.25) > 1e-9 {
		t.Fatalf("BrierScore = %v, want 0.25", bs)
	}
	if bs := BrierScore(nil); bs != 0 {
		t.Fatalf("BrierScore(nil) = %v, want 0", bs)
	}
}

// TestReliabilityBins checks bin edges, counts, mean confidence and
// accuracy for a hand-computable 4-sample case.
func TestReliabilityBins(t *testing.T) {
	samples := []CalibSample{
		{P: 0.1, Correct: true},
		{P: 0.2, Correct: false},
		{P: 0.6, Correct: true},
		{P: 0.9, Correct: false},
	}
	bins := ReliabilityBins(samples, 2)
	if len(bins) != 2 {
		t.Fatalf("len(bins) = %d, want 2", len(bins))
	}
	// Bin 0 covers [0, 0.5): P = {0.1, 0.2} -> Conf = 0.15, Acc = 1/2.
	if bins[0].Lo != 0 || bins[0].Hi != 0.5 {
		t.Fatalf("bin0 edges = [%v, %v), want [0, 0.5)", bins[0].Lo, bins[0].Hi)
	}
	if bins[0].N != 2 {
		t.Fatalf("bin0 N = %d, want 2", bins[0].N)
	}
	if math.Abs(bins[0].Conf-0.15) > 1e-9 {
		t.Fatalf("bin0 Conf = %v, want 0.15", bins[0].Conf)
	}
	if math.Abs(bins[0].Acc-0.5) > 1e-9 {
		t.Fatalf("bin0 Acc = %v, want 0.5", bins[0].Acc)
	}
	// Bin 1 covers [0.5, 1]: P = {0.6, 0.9} -> Conf = 0.75, Acc = 1/2.
	if bins[1].Lo != 0.5 || bins[1].Hi != 1.0 {
		t.Fatalf("bin1 edges = [%v, %v), want [0.5, 1]", bins[1].Lo, bins[1].Hi)
	}
	if bins[1].N != 2 {
		t.Fatalf("bin1 N = %d, want 2", bins[1].N)
	}
	if math.Abs(bins[1].Conf-0.75) > 1e-9 {
		t.Fatalf("bin1 Conf = %v, want 0.75", bins[1].Conf)
	}
	if math.Abs(bins[1].Acc-0.5) > 1e-9 {
		t.Fatalf("bin1 Acc = %v, want 0.5", bins[1].Acc)
	}
}

// TestCalibIsotonicMonotone verifies the PAV fit produces a non-decreasing
// calibration map on data where correctness rises with P.
func TestCalibIsotonicMonotone(t *testing.T) {
	var samples []CalibSample
	for i := 0; i < 500; i++ {
		p := float64((i*37)%100) / 100
		samples = append(samples, CalibSample{P: p, Correct: (i*13)%100 < int(p*100)})
	}
	fit := IsotonicFit(samples)
	if len(fit) == 0 {
		t.Fatal("IsotonicFit returned no points")
	}
	prev := math.Inf(-1)
	for p := 0.0; p <= 1.0; p += 0.05 {
		y := IsotonicApply(p, fit)
		if y < prev-1e-9 {
			t.Fatalf("IsotonicApply not monotone: f(%v)=%v < f(prev)=%v", p, y, prev)
		}
		if y < -1e-9 || y > 1+1e-9 {
			t.Fatalf("IsotonicApply(%v) = %v out of [0,1]", p, y)
		}
		prev = y
	}
}
