package estq

type Produto struct {
	Nome  string
	Preco int
}

func Rotulo(p Produto) string {
	return p.Nome + ": " + Moeda(p.Preco) + " cada"
}
