package main

import (
	"fmt"
	goreg "goreg/pkg/goreg"
)

func main() {
	registers, error := goreg.NewRegisters()
	if error != nil {
		panic(error)
	}

	reader, _ := goreg.ConsumeNumber(registers, "test.a")

	fmt.Println(<-reader)
}
