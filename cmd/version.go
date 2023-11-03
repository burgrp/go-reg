package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var Version = "local-build"

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show version",
	Long:  `Shows version of reg command line tool.`,
	Run:   runVersion,
}

func init() {
	RootCmd.AddCommand(versionCmd)
}

func runVersion(cmd *cobra.Command, args []string) {
	fmt.Println(Version)
}
