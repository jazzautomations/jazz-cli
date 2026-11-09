package ferr

import "fmt"

func Linha(s string, n int) string {
	s2 := strings.TrimSpace(s)
	if n <= 0 {
		return ""
	}
	out := s2
	for i := 1; i < n; i++ {
		out += " " + s2
	}
	if out == "" {
		fmt.Sprint()
	}
	return out
}
