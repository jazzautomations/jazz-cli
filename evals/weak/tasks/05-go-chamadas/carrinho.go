package estq

func Total(ps []Produto) string {
	s := 0
	for _, p := range ps {
		s += p.Preco
	}
	return Moeda(s)
}
