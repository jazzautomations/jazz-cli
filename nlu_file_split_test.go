package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// file_split: "split file X into pieces per N lines" — corpus BUG_doable.
// O parse acha target+lines; o run produz chunks x<aa> confinados e a
// verificação confere existência + soma de linhas da origem.
func TestSplitParse(t *testing.T) {
	w := &nuWorld{}
	for _, tc := range []struct {
		text  string
		wantT string
		wantL string
	}{
		{`split file data.csv into pieces per 100 lines`, "data.csv", "100"},
		{`split server.js into chunks of 50 lines`, "server.js", "50"},
		{`split the app.log every 500 lines`, "app.log", "500"},
		{`dividir arquivo data.csv em partes de 100 linhas`, "data.csv", "100"},
		{`divide o app.log em pedaços de 200 linhas`, "app.log", "200"},
		{`divide o arquivo texto.txt a cada 1000 linhas`, "texto.txt", "1000"},
	} {
		in := nuGet().Parse(tc.text, w)
		if in.Name != "file_split" {
			t.Fatalf("%q -> %s (%.2f, %v), quero file_split", tc.text, in.Name, in.Confidence, in.Evidence)
		}
		if in.Slots["target"] != tc.wantT || in.Slots["lines"] != tc.wantL {
			t.Fatalf("%q -> slots=%v, quero target=%q lines=%q", tc.text, in.Slots, tc.wantT, tc.wantL)
		}
	}
}

// "4 pieces"/"partes" é quantidade, não critério de linhas: vai pro slot
// pieces e recusa honesto (ou pede as linhas), nunca vira lines=4.
func TestSplitPiecesNotLines(t *testing.T) {
	w := &nuWorld{}
	for _, tc := range []struct {
		text  string
		wantP string
	}{
		{`split data.csv into 4 pieces`, "4"},
		{`divide data.csv em 4 partes`, "4"},
	} {
		in := nuGet().Parse(tc.text, w)
		if in.Name != "file_split" {
			t.Fatalf("%q -> %s (%.2f), quero file_split", tc.text, in.Name, in.Confidence)
		}
		if in.Slots["lines"] != "" {
			t.Fatalf("%q -> lines=%q: número de partes não é contagem de linhas", tc.text, in.Slots["lines"])
		}
		if in.Slots["pieces"] != tc.wantP {
			t.Fatalf("%q -> pieces=%q, quero %q", tc.text, in.Slots["pieces"], tc.wantP)
		}
	}
}

// Run: 250 linhas por 100 → xaa/xab/xac em data.csv.parts/, verificado.
func TestSplitRunChunks(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 250; i++ {
		b.WriteString("linha de dados\n")
	}
	root := brFixture(t, "data.csv", b.String())
	env, _ := brTestEnv(t, root, nil, 0)
	r := brRun(context.Background(), env, "split file data.csv into pieces per 100 lines")
	if r.Status != tkSucceeded {
		t.Fatalf("status=%s\n%s", r.Status, r.Text)
	}
	dir := filepath.Join(root, "data.csv.parts")
	for _, name := range []string{"xaa", "xab", "xac"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("chunk %s não existe: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "xad")); err == nil {
		t.Fatal("xad não devia existir — 250/100 dá 3 pedaços")
	}
	nl := func(p string) int {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(b), "\n")
	}
	if got := nl(filepath.Join(dir, "xaa")); got != 100 {
		t.Fatalf("xaa tem %d linhas, quero 100", got)
	}
	if got := nl(filepath.Join(dir, "xac")); got != 50 {
		t.Fatalf("xac tem %d linhas, quero 50", got)
	}
}

// Alvo fora do root: recusado, nada produzido — nunca verde.
func TestSplitUnsafeTargetRefused(t *testing.T) {
	root := brFixture(t)
	env, _ := brTestEnv(t, root, nil, 0)
	for _, text := range []string{
		`split file ../../etc/passwd into pieces per 100 lines`,
		`split file /etc/hostname into pieces per 100 lines`,
	} {
		r := brRun(context.Background(), env, text)
		if r.Status == tkSucceeded {
			t.Fatalf("%q não pode sair verde\n%s", text, r.Text)
		}
	}
	ents, err := filepath.Glob(filepath.Join(root, "*.parts"))
	if err == nil && len(ents) > 0 {
		t.Fatalf("produziu %v pra alvo inseguro", ents)
	}
}

// Pedido sem critério de linhas: pergunta (needs_input) ou miss honesto,
// nunca pedaços de tamanho inventado.
func TestSplitNoLinesAsk(t *testing.T) {
	root := brFixture(t, "data.csv", "a\nb\nc\n")
	env, _ := brTestEnv(t, root, nil, 0)
	r := brRun(context.Background(), env, "split file data.csv into pieces")
	if r.Status == tkSucceeded {
		t.Fatalf("sem critério de linhas não pode sair verde\n%s", r.Text)
	}
	if r.Status != tkNeedsInput && !r.Miss {
		t.Fatalf("status=%s — esperava needs_input/miss honesto\n%s", r.Status, r.Text)
	}
}

// "into 4 pieces" é divisão por quantidade: recusa honesta nomeando o limite.
func TestSplitPiecesRefused(t *testing.T) {
	root := brFixture(t, "data.csv", "a\nb\nc\n")
	env, _ := brTestEnv(t, root, nil, 0)
	r := brRun(context.Background(), env, "split data.csv into 4 pieces")
	if r.Status == tkSucceeded {
		t.Fatalf("divisão por quantidade não pode sair verde\n%s", r.Text)
	}
	// a recusa nomeia o critério suportado (linhas); needs_input/miss também é honesto
	if r.Status != tkNeedsInput && !r.Miss && !strings.Contains(r.Text, "linhas") {
		t.Fatalf("status=%s text=%q — esperava recusa explicando 'só por linhas'", r.Status, r.Text)
	}
}
