package catlog

func CalcPreco(itens []int) int {
	t := 0
	for _, v := range itens {
		t += v
	}
	return t
}
