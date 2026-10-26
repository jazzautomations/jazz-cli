package main

import (
	"math/rand"
	"regexp"
	"strings"
	"testing"
)

// The old implementations, kept here as the reference the fast paths of
// item 9 (low-level study) must reproduce exactly.

func refFzKey(s string) string {
	var b strings.Builder
	for _, r := range fzFoldUnicode(s) {
		switch r {
		case '_', '-', '.', ' ':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func refNiCore(stem string) string {
	for i := 0; i < 2; i++ {
		s := niTestAffix.ReplaceAllString(stem, "")
		if s == "" || s == stem {
			break
		}
		stem = s
	}
	return stem
}

func refFzIntent(w string) string {
	w = fzFold(w)
	if v, ok := fzVerbs[w]; ok {
		return v
	}
	if len([]rune(w)) < 3 {
		return ""
	}
	best, bd, tie := "", 1<<30, false
	for k, v := range fzVerbs {
		if len([]rune(k)) < 4 {
			continue
		}
		if d, ok := fzNearWord(w, k); ok && (len([]rune(w)) >= 5 || d <= fzSlip) {
			switch {
			case d < bd:
				best, bd, tie = v, d, false
			case d == bd && v != best:
				tie = true
			}
		}
	}
	if tie {
		return ""
	}
	return best
}

func refFzRepairLine(s string, vocab map[string]bool) (string, bool) {
	words := strings.Fields(s)
	changed := false
	for i, w := range words {
		f := fzFold(w)
		if len([]rune(f)) < 4 || vocab[f] || strings.ContainsAny(f, "0123456789/\\._@-`'\"") {
			continue
		}
		best, bd, tie := "", 1<<30, false
		for v := range vocab {
			if d, ok := fzNearWord(f, v); ok {
				switch {
				case d < bd:
					best, bd, tie = v, d, false
				case d == bd && v != best:
					tie = true
				}
			}
		}
		if best != "" && !tie {
			words[i] = best
			changed = true
		}
	}
	return strings.Join(words, " "), changed
}

var (
	refExtOK   = regexp.MustCompile(`^[a-z][a-z0-9]{0,5}$`)
	refLineSx  = regexp.MustCompile(`:\d+(?::\d+)?$`)
	refCamel   = regexp.MustCompile(`[a-z0-9][A-Z]`)
	refSnake   = regexp.MustCompile(`[A-Za-z]_[A-Za-z]`)
	refLetters = regexp.MustCompile(`^\p{L}+$`)
	refSafeArg = regexp.MustCompile(`^[\w./:@=+-]+$`)
	refPunct   = regexp.MustCompile(`[\s?!.…;:]+$`)
)

// fpCorpus: random short strings over the characters these tests care
// about, plus hand-picked edge cases.
func fpCorpus(n int) []string {
	out := []string{"", "a", "A", "Ação", "TEST_calc", "calc.test", "spec_x", "ſpec", "Kelvin", "x:1", "x:1:2", "x:1:2:3", "::5", ":5", "1:2",
		"a:b:1", "fooBar", "foo_bar", "_x_", "a_1", "README.md", "e/ou", "rodaa os testes...", "git status?!", "olá…", "abc;:", "x\v", "x\t",
		"Test-Calculo.PY", "çã", "naïve", "ÀÉÎ", "x\x00y", "\xff\xfe", "tests", "Specs", "my_test", "test", "Foo Bar-baz.qux"}
	r := rand.New(rand.NewSource(3))
	al := []rune("abcxyzABCXYZ0129_:.-/ @=+?!…;\t\nçãéſKtTeEsSpPcC")
	for i := 0; i < n; i++ {
		b := make([]rune, r.Intn(12))
		for j := range b {
			b[j] = al[r.Intn(len(al))]
		}
		out = append(out, string(b))
	}
	return out
}

func TestFzFastPathsMatchReference(t *testing.T) {
	for _, s := range fpCorpus(20000) {
		if got, want := fzFold(s), fzFoldUnicode(s); got != want {
			t.Fatalf("fzFold(%q) = %q, want %q", s, got, want)
		}
		if got, want := fzKey(s), refFzKey(s); got != want {
			t.Fatalf("fzKey(%q) = %q, want %q", s, got, want)
		}
		if got, want := niCore(s), refNiCore(s); got != want {
			t.Fatalf("niCore(%q) = %q, want %q", s, got, want)
		}
	}
	if n := testing.AllocsPerRun(100, func() { fzKey("modulocalculo0042"); fzFold("abre") }); n != 0 {
		t.Errorf("fzKey/fzFold of folded ASCII allocate %v times", n)
	}
}

func TestNiCharTestsMatchRegexps(t *testing.T) {
	for _, s := range fpCorpus(20000) {
		if niExtOK(s) != refExtOK.MatchString(s) {
			t.Fatalf("niExtOK(%q)", s)
		}
		if got, want := niStripLine(s), refLineSx.ReplaceAllString(s, ""); got != want {
			t.Fatalf("niStripLine(%q) = %q, want %q", s, got, want)
		}
		if niCamel(s) != refCamel.MatchString(s) || niSnake(s) != refSnake.MatchString(s) || niLetters(s) != refLetters.MatchString(s) {
			t.Fatalf("camel/snake/letters(%q)", s)
		}
		if fpSafeArg(s) != refSafeArg.MatchString(s) {
			t.Fatalf("fpSafeArg(%q)", s)
		}
		if strings.TrimRightFunc(s, fpPunct) != refPunct.ReplaceAllString(s, "") {
			t.Fatalf("fpPunct(%q)", s)
		}
	}
}

func TestFzIntentAndRepairMatchReference(t *testing.T) {
	set := fpBuild("")
	words := append(fpCorpus(3000), "procrua", "tesets", "concerat", "rodda", "exectua", "abri", "mostar", "instlaa", "explcia", "busac", "status", "stauts")
	for k := range fzVerbs {
		words = append(words, k, k+"s", k[1:], strings.ToUpper(k))
	}
	for _, w := range words {
		if got, want := fzIntent(w), refFzIntent(w); got != want {
			t.Fatalf("fzIntent(%q) = %q, want %q", w, got, want)
		}
	}
	lines := []string{"roda os tesets", "git stauts", "procra X no codgio", "mostar o README", "rodda tetses agora", "explcia o erro"}
	for _, w := range words {
		lines = append(lines, "roda "+w, w+" os testes")
	}
	for _, vocab := range []map[string]bool{set.vocab, rtVocab} {
		for _, l := range lines {
			g1, c1 := fzRepairLine(l, vocab)
			g2, c2 := refFzRepairLine(l, vocab)
			if g1 != g2 || c1 != c2 {
				t.Fatalf("fzRepairLine(%q) = %q %v, want %q %v", l, g1, c1, g2, c2)
			}
		}
	}
}

func TestQgIsTestFilePrefilter(t *testing.T) {
	ref := regexp.MustCompile(qgTestFileRE.String())
	cases := append(fpCorpus(5000), "a_test.go", "pkg/tests/x.go", "Foo.Test.tsx", "FooTests.java", "spec/a.rb", "__tests__/x.js", "x_spec.rb", "test_x.py", "TEST_X.PY", "src/main.go")
	for _, s := range cases {
		if qgIsTestFile(s) != ref.MatchString(s) {
			t.Fatalf("qgIsTestFile(%q)", s)
		}
	}
}
