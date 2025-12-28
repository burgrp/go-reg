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

	reader, _ := reg.ConsumeNumber(registers, "test.a")

	fmt.Println(<-reader)
}
