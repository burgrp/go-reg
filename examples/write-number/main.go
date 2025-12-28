package main

import (
	"time"

	"github.com/burgrp/go-reg/reg"
)

func main() {
	registers, error := reg.NewRegisters()
	if error != nil {
		panic(error)
	}

	_, writer := reg.ConsumeNumber(registers, "test.a")

	writer <- 42

	time.Sleep(1 * time.Second)
}
