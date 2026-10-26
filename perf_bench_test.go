package main

// Benchmarks of the low-level study (docs/research/low-level-performance.md,
// §0 A4–A10 and the top 10). Run on the reference machine with:
//
//	GOOS=linux GOARCH=386 go test -c -o jazz-386.test .
//	./jazz-386.test -test.run XXX -test.bench 'NiBuild|NiResolve|FzDist|FzIntent|FpMatch|FzKey' -test.benchmem

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// pbRepo5k writes the synthetic 5k-file tree of the study: 50 packages x 7
// subfolders x 14 files named modulo_calculo_NNNN.go (4 900 files + a few
// tests and docs).
func pbRepo5k(tb testing.TB) string {
	tb.Helper()
	root := tb.TempDir()
	n := 0
	for p := 0; p < 50; p++ {
		for s := 0; s < 7; s++ {
			dir := filepath.Join(root, fmt.Sprintf("pacote%02d", p), fmt.Sprintf("sub%d", s))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				tb.Fatal(err)
			}
			for f := 0; f < 14; f++ {
				name := fmt.Sprintf("modulo_calculo_%04d.go", n)
				if f == 13 {
					name = fmt.Sprintf("modulo_calculo_%04d_test.go", n)
				}
				body := fmt.Sprintf("package sub%d\n\nfunc CalculaValor%04d() int { return %d }\n", s, n, n)
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					tb.Fatal(err)
				}
				n++
			}
		}
	}
	os.WriteFile(filepath.Join(root, "README.md"), []byte("# repo\n"), 0o644)
	return root
}

// pbPairs are typo pairs of the kinds the resolvers see (folded keys).
var pbPairs = [][2]string{
	{"modulocalculo0042", "modulocalculo0024"},
	{"modlecalculo", "modulocalculo"},
	{"procrua", "procura"},
	{"tesets", "testes"},
	{"stauts", "status"},
	{"codgio", "codigo"},
	{"readme", "raedme"},
	{"calculadora", "calculdora"},
	{"configuracao", "configuraçao"},
	{"parsee", "parser"},
}

func BenchmarkFzDist(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		p := pbPairs[i%len(pbPairs)]
		fzDist(p[0], p[1], 2*fzEdit)
	}
}

// BenchmarkFzDist16 is the A4 case: two 16-17 letter keys.
func BenchmarkFzDist16(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		fzDist("modulocalculo0042", "modulocalculo0024", 2*fzEdit)
	}
}

func BenchmarkFzKey(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		fzKey("modulo_calculo_0042_test")
	}
}

func BenchmarkFzIntent(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		fzIntent("procrua")
	}
}

func BenchmarkFpMatchTypo(b *testing.B) {
	set := fpBuild("")
	p := fpProject{Git: true, Test: "go test ./..."}
	if _, ok := set.Match("roda os tesets", p, true); !ok {
		b.Log("note: 'roda os tesets' did not match a fast path")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		set.Match("roda os tesets", p, true)
	}
}

func BenchmarkNiBuild5k(b *testing.B) {
	root := pbRepo5k(b)
	niBuild(root) // warm the page cache
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		niBuild(root)
	}
}

func BenchmarkNiResolve5k(b *testing.B) {
	root := pbRepo5k(b)
	ix := niBuild(root)
	tk := niTok{Raw: "modulo_calculo_0042x.go", Name: "modulo_calculo_0042x", Ext: ".go", Shape: "ext"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ix.resolveAll(tk)
	}
}

// BenchmarkNiResolvePlain5k: a typo far from every name (the common "no
// match" case: every file is a rejected candidate).
func BenchmarkNiResolvePlain5k(b *testing.B) {
	root := pbRepo5k(b)
	ix := niBuild(root)
	tk := niTok{Raw: "configuracao", Name: "configuracao", Shape: "plain", Hint: "file"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ix.resolveAll(tk)
	}
}
