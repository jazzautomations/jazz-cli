package estq

import "testing"

func TestMoeda(t *testing.T) {
	if got := Moeda(1500); got != "R$ 15,00" {
		t.Errorf("Moeda(1500) = %q, quero %q", got, "R$ 15,00")
	}
	if got := Moeda(805); got != "R$ 8,05" {
		t.Errorf("Moeda(805) = %q, quero %q", got, "R$ 8,05")
	}
	if got := Moeda(5); got != "R$ 0,05" {
		t.Errorf("Moeda(5) = %q, quero %q", got, "R$ 0,05")
	}
}

func TestRotulo(t *testing.T) {
	if got := Rotulo(Produto{Nome: "Parafuso", Preco: 250}); got != "Parafuso: R$ 2,50 cada" {
		t.Errorf("Rotulo = %q", got)
	}
}
