package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"goreg/pkg/goreg"
	"os"
	"time"

	"github.com/spf13/cobra"
)

var setCmd = &cobra.Command{
	Use:   "set <register> <value>",
	Short: "Write a register",
	Long: `Writes the specified register.
With --stay flag, the command will remain connected, read values from stdin and write any changes to stdout.
Values are specified as JSON expressions, e.g. true, false, "3.14", "hello world" or null.`,
	RunE: runSet,
}

func init() {
	rootCmd.AddCommand(setCmd)
	setCmd.Flags().BoolP("stay", "s", false, "Stay connected, read values from stdin and write changes to stdout")
	setCmd.Flags().DurationP("timeout", "t", 5*time.Second, "Timeout for waiting for the register to be set")
	setCmd.Args = cobra.ExactArgs(2)
}

func runSet(cmd *cobra.Command, args []string) error {

	name := args[0]
	stay, err := cmd.Flags().GetBool("stay")
	if err != nil {
		return err
	}

	registers, err := goreg.NewRegisters()
	if err != nil {
		return err
	}
	reader, writer := goreg.Consume(registers, name, json_serializer, json_deserializer)

	desired := args[1]
	writer <- desired

	timeout, error := cmd.Flags().GetDuration("timeout")
	if error != nil {
		return error
	}

	timeout_timer := time.NewTimer(timeout)
	defer timeout_timer.Stop()

Loop:
	for {
		select {
		case <-timeout_timer.C:
			return errors.New("timeout waiting for register to be set")
		case value := <-reader:
			if value == desired {
				break Loop
			}
		}
	}

	if stay {
		go func() {
			for {
				value := <-reader
				fmt.Println(value)
				if !stay {
					break
				}
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

	}

	return nil
}
