package main

import (
	"fmt"
	"os"
)

func main() {
	nome := "mundo"
	if len(os.Args) > 1 {
		nome = os.Args[1]
	}
	fmt.Println("Ola, " + nome + "!")
}
