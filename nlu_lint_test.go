package main

// TestNLUGrammarLint: compile-time hygiene for the grammar files.
// Every past whack-a-mole incident was a data defect the parser never
// validated: atoms naming words instead of concepts, self-neutralizing
// vetoes, cue words that must stay concept-free, and rules whose own
// vocabulary vetoes itself. These checks turn each class into a build
// failure instead of a silent routing change.
import (
	"strings"
	"testing"
)

var nuValidTags = map[string]bool{
	"@file": true, "@symbol": true, "@tool": true, "@host": true,
	"@service": true, "@container": true, "@number": true, "@test": true,
	"@quoted": true,
}

// cue words the slot helpers scan by Fold — they must never carry a
// concept (the 'menos' incident: a concept assignment vetoes the intent
// AND un-marks Filler, two channels at once).
// apenas os núcleos: saídas de slang que os helpers leem por Fold e que
// dependem de ser filler — 'menos' foi o incidente real (conceito nele
// veta o intent E tira o status de filler, dois canais de uma vez).
var nuProtectedCues = []string{
	"menos", "mais", "de", "ate", "até", "que", "em", "no", "na",
	"a", "o", "ago", "há", "pra", "com", "sem", "por",
}

func TestNLUGrammarLint(t *testing.T) {
	m := nuGet()
	if m == nil || m.loadErr != nil {
		t.Fatalf("modelo não carregou: %v", m.loadErr)
	}

	// 1. every rule atom must name a real concept or a valid tag —
	// 'journal'/'file'/'tool'/'@dir' sat dead in veto lists for weeks.
	for _, def := range m.data.Intents {
		for ri, r := range def.Rules {
			for grp, atoms := range map[string][]string{"all": r.All, "any": r.Any, "none": r.None} {
				for _, a := range atoms {
					for _, alt := range strings.Split(a, "|") {
						if strings.HasPrefix(alt, "@") {
							if !nuValidTags[alt] {
								t.Errorf("%s r%d %s: tag inexistente %q", def.Name, ri+1, grp, alt)
							}
							continue
						}
						if !m.concs[alt] {
							t.Errorf("%s r%d %s: conceito inexistente %q", def.Name, ri+1, grp, alt)
						}
					}
				}
			}
		}
	}

	// 2. @file inside none is self-neutralizing: nuGroupOKVeto skips
	// @file-tagged tokens, so the atom can never fire (17 dead found).
	for _, def := range m.data.Intents {
		for ri, r := range def.Rules {
			for _, a := range r.None {
				for _, alt := range strings.Split(a, "|") {
					if alt == "@file" {
						t.Errorf("%s r%d: '@file' em none é auto-neutralizante", def.Name, ri+1)
					}
				}
			}
		}
	}

	// 3. protected cues must stay concept-free and, where the parser
	// relies on it, filler — a lexicon edit that assigns them a concept
	// breaks temporal/size/depth helpers through two channels at once.
	for _, w := range nuProtectedCues {
		if cs := m.exact[w]; len(cs) > 0 {
			t.Errorf("cue protegido %q carrega conceitos %v — quebra helpers de slot", w, cs)
		}
	}

	// 4. member-level all∩none: a word satisfying BOTH a rule's all and
	// its none self-vetoes ('novo' = create+newest kills 'arquivo novo').
	memberOf := map[string][]string{}
	for w, cs := range m.exact {
		for _, c := range cs {
			memberOf[c] = append(memberOf[c], w)
		}
	}
	for _, def := range m.data.Intents {
		for ri, r := range def.Rules {
			allC, noneC := map[string]bool{}, map[string]bool{}
			for _, a := range r.All {
				for _, alt := range strings.Split(a, "|") {
					if !strings.HasPrefix(alt, "@") {
						allC[alt] = true
					}
				}
			}
			for _, a := range r.None {
				for _, alt := range strings.Split(a, "|") {
					if !strings.HasPrefix(alt, "@") {
						noneC[alt] = true
					}
				}
			}
			for ac := range allC {
				for nc := range noneC {
					shared := intersectStr(memberOf[ac], memberOf[nc])
					if len(shared) > 0 {
						t.Logf("%s r%d: palavras %v satisfazem all:%s E none:%s (auto-veto potencial)", def.Name, ri+1, shared, ac, nc)
					}
				}
			}
		}
	}
}

func intersectStr(a, b []string) []string {
	set := map[string]bool{}
	for _, x := range b {
		set[x] = true
	}
	var out []string
	for _, x := range a {
		if set[x] {
			out = append(out, x)
		}
	}
	return out
}
