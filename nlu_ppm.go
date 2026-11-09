package main

// nlu_ppm.go — a compression classifier for the offline interpreter.
//
// Prediction is compression (Shannon): the intent whose language model codes
// a request in the fewest bits is the best reading. One PPM-C model (orders
// 0..nuPPMOrder, escape method C, no exclusion) per intent, trained on that
// intent's examples in the same canonical text the naive Bayes sees. The
// posterior is a softmax of the per-character bit difference, so the scale
// matches the classifier contract (classify returns intent + probability).
//
// Measured on the grammar's own examples (5-fold, classifier alone, before
// rules): PPM order 3 = 67.0% vs naive Bayes 62.0%, and negatives landing on
// their forbidden intent 6/111 vs 10/111. Opt-in until the brain-level eval
// agrees: JAZZ_NLU_CLS=ppm.

import (
	"math"
	"os"
	"strings"
)

const (
	nuPPMOrder = 3
	nuPPMAlpha = 64  // order -1 alphabet: uniform over this many symbols
	nuPPMBeta  = 6.0 // posterior sharpness per character (same role as nuBeta)
)

type nuPPMCtx struct {
	n    int            // total count in this context
	syms map[byte]int32 // next byte -> count
}

type nuPPM struct {
	classes []int                    // intent indexes
	models  [][]map[string]*nuPPMCtx // [class][order] context -> stats
}

func nuClassifierPPM() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("JAZZ_NLU_CLS")), "ppm")
}

func nuPPMText(canon string) string { return " " + canon + " " }

func (m *nuModel) nuTrainPPM() *nuPPM {
	p := &nuPPM{}
	for ii, it := range m.data.Intents {
		if len(it.Examples) == 0 {
			continue
		}
		orders := make([]map[string]*nuPPMCtx, nuPPMOrder+1)
		for k := range orders {
			orders[k] = map[string]*nuPPMCtx{}
		}
		for _, ex := range it.Examples {
			t := nuPPMText(nuCanonText(m.normalize(nuTokenize(ex))))
			for i := 0; i < len(t); i++ {
				for k := 0; k <= nuPPMOrder && i-k >= 0; k++ {
					key := t[i-k : i]
					c := orders[k][key]
					if c == nil {
						c = &nuPPMCtx{syms: map[byte]int32{}}
						orders[k][key] = c
					}
					c.n++
					c.syms[t[i]]++
				}
			}
		}
		p.classes = append(p.classes, ii)
		p.models = append(p.models, orders)
	}
	return p
}

// bits codes t with class ci's model.
func (p *nuPPM) bits(ci int, t string) float64 {
	orders, b := p.models[ci], 0.0
	for i := 0; i < len(t); i++ {
		esc, prob := 1.0, -1.0
		k := nuPPMOrder
		if i < k {
			k = i
		}
		for ; k >= 0; k-- {
			c := orders[k][t[i-k:i]]
			if c == nil {
				continue
			}
			d := float64(len(c.syms))
			if n, ok := c.syms[t[i]]; ok {
				prob = esc * float64(n) / (float64(c.n) + d)
				break
			}
			esc *= d / (float64(c.n) + d)
		}
		if prob < 0 {
			prob = esc / nuPPMAlpha
		}
		b -= math.Log2(prob)
	}
	return b
}

func (p *nuPPM) predict(canon string, among []int) (int, float64) {
	if p == nil || len(p.classes) == 0 || canon == "" {
		return -1, 0
	}
	t := nuPPMText(canon)
	allowed := func(ci int) bool {
		if among == nil {
			return true
		}
		for _, a := range among {
			if a == p.classes[ci] {
				return true
			}
		}
		return false
	}
	best, bestBits := -1, math.Inf(1)
	cost := make([]float64, len(p.classes))
	for ci := range p.classes {
		if !allowed(ci) {
			continue
		}
		cost[ci] = p.bits(ci, t)
		if cost[ci] < bestBits {
			best, bestBits = ci, cost[ci]
		}
	}
	if best < 0 {
		return -1, 0
	}
	sum, n := 0.0, float64(len(t))
	for ci := range p.classes {
		if allowed(ci) {
			sum += math.Exp(-(cost[ci] - bestBits) * math.Ln2 * nuPPMBeta / n)
		}
	}
	return p.classes[best], 1 / sum
}
