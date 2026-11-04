package main

// Item 5 (onda 1): an intent held up only by a typo-repaired word drops to
// conf <= 0.5 (under nuMinConf and brLevel0Min) and is flagged TypoOnly, so
// the brain offers a one-key confirmation instead of acting blind (principle
// 1) or weakening a guardrail (principle 2). Kept in its own file so it does
// not collide with the lexicon item's nlu_test.go.

import (
	"context"
	"strings"
	"testing"
)

func TestNLUTypoOnlyDiscount(t *testing.T) {
	m, w := nuTestWorld(t)

	// Genuine typo-only: a rule fires, but the single concept it needs comes
	// only from a word we repaired. Confidence is capped under nuMinConf and
	// the reading is flagged. 'tesr'->test (run_tests), 'explca'->explica
	// (explain), 'portaa'/'porat'->porta (list_ports) are each one word, one
	// slip, no other signal — the "1 slip de 'porta'" case from the spec.
	for _, s := range []string{"tesr", "explca", "portaa", "porat"} {
		in := m.Parse(s, w)
		if !in.TypoOnly || in.Confidence >= nuMinConf {
			t.Errorf("%q: TypoOnly=%v conf=%.2f, queria TypoOnly e conf<%.2f (%v)",
				s, in.TypoOnly, in.Confidence, nuMinConf, in.Evidence)
		}
	}

	// NÃO 'tesk': it lands on run_tests through the classifier (no repair
	// fired), so it is not typo-only and keeps its own confidence.
	if in := m.Parse("tesk", w); in.TypoOnly {
		t.Errorf("'tesk' não deveria ser typo-only (conf %.2f, %v)", in.Confidence, in.Evidence)
	}

	// A correctly-spelled verb carries the intent; only the object was garbled.
	if in := m.Parse("roda os tets", w); !in.Understood() || in.TypoOnly {
		t.Errorf("'roda os tets' deveria ser entendido sem typo-only (understood=%v TypoOnly=%v conf %.2f)",
			in.Understood(), in.TypoOnly, in.Confidence)
	}

	// A named entity corroborates the repaired verb, so the intent stands even
	// though the rule's grammar did not formally consume the entity.
	if in := m.Parse("explca o parser.go", w); !in.Understood() || in.TypoOnly {
		t.Errorf("'explca o parser.go' deveria ser entendido (a entidade corrobora): understood=%v TypoOnly=%v (%v)",
			in.Understood(), in.TypoOnly, in.Evidence)
	}

	// 'tets' is a transposition beyond the repair budget: no repair fires, so
	// it is not flagged TypoOnly but it is still never acted on — an honest
	// miss (DEPOIS: "confirmação barata ou honest-miss").
	if in := m.Parse("tets", w); in.Understood() {
		t.Errorf("'tets' deveria ser honest-miss (não entendido), veio understood conf %.2f", in.Confidence)
	}
}

func TestBrainTypoOnlyOffersConfirm(t *testing.T) {
	root := brFixture(t)

	// Interactive: env.Ask is present and the user confirms (answer 0). The
	// confirmation counts as exactly one question and the intent then runs.
	env, rec := brTestEnv(t, root, map[string]brFakeOut{"go test": {"ok  \tx\t0.01s\n", 0}}, 0)
	env.World = nuWorldFromDef(nuGet().data.World)
	r := brRun(context.Background(), env, "tesr")
	if len(rec.asked) != 1 {
		t.Fatalf("queria 1 pergunta de confirmação, veio %d: %v", len(rec.asked), rec.asked)
	}
	if !r.Handled || r.Status != tkSucceeded {
		t.Fatalf("após confirmar o intent deveria rodar: handled=%v status=%v text=%q", r.Handled, r.Status, r.Text)
	}
	if r.Questions != 1 {
		t.Errorf("r.Questions = %d, queria 1", r.Questions)
	}
	if len(rec.cmds) == 0 {
		t.Errorf("nenhum comando rodou após confirmar")
	}

	// Headless (-p): no env.Ask, so nothing is asked and nothing runs — it
	// falls into brNotUnderstood, which prints "talvez X, confiança 0.50".
	env2, rec2 := brTestEnv(t, root, nil, 0)
	env2.World = nuWorldFromDef(nuGet().data.World)
	env2.Ask = nil
	r2 := brRun(context.Background(), env2, "tesr")
	if r2.Handled {
		t.Errorf("headless não deveria executar um typo-only: %q", r2.Text)
	}
	if !strings.Contains(r2.Text, "confiança 0.50") {
		t.Errorf("headless deveria cair em brNotUnderstood com 'confiança 0.50', veio %q", r2.Text)
	}
	if len(rec2.cmds) != 0 {
		t.Errorf("headless não deveria rodar comando: %v", rec2.cmds)
	}
}
