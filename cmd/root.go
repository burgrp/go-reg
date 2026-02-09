package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

var RootCmd = &cobra.Command{
	Use:   "mreg",
	Short: "mreg is a command line tool for working with registers over MQTT.",
	Long: `The mreg command is a command line tool for working with registers over MQTT.
It allows you to read, write and list registers.
Furthermore it can provide a 'virtual' register which is convenient for debugging of consumers of the register.

MQTT broker is specified using environment variable MQTT as hostname or hostname:port.

For more information on registers over MQTT, see: https://github.com/burgrp/go-reg .`,
	SilenceUsage: true,
}

func Execute() {
	err := RootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
}
