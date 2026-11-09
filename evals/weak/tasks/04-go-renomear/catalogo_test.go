package catlog

import "testing"

func TestCalcularPreco(t *testing.T) {
	if got := CalcularPreco([]int{10, 20, 12}); got != 42 {
		t.Errorf("CalcularPreco = %d, quero 42", got)
	}
}
