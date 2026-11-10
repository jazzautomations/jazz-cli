package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// contract_sort_test.go — sort_op.print imprime ordenado e não toca no
// arquivo: a prova de que o refinamento do portão (text_op sem destino =
// leitura) descreve a verdade.
// sort_op.print imprime ordenado e não toca no arquivo — a prova de que o
// refinamento do portão (text_op sem destino = leitura) descreve a verdade.
func TestBrainSortPrintIsReadOnly(t *testing.T) {
	root := brFixture(t, "dados.txt", "b\na\nc\n")
	env, _ := brTestEnv(t, root, nil, -1)
	r := brRun(context.Background(), env, "ordena o dados.txt")
	if r.Status == tkFailed {
		t.Fatalf("%s:\n%s", r.Status, r.Text)
	}
	got, err := os.ReadFile(filepath.Join(root, "dados.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "b\na\nc\n" {
		t.Errorf("sort_op.print alterou o arquivo: %q", got)
	}
	// a saída ordenada foi conferida pelo verificador do método — a prova de
	// que o step é leitura mesmo quando o resultado é validado
	if !strings.Contains(r.Text, "ordenada conferida") {
		t.Errorf("resposta não mostra a saída ordenada verificada:\n%s", r.Text)
	}
}
