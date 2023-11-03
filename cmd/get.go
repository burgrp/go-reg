package cmd

import (
	"fmt"
	"goreg/pkg/goreg"

	"github.com/spf13/cobra"
)

var GetCmd = &cobra.Command{
	Use:   "get <register>",
	Short: "Read a register",
	Long: `Reads the specified register.
With --stay flag, the command will remain connected and write any changes to stdout.`,
	RunE: runGet,
}

func init() {
	RootCmd.AddCommand(GetCmd)
	GetCmd.Flags().BoolP("stay", "s", false, "Stay connected and write changes to stdout")
	GetCmd.Args = cobra.ExactArgs(1)
}

func runGet(cmd *cobra.Command, args []string) error {

	name := args[0]
	stay, err := cmd.Flags().GetBool("stay")
	if err != nil {
		return err
	}

	registers, err := goreg.NewRegisters()
	if err != nil {
		return err
	}
	reader, _ := goreg.Consume(registers, name, json_serializer, json_deserializer)

	for {
		value := <-reader
		fmt.Println(value)
		if !stay {
			break
		}
	}
	return nil
}
