package main

// calib.go — confidence calibration math for the offline NLU.
//
// The classifier emits a score P we would like to treat as a probability:
// among all predictions with P ~= 0.6, roughly 60% should be correct. These
// helpers measure how far that is from true and fit post-hoc corrections.
// Everything here is pure (no I/O), deterministic, and cheap: O(n) for
// metrics, O(n log n) for isotonic fitting, a few Newton iterations for
// Platt scaling.
//
// FORMULAS
//
// Equal-width binning: bin b covers [b/B, (b+1)/B) for B = nbins, with
//
//	acc(B_b)  = (1/|B_b|) * sum_{i in B_b} 1[y_i = correct]
//	conf(B_b) = (1/|B_b|) * sum_{i in B_b} P_i
//
// Expected Calibration Error (Naeini, Cooper & Hauskrecht, AAAI 2015,
// "Obtaining Well Calibrated Probabilities Using Bayesian Binning"):
//
//	ECE = sum_{b=1..B} (|B_b| / N) * |acc(B_b) - conf(B_b)|
//
// Maximum Calibration Error is the same gap without averaging:
//
//	MCE = max_{b=1..B} |acc(B_b) - conf(B_b)|
//
// Brier score (Brier 1950):
//
//	BS = (1/N) * sum_i (P_i - y_i)^2,   y_i in {0,1}
//
// Platt scaling (Platt 1999, "Probabilistic Outputs for Support Vector
// Machines and Comparisons to Regularized Likelihood Methods"): fit a
// logistic map on the log-odds of the raw score,
//
//	P' = sigmoid(a * logit(P) + b),   logit(P) = ln(P / (1-P))
//
// with (a, b) chosen to minimize regularized log-loss
//
//	L = -sum_i [ y_i ln P'_i + (1-y_i) ln(1-P'_i) ] + (lambda/2)(a^2+b^2)
//
// via damped 2x2 Newton iterations on the gradient/Hessian of L.
//
// Isotonic regression via Pool-Adjacent-Violators (Ayer et al. 1955; used
// for calibration by Zadrozny & Elkan 2002): sort samples by P, then fit
// the non-decreasing step function g minimizing sum (g_i - y_i)^2 by
// merging any adjacent blocks whose means violate monotonicity. Applied
// piecewise-linearly between block centers.
//
// NUMERICAL CARE: P is clamped to [1e-6, 1-1e-6] before logit so endpoints
// 0/1 cannot produce +-Inf; sigmoid uses the exp(z)-branch for z < 0 so
// large |z| never overflows.

import (
	"math"
	"sort"
)

const (
	// calibEps keeps probabilities off the exact endpoints so logit stays
	// finite.
	calibEps = 1e-6
	// plattLambda is a tiny L2 penalty keeping Platt parameters finite on
	// degenerate data (e.g. all samples correct).
	plattLambda = 1e-6
	// plattMaxIter caps Newton iterations; convergence is normally <20.
	plattMaxIter = 100
)

// CalibSample pairs a predicted confidence P in [0,1] with whether the
// prediction was actually correct.
type CalibSample struct {
	P       float64
	Correct bool
}

// ReliabilityBin is one equal-width slice of the reliability diagram:
// Lo/Hi are the P range, Conf the mean predicted confidence, Acc the
// empirical accuracy, and N the sample count. Empty bins keep N == 0 and
// zero Conf/Acc. Declared as a type alias so callers may also write the
// literal anonymous struct type.
type ReliabilityBin = struct {
	Lo, Hi    float64
	Conf, Acc float64
	N         int
}

func clampP(p float64) float64 {
	if p < calibEps {
		return calibEps
	}
	if p > 1-calibEps {
		return 1 - calibEps
	}
	return p
}

func sigmoid(z float64) float64 {
	if z >= 0 {
		return 1 / (1 + math.Exp(-z))
	}
	e := math.Exp(z)
	return e / (1 + e)
}

func logit(p float64) float64 {
	return math.Log(p / (1 - p))
}

// ReliabilityBins groups samples into nbins equal-width bins over [0,1]
// and reports mean confidence and accuracy per bin. The result always has
// nbins entries; empty bins have N == 0 and zero Conf/Acc. Out-of-range P
// is clamped into [0,1] for assignment.
func ReliabilityBins(samples []CalibSample, nbins int) []ReliabilityBin {
	if nbins < 1 {
		nbins = 1
	}
	bins := make([]ReliabilityBin, nbins)
	w := 1.0 / float64(nbins)
	for i := range bins {
		bins[i].Lo = float64(i) * w
		bins[i].Hi = float64(i+1) * w
	}
	var sumP, sumY []float64
	if len(samples) > 0 {
		sumP = make([]float64, nbins)
		sumY = make([]float64, nbins)
	}
	for _, s := range samples {
		p := s.P
		if p < 0 {
			p = 0
		}
		if p > 1 {
			p = 1
		}
		idx := int(p * float64(nbins))
		if idx >= nbins {
			idx = nbins - 1
		}
		bins[idx].N++
		sumP[idx] += p
		if s.Correct {
			sumY[idx]++
		}
	}
	for i := range bins {
		if bins[i].N > 0 {
			n := float64(bins[i].N)
			bins[i].Conf = sumP[i] / n
			bins[i].Acc = sumY[i] / n
		}
	}
	return bins
}

// ECE returns the Expected Calibration Error:
//
//	ECE = sum_b (|B_b|/N) * |acc(B_b) - conf(B_b)|
//
// over nbins equal-width bins (Naeini et al. 2015). Empty bins contribute
// zero. ECE == 0 iff every non-empty bin's mean confidence equals its
// empirical accuracy. Returns 0 for empty input.
func ECE(samples []CalibSample, nbins int) float64 {
	n := len(samples)
	if n == 0 {
		return 0
	}
	bins := ReliabilityBins(samples, nbins)
	var ece float64
	for _, b := range bins {
		if b.N == 0 {
			continue
		}
		ece += (float64(b.N) / float64(n)) * math.Abs(b.Acc-b.Conf)
	}
	return ece
}

// MCE returns the Maximum Calibration Error: the largest per-bin
// |acc - conf| gap. Sensitive to small bins; use with generous N.
func MCE(samples []CalibSample, nbins int) float64 {
	var mce float64
	for _, b := range ReliabilityBins(samples, nbins) {
		if b.N == 0 {
			continue
		}
		if gap := math.Abs(b.Acc - b.Conf); gap > mce {
			mce = gap
		}
	}
	return mce
}

// BrierScore returns the mean squared error of the probabilistic
// predictions: (1/N) * sum (P_i - y_i)^2 with y_i in {0,1}. Lower is
// better; a perfect predictor scores 0. Returns 0 for empty input.
func BrierScore(samples []CalibSample) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, s := range samples {
		var y float64
		if s.Correct {
			y = 1
		}
		d := s.P - y
		sum += d * d
	}
	return sum / float64(len(samples))
}

// PlattFit fits P' = sigmoid(a*logit(P) + b) by minimizing the
// L2-regularized log-loss with a damped 2x2 Newton method (Platt 1999).
// Starting from the identity map (a=1, b=0), each iteration solves
// H*d = g for the step and applies theta -= d, stopping when the step
// shrinks below 1e-10 or plattMaxIter is reached. Returns (a, b); empty
// input yields the identity (1, 0).
func PlattFit(samples []CalibSample) (a, b float64) {
	if len(samples) == 0 {
		return 1, 0
	}
	xs := make([]float64, len(samples))
	ys := make([]float64, len(samples))
	for i, s := range samples {
		xs[i] = logit(clampP(s.P))
		if s.Correct {
			ys[i] = 1
		}
	}
	a, b = 1, 0
	for iter := 0; iter < plattMaxIter; iter++ {
		var gA, gB, hAA, hAB, hBB float64
		for i := range xs {
			p := sigmoid(a*xs[i] + b)
			d := p - ys[i]
			w := p * (1 - p)
			gA += d * xs[i]
			gB += d
			hAA += w * xs[i] * xs[i]
			hAB += w * xs[i]
			hBB += w
		}
		gA += plattLambda * a
		gB += plattLambda * b
		hAA += plattLambda
		hBB += plattLambda
		det := hAA*hBB - hAB*hAB
		if det < 1e-18 {
			break
		}
		da := (hBB*gA - hAB*gB) / det
		db := (hAA*gB - hAB*gA) / det
		a -= da
		b -= db
		if math.Abs(da)+math.Abs(db) < 1e-10 {
			break
		}
	}
	return a, b
}

// PlattApply evaluates the fitted Platt map on a raw confidence p.
func PlattApply(p, a, b float64) float64 {
	return sigmoid(a*logit(clampP(p)) + b)
}

// IsoPoint is one node of the fitted isotonic calibration map: raw
// confidence P maps to calibrated probability Y. Points are strictly
// increasing in P and non-decreasing in Y.
type IsoPoint struct {
	P, Y float64
}

// IsotonicFit learns a monotone recalibration map by Pool-Adjacent-
// Violators (Ayer et al. 1955; Zadrozny & Elkan 2002). Samples are sorted
// by P and merged into the fewest blocks with non-decreasing mean
// outcome; each final block contributes one IsoPoint at its mean P and
// mean outcome. Returns nil for empty input.
func IsotonicFit(samples []CalibSample) []IsoPoint {
	if len(samples) == 0 {
		return nil
	}
	sorted := make([]CalibSample, len(samples))
	copy(sorted, samples)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].P < sorted[j].P })

	// PAV: maintain a stack of blocks; each block tracks its sum of
	// outcomes, weight, and the sum of P (for the emitted point's x).
	type block struct {
		sumY, sumP float64
		n          int
	}
	stack := make([]block, 0, len(sorted))
	for _, s := range sorted {
		var y float64
		if s.Correct {
			y = 1
		}
		stack = append(stack, block{sumY: y, sumP: s.P, n: 1})
		for len(stack) >= 2 {
			top := stack[len(stack)-1]
			prev := stack[len(stack)-2]
			if top.sumY*float64(prev.n) >= prev.sumY*float64(top.n) {
				break // prev.mean <= top.mean: monotone, stop merging
			}
			stack[len(stack)-2] = block{
				sumY: prev.sumY + top.sumY,
				sumP: prev.sumP + top.sumP,
				n:    prev.n + top.n,
			}
			stack = stack[:len(stack)-1]
		}
	}
	out := make([]IsoPoint, len(stack))
	for i, blk := range stack {
		out[i] = IsoPoint{P: blk.sumP / float64(blk.n), Y: blk.sumY / float64(blk.n)}
	}
	return out
}

// IsotonicApply evaluates a fitted isotonic map at p by piecewise-linear
// interpolation between block centers, clamped to the endpoint values
// outside the fitted range. Returns clamped p unchanged for an empty fit.
func IsotonicApply(p float64, fit []IsoPoint) float64 {
	if len(fit) == 0 {
		return clampP(p)
	}
	if p <= fit[0].P {
		return fit[0].Y
	}
	if p >= fit[len(fit)-1].P {
		return fit[len(fit)-1].Y
	}
	// Binary search for the enclosing segment fit[i-1].P < p < fit[i].P.
	lo, hi := 0, len(fit)-1
	for hi-lo > 1 {
		mid := (lo + hi) / 2
		if fit[mid].P <= p {
			lo = mid
		} else {
			hi = mid
		}
	}
	p0, y0 := fit[lo].P, fit[lo].Y
	p1, y1 := fit[hi].P, fit[hi].Y
	if p1 <= p0 {
		return y1
	}
	t := (p - p0) / (p1 - p0)
	return y0 + t*(y1-y0)
}
