package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"goreg/pkg/goreg"
	"os"
	"time"

	"github.com/spf13/cobra"
)

var regCmd = &cobra.Command{
	Use:   "reg",
	Short: "Read, write or monitor registers",
	Long: `use:
  reg 						- watch for register changes
  reg <name>                - read a single register
  reg <name> <value>        - write a single register to the given value
  reg <name> <meta> <value> - create new read only register with the given metadata (as JSON) and value
  reg <name> <meta> -       - create new read write register; read values from stdin; write values to stdout

  The tool expects MQTT environment variable to be set and pointing to a valid MQTT broker host or host:port.
  `,
	Run: func(cmd *cobra.Command, args []string) {
		var err error
		if len(args) == 0 {
			err = watchRegisters()
		} else if len(args) == 1 {
			name := args[0]
			err = readRegister(name)
		} else if len(args) == 2 {
			name := args[0]
			value := args[1]
			err = writeRegister(name, value)
		} else if len(args) == 3 {
			name := args[0]
			meta := args[1]
			value := args[2]
			if value == "-" {
				err = createRegisterRW(name, meta)
			} else {
				err = createRegisterRO(name, meta, value)
			}

		} else {
			cmd.Help()
		}
		if err != nil {
			os.Stderr.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))
			os.Exit(1)
		}
	},
}

func watchRegisters() error {

	registers, err := goreg.NewRegisters()
	if err != nil {
		return err
	}

	metadata, values := goreg.Watch(registers)

	for {
		select {
		case meta := <-metadata:
			m := ""
			for k, v := range meta.Metadata {
				m += fmt.Sprintf(" %s:%s", k, v)
			}
			println(meta.Name + " -" + m)
		case value := <-values:
			println(value.Name + " = " + string(value.Value))
		}
	}

}

func readRegister(name string) error {

	registers, err := goreg.NewRegisters()
	if err != nil {
		return err
	}

	reader, _ := goreg.Consume(registers, name, json_serializer, json_deserializer)

	value := <-reader

	println(value)

	return nil
}

func writeRegister(name string, value string) error {

	registers, err := goreg.NewRegisters()
	if err != nil {
		return err
	}

	reader, writer := goreg.Consume(registers, name, json_serializer, json_deserializer)

	writer <- value

	for v := range reader {
		if v == value {
			break
		}
	}

	return nil
}

func createRegisterRO(name string, meta string, value string) error {
	registers, err := goreg.NewRegisters()
	if err != nil {
		return err
	}

	metadata := map[string]string{}
	err = json.Unmarshal([]byte(meta), &metadata)
	if err != nil {
		return err
	}

	_, writer := goreg.Provide(registers, name, json_serializer, json_deserializer, metadata)
	writer <- value

	for {
		time.Sleep(100 * time.Hour)
	}
}

func createRegisterRW(name string, meta string) error {

	registers, err := goreg.NewRegisters()
	if err != nil {
		return err
	}

	metadata := map[string]string{}
	err = json.Unmarshal([]byte(meta), &metadata)
	if err != nil {
		return err
	}

	reader, writer := goreg.Provide(registers, name, json_serializer, json_deserializer, metadata)
	go func() {
		for {
			v := <-reader
			println(v)
		}
	}()

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		v := scanner.Text()
		writer <- v
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

func json_serializer(value string) []byte {
	return []byte(value)
}

func json_deserializer(value []byte) string {
	return string(value)
}

func main() {
	regCmd.Execute()
}
