package main

import "fmt"

func main() {
	fmt.Printf("servidor na porta %d\n", Porta)
	if MostrarLog {
		fmt.Println("[log] subindo nimbus")
	}
}
