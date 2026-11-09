package palavras

import "strings"

// RevPalavras retorna a frase com CADA PALAVRA em ordem invertida,
// mantendo os espacos como estao na entrada.
// Ex.: RevPalavras("oi bom dia") == "io mob aid".
// Palavra e qualquer sequencia separada por espacos.
func RevPalavras(s string) string {
	palavras := strings.Split(s, " ")
	out := make([]string, len(palavras))
	for i, p := range palavras {
		out[i] = p
	}
	return strings.Join(out, " ")
}
