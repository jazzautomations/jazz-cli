package estq

import "fmt"

// Moeda formata um valor em centavos como Real.
func Moeda(c int) string {
	return fmt.Sprintf("R$ %d", c)
}
