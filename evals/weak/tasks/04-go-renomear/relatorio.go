package catlog

import "fmt"

func Relatorio(itens []int) string {
	return fmt.Sprintf("total: %d", CalcPreco(itens))
}
