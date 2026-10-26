package main

import (
	"math/rand"
	"testing"
)

// fzDistCases: every pair the fuzzy tests and resolvers care about, plus
// edge shapes (empty, long, digits, uppercase, separators).
func fzDistCases() [][2]string {
	c := append([][2]string{}, pbPairs...)
	c = append(c, [][2]string{
		{"", ""}, {"", "abc"}, {"abc", ""}, {"a", "s"}, {"modle", "model"}, {"parsee", "parser"}, {"parsee", "parsel"},
		{"git", "gti"}, {"status", "stauts"}, {"roda", "rode"}, {"qwerty", "wqerty"}, {"1234", "1243"},
		{"ABC", "abc"}, {"teste", "tesre"}, {"procura", "procrua"}, {"buildtarget", "buildtraget"},
		{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab"},
		{"x", "y"}, {"zxcv", "xzcv"}, {"pacote01sub3", "pacote10sub3"}, {"go", "og"},
	}...)
	for k := range fzVerbs {
		c = append(c, [2]string{"procrua", k}, [2]string{"tesets", k}, [2]string{"conserat", k})
	}
	return c
}

func TestFzDistASCIIEqualsRunes(t *testing.T) {
	for _, p := range fzDistCases() {
		for _, max := range []int{0, fzSlip, fzEdit, fzEdit + fzSlip, 2 * fzEdit, 3 * fzEdit, 100} {
			for _, q := range [][2]string{p, {p[1], p[0]}} {
				if got, want := fzDist(q[0], q[1], max), fzDistRunes(q[0], q[1], max); got != want {
					t.Errorf("fzDist(%q,%q,%d): ascii %d, runes %d", q[0], q[1], max, got, want)
				}
			}
		}
	}
	// random pairs over the keyboard alphabet (dense neighbours) and digits
	r := rand.New(rand.NewSource(7))
	const al = "qwertyuiopasdfghjklzxcvbnm1234567890_-.QAZ"
	word := func() string {
		b := make([]byte, r.Intn(20))
		for i := range b {
			b[i] = al[r.Intn(len(al))]
		}
		return string(b)
	}
	for i := 0; i < 20000; i++ {
		a := word()
		b := []byte(a)
		for k := r.Intn(4); k > 0 && len(b) > 0; k-- { // a few edits so many pairs are near
			b[r.Intn(len(b))] = al[r.Intn(len(al))]
		}
		if r.Intn(3) == 0 {
			b = []byte(word())
		}
		max := r.Intn(4 * fzEdit)
		if got, want := fzDistASCII(a, string(b), max), fzDistRunes(a, string(b), max); got != want {
			t.Fatalf("fzDist(%q,%q,%d): ascii %d, runes %d", a, b, max, got, want)
		}
	}
	// non-ASCII and long inputs go through the rune path unchanged
	if fzDist("configuraçao", "configuracao", 100) != fzDistRunes("configuraçao", "configuracao", 100) {
		t.Error("unicode fallback differs")
	}
}

func TestFzDistNoAlloc(t *testing.T) {
	if n := testing.AllocsPerRun(100, func() { fzDist("modulocalculo0042", "modulocalculo0024", 2*fzEdit) }); n != 0 {
		t.Errorf("fzDist ASCII allocates %v times", n)
	}
}

func FuzzFzDist(f *testing.F) {
	for _, p := range fzDistCases() {
		f.Add(p[0], p[1], 2*fzEdit)
	}
	f.Fuzz(func(t *testing.T, a, b string, max int) {
		if len(b) > fzRowMax || max < 0 || max > 1<<20 || !fzIsASCII(a) || !fzIsASCII(b) {
			return
		}
		if got, want := fzDistASCII(a, b, max), fzDistRunes(a, b, max); got != want {
			t.Fatalf("fzDist(%q,%q,%d): ascii %d, runes %d", a, b, max, got, want)
		}
	})
}

// TestNiBoundIsLowerBound: the resolver's filter never rejects a pair whose
// real distance is within the limit.
func TestNiBoundIsLowerBound(t *testing.T) {
	check := func(a, b string) {
		ma, la := niMask(a)
		mb, lb := niMask(b)
		bound := niBound(ma, la, mb, lb)
		if d := fzDistRunes(a, b, 1<<20); bound > d {
			t.Fatalf("niBound(%q,%q) = %d > dist %d", a, b, bound, d)
		}
	}
	for _, p := range fzDistCases() {
		check(p[0], p[1])
		check(p[1], p[0])
	}
	r := rand.New(rand.NewSource(11))
	const al = "qwertyuiopasdfghjklzxcvbnm1234567890çã"
	ral := []rune(al)
	word := func() string {
		b := make([]rune, r.Intn(18))
		for i := range b {
			b[i] = ral[r.Intn(len(ral))]
		}
		return string(b)
	}
	for i := 0; i < 30000; i++ {
		a := word()
		b := []rune(a)
		for k := r.Intn(4); k > 0 && len(b) > 0; k-- {
			switch r.Intn(3) {
			case 0:
				b[r.Intn(len(b))] = ral[r.Intn(len(ral))]
			case 1:
				j := r.Intn(len(b))
				b = append(b[:j], b[j+1:]...)
			default:
				j := r.Intn(len(b))
				b = append(b[:j], append([]rune{ral[r.Intn(len(ral))]}, b[j:]...)...)
			}
		}
		check(a, string(b))
	}
}

func FuzzNiBound(f *testing.F) {
	for _, p := range fzDistCases() {
		f.Add(p[0], p[1])
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		if len(a) > 40 || len(b) > 40 {
			return
		}
		a, b = fzKey(a), fzKey(b)
		ma, la := niMask(a)
		mb, lb := niMask(b)
		if bound, d := niBound(ma, la, mb, lb), fzDistRunes(a, b, 1<<20); bound > d {
			t.Fatalf("niBound(%q,%q) = %d > dist %d", a, b, bound, d)
		}
	})
}
