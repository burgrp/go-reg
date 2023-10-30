package main

import (
	goreg "goreg/pkg/goreg"
)

func main() {
	registers, error := goreg.NewRegisters()
	if error != nil {
		panic(error)
	}

	reader, _ := goreg.ConsumeNumber(registers, "test.a")

	println(<-reader)
}
