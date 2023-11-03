package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"goreg/pkg/goreg"
	"os"

	"github.com/spf13/cobra"
)

var provideCmd = &cobra.Command{
	Use:   "provide <name> <meta> [<value>]",
	Short: "Provide a register",
	Long: `Registers are established by advertising them to the network.
The 'meta' argument is a JSON object containing metadata for the register, such as {"device": "My device"}.
You can also specify an initial value as the last argument. If no initial value is provided, the register will be created with a null value.
Values for registers are defined using JSON expressions, such as true, false, 3.14, "hello world," or null.
Additionally, subsequent values can be read from stdin and written to stdout.`,
	RunE: runProvide,
}

func init() {
	RootCmd.AddCommand(provideCmd)
	provideCmd.Flags().BoolP("read-only", "r", false, "Make the register read-only.")
	provideCmd.Args = cobra.RangeArgs(2, 3)
}

func runProvide(cmd *cobra.Command, args []string) error {

	name := args[0]

	metadata := goreg.Metadata{}
	json.Unmarshal([]byte(args[1]), &metadata)

	value := ""
	if len(args) == 3 {
		value = args[2]
	}

	read_only, err := cmd.Flags().GetBool("read-only")
	if err != nil {
		return err
	}

	registers, err := goreg.NewRegisters()
	if err != nil {
		return err
	}
	reader, writer := goreg.Provide(registers, name, json_serializer, json_deserializer, metadata)

	go func() {
		for {
			if !read_only {
				value := <-reader
				writer <- value
				fmt.Println(value)
			}
		}
	}()

	writer <- value

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
