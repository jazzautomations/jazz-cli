package main

// Calibrated confidence for the offline interpreter (docs/research/
// decision-policy-calibrated.md): the number Parse prints as "confiança" is
// the thing brNeedsConfirm / brHeadlessLevel0 compare against thresholds,
// so it must mean "probability the reading is right", not a hand-tuned
// multiplier. calib.go has the math (ECE, Platt, isotonic); this file is
// the asset around it:
//
//   - nuCalibSamples builds honest (raw confidence, was it right) pairs
//     with K-fold held-out parsing of the grammar's own examples (the
//     classifier never sees the example it is scored on), the negatives
//     (a hit on the listed intent is a confident mistake) and the offline
//     eval prompts against the fixed test world.
//   - nuCalibFit chooses Platt or isotonic by 2-fold held-out ECE and
//     returns the asset; the test TestNLUCalibAsset regenerates it with
//     JAZZ_UPDATE_CALIB=1 (compile-time intelligence: the fit is paid once,
//     the runtime applies a 2-flop map or a binary search).
//   - nuCalibApply is what Parse calls on the raw score. "method":"none"
//     (or JAZZ_NLU_CALIB=off) is the identity, so a broken asset degrades to
//     the old behaviour instead of breaking the brain.

import (
	_ "embed"
	"encoding/json"
	"hash/fnv"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
)

//go:embed nlu/calib.json
var nuCalibJSON []byte

// nuCalibAsset is nlu/calib.json.
type nuCalibAsset struct {
	Version  int        `json:"version"`
	Method   string     `json:"method"` // none | platt | isotonic
	Platt    [2]float64 `json:"platt,omitempty"`
	Iso      []IsoPoint `json:"iso,omitempty"`
	N        int        `json:"n"`
	Folds    int        `json:"folds"`
	Seed     int64      `json:"seed"`
	ECERaw   float64    `json:"ece_raw"`
	ECECal   float64    `json:"ece_cal"` // held-out (2-fold) — never the in-sample number
	BrierRaw float64    `json:"brier_raw"`
	BrierCal float64    `json:"brier_cal"`
	Note     string     `json:"note,omitempty"`
}

var (
	nuCalibOnce sync.Once
	nuCalibDef  nuCalibAsset
)

// nuCalibGet returns the embedded asset (method none when absent/invalid).
func nuCalibGet() nuCalibAsset {
	nuCalibOnce.Do(func() {
		var a nuCalibAsset
		if err := json.Unmarshal(nuCalibJSON, &a); err != nil || a.Version < 1 {
			a = nuCalibAsset{Method: "none"}
		}
		if a.Method == "isotonic" && len(a.Iso) < 2 {
			a.Method = "none"
		}
		nuCalibDef = a
	})
	return nuCalibDef
}

// nuCalibApply maps a raw confidence to the calibrated probability.
// Monotone, clamped to [0,1]; identity when calibration is off.
func nuCalibApply(p float64) float64 {
	// OPT-IN (JAZZ_NLU_CALIB=on) until the samples are representative: the
	// K-fold set is the grammar's own examples (95% hits), so the isotonic
	// floor lifts weak readings (raw 0.37 -> 0.60 = "understood") and broke
	// the vetoes ("conecta no wifi" became connect_host) and the medium-band
	// confirmation in CI. Next step: samples from real misses (signals.jsonl).
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("JAZZ_NLU_CALIB"))); v != "on" && v != "1" && v != "true" {
		return p
	}
	return nuCalibGet().apply(p)
}

func (a nuCalibAsset) apply(p float64) float64 {
	if p <= 0 {
		return 0
	}
	var q float64
	switch a.Method {
	case "platt":
		q = PlattApply(p, a.Platt[0], a.Platt[1])
	case "isotonic":
		q = IsotonicApply(p, a.Iso)
	default:
		return p
	}
	return math.Max(0, math.Min(1, q))
}

// nuPredictName is the intent the brain acts on: the name when understood
// on the RAW score, else none (what the samples must grade).
func nuPredictName(in Intent) string {
	if in.Name != "" && in.Name != "none" && in.Raw >= nuMinConf {
		return in.Name
	}
	return "none"
}

// nuCalibFold assigns (intent, example) to one of k folds by a stable hash
// of the text, so the split does not depend on map order or edits elsewhere.
func nuCalibFold(k int, seed int64, text string) int {
	h := fnv.New64a()
	h.Write([]byte(text))
	v := h.Sum64() ^ uint64(seed)*0x9E3779B97F4A7C15
	return int(v % uint64(k))
}

// nuCalibSamples builds the calibration set. k folds (≥2), seed for the
// split. Deterministic: same grammar, same samples.
func nuCalibSamples(k int, seed int64) []CalibSample {
	if k < 2 {
		k = 2
	}
	lx, _ := nuFS.ReadFile("nlu/lexicon.json")
	ds, _ := nuFS.ReadFile("nlu/intents.json")
	var out []CalibSample
	// 1. held-out examples: the fold model never trained on them
	for f := 0; f < k; f++ {
		m := nuCompile(lx, ds)
		if m.loadErr != nil {
			return nil
		}
		fold := f
		m.nb = m.nuTrain(func(ii, ei int) bool {
			return nuCalibFold(k, seed, m.data.Intents[ii].Examples[ei]) == fold
		})
		m.nbOnce.Do(func() {}) // the fold table is the classifier
		w := nuWorldFromDef(m.data.World)
		for _, it := range m.data.Intents {
			for _, ex := range it.Examples {
				if nuCalibFold(k, seed, ex) != fold {
					continue
				}
				in := m.Parse(ex, w)
				out = append(out, CalibSample{P: in.Raw, Correct: nuPredictName(in) == it.Name})
			}
		}
	}
	// 2. negatives: landing on the listed intent is a confident mistake;
	//    landing elsewhere is unknown (not graded)
	full := nuGet()
	w := nuWorldFromDef(full.data.World)
	for _, it := range full.data.Intents {
		for _, neg := range it.Negatives {
			in := full.Parse(neg, w)
			if nuPredictName(in) == it.Name {
				out = append(out, CalibSample{P: in.Raw, Correct: false})
			}
		}
	}
	// 3. offline eval prompts with an expected single intent
	var suite struct {
		Tasks []eoTask `json:"tasks"`
	}
	if json.Unmarshal(eoTasksJSON, &suite) == nil {
		for _, t := range suite.Tasks {
			if t.Prompt == "" || t.Expect.Intent == "" || t.Expect.Intent == "multi" {
				continue
			}
			in := full.Parse(t.Prompt, w)
			out = append(out, CalibSample{P: in.Raw, Correct: nuPredictName(in) == t.Expect.Intent})
		}
	}
	// stable order: by P then Hit (the fit is order-independent, the JSON is not)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].P != out[j].P {
			return out[i].P < out[j].P
		}
		return !out[i].Correct && out[j].Correct
	})
	return out
}

// nuCalibHeldOutECE scores a fitting method honestly: fit on one half,
// measure on the other, both ways, mean. bins = 10.
func nuCalibHeldOutECE(samples []CalibSample, fit func([]CalibSample) func(float64) float64) (ece, brier float64) {
	var a, b []CalibSample
	for i, s := range samples {
		if i%2 == 0 {
			a = append(a, s)
		} else {
			b = append(b, s)
		}
	}
	score := func(train, test []CalibSample) (float64, float64) {
		f := fit(train)
		mapped := make([]CalibSample, len(test))
		for i, s := range test {
			mapped[i] = CalibSample{P: f(s.P), Correct: s.Correct}
		}
		return ECE(mapped, 10), BrierScore(mapped)
	}
	e1, b1 := score(a, b)
	e2, b2 := score(b, a)
	return (e1 + e2) / 2, (b1 + b2) / 2
}

// nuCalibFit builds the asset from samples: the method with the lower
// held-out ECE wins; its parameters are then fitted on everything.
func nuCalibFit(samples []CalibSample, k int, seed int64) nuCalibAsset {
	a := nuCalibAsset{Version: 1, Method: "none", N: len(samples), Folds: k, Seed: seed}
	if len(samples) < 50 {
		a.Note = "too few samples to calibrate"
		return a
	}
	a.ECERaw = ECE(samples, 10)
	a.BrierRaw = BrierScore(samples)
	plattFit := func(tr []CalibSample) func(float64) float64 {
		pa, pb := PlattFit(tr)
		return func(p float64) float64 { return PlattApply(p, pa, pb) }
	}
	isoFit := func(tr []CalibSample) func(float64) float64 {
		pts := IsotonicFit(tr)
		return func(p float64) float64 { return IsotonicApply(p, pts) }
	}
	pe, pbr := nuCalibHeldOutECE(samples, plattFit)
	ie, ibr := nuCalibHeldOutECE(samples, isoFit)
	rawHeld, _ := nuCalibHeldOutECE(samples, func([]CalibSample) func(float64) float64 { return func(p float64) float64 { return p } })
	best, bestECE, bestBrier := "none", rawHeld, a.BrierRaw
	if pe < bestECE {
		best, bestECE, bestBrier = "platt", pe, pbr
	}
	if ie < bestECE {
		best, bestECE, bestBrier = "isotonic", ie, ibr
	}
	a.Method, a.ECECal, a.BrierCal = best, bestECE, bestBrier
	switch best {
	case "platt":
		a.Platt[0], a.Platt[1] = PlattFit(samples)
	case "isotonic":
		// PAV returns one node per block; equal P nodes carry the same Y, so
		// keep the first of each P — the asset stays readable (≈10 nodes)
		var pts []IsoPoint
		for _, pt := range IsotonicFit(samples) {
			if len(pts) == 0 || pt.P > pts[len(pts)-1].P {
				pts = append(pts, pt)
			}
		}
		a.Iso = pts
	}
	a.Note = "generated by TestNLUCalibAsset with JAZZ_UPDATE_CALIB=1; ece_cal/brier_cal are 2-fold held-out"
	return a
}
