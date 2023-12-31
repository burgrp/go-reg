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

	reader, writer := goreg.ProvideNumber(registers, "test.a", goreg.Metadata{
		"device": "test",
		"type":   "number",
		"unit":   "°C",
	})

	writer <- 24

	for v := range reader {
		fmt.Println("Register changed to:", v)
	}

}
