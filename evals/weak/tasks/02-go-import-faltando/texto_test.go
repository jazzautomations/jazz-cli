package ferr

import "testing"

func TestLinha(t *testing.T) {
	if got := Linha("abe ", 2); got != "abe abe" {
		t.Errorf("Linha = %q, quero %q", got, "abe abe")
	}
}
