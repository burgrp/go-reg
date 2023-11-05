package cmd

import (
	"fmt"
	"goreg/pkg/goreg"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:   "list [<reg1> <reg2> ...]",
	Short: "List all known registers",
	Long: `Lists all known registers by sending advertise challenge to all devices.
With --stay flag, the command will remain connected and write any changes to stdout.
If registers are specified, only those will be listed.`,
	RunE: runList,
}

func init() {
	RootCmd.AddCommand(listCmd)
	listCmd.Flags().BoolP("stay", "s", false, "Stay connected, write changes to stdout")
	listCmd.Flags().DurationP("timeout", "t", 5*time.Second, "Timeout for waiting for advertise challenge to be answered")
}

func printMetadata(metadata goreg.NameAndMetadata) {
	m := ""
	for k, v := range metadata.Metadata {
		m += fmt.Sprintf(" %s:%s", k, v)
	}
	fmt.Println(metadata.Name + " -" + m)
}

func printValue(value goreg.NameAndValue) {
	fmt.Println(value.Name + " = " + string(value.Value))
}

func passNameFilter(name string, args []string) bool {
	if len(args) == 0 {
		return true
	}
	for _, arg := range args {
		if strings.Contains(name, arg) {
			return true
		}
	}
	return false
}

func runList(cmd *cobra.Command, args []string) error {

	stay, err := cmd.Flags().GetBool("stay")
	if err != nil {
		return err
	}

	timeout, error := cmd.Flags().GetDuration("timeout")
	if error != nil {
		return error
	}

	registers, err := goreg.NewRegisters()
	if err != nil {
		return err
	}
	metadata, values := goreg.Watch(registers)

	if stay {
		for {
			select {
			case metadata := <-metadata:
				if passNameFilter(metadata.Name, args) {
					printMetadata(metadata)
				}
			case value := <-values:
				if passNameFilter(value.Name, args) {
					printValue(value)
				}
			}
		}
	} else {
		timeout_timer := time.NewTimer(timeout)
		printed := make(map[string]bool)
		for {
			select {
			case metadata := <-metadata:
				if passNameFilter(metadata.Name, args) {
					if !printed[metadata.Name] {
						if timeout_timer != nil {
							timeout_timer.Stop()
						}
						timeout_timer = time.NewTimer(timeout)
						printed[metadata.Name] = true
						printMetadata(metadata)
					}
				}
			case <-timeout_timer.C:
				return nil
			}
		}
	}

}
