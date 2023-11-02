package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "reg",
	Short: "reg is a command line tool for working with registers over MQTT.",
	Long: `The reg command is a command line tool for working with registers over MQTT.
It allows you to read, write and list registers.
Furthermore it can also provide a 'virtual' register which is convenient for debugging of consumers of the register.

MQTT broker is specified using environment variable MQTT as hostname or hostname:port.

For more information on registers over MQTT, see: https://github.com/burgrp/go-reg .`,
	SilenceUsage: true,
}

func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
}
