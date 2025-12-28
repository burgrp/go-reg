package main

import (
	"fmt"

	"github.com/burgrp/go-reg/reg"
)

func main() {
	registers, error := reg.NewRegisters()
	if error != nil {
		panic(error)
	}

	reader, writer := reg.ProvideNumber(registers, "test.a", reg.Metadata{
		"device": "test",
		"type":   "number",
		"unit":   "°C",
	})

	writer <- 24

	for v := range reader {
		fmt.Println("Register changed to:", v)
	}

}
