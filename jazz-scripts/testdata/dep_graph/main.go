package main

import (
	"fmt"

	"example.com/demo/internal/core"
	toolkit "example.org/toolkit/log"
)

func main() { fmt.Println(core.Run(), toolkit.X) }
