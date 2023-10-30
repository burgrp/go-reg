package main

import (
	goreg "goreg/pkg/goreg"
	"time"
)

func main() {
	registers, error := goreg.NewRegisters()
	if error != nil {
		panic(error)
	}

	_, writer := goreg.ConsumeNumber(registers, "test.a")

	writer <- 42

	time.Sleep(1 * time.Second)
}
