package minisoma

import "testing"

func TestSoma(t *testing.T) {
	if got := Soma([]int{1, 2, 3}); got != 6 {
		t.Errorf("Soma(1,2,3) = %d, quero 6", got)
	}
	if got := Soma(nil); got != 0 {
		t.Errorf("Soma(nil) = %d, quero 0", got)
	}
	if got := Soma([]int{-5, 5}); got != 0 {
		t.Errorf("Soma(-5,5) = %d, quero 0", got)
	}
}
