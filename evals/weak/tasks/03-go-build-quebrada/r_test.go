package fracao

import "testing"

func TestMedia(t *testing.T) {
	if got := Media([]int{2, 4}); got != 3 {
		t.Errorf("Media(2,4) = %v, quero 3", got)
	}
	if got := Media([]int{1, 2, 3, 5, 9}); got != 4 {
		t.Errorf("Media = %v, quero 4", got)
	}
}
