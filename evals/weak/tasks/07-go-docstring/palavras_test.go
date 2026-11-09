package palavras

import "testing"

func TestRevPalavras(t *testing.T) {
	casos := []struct{ in, want string }{
		{"oi bom dia", "io mob aid"},
		{"aba", "aba"},
		{"", ""},
		{"a  b", "a  b"},
	}
	for _, c := range casos {
		if got := RevPalavras(c.in); got != c.want {
			t.Errorf("RevPalavras(%q) = %q, quero %q", c.in, got, c.want)
		}
	}
}
