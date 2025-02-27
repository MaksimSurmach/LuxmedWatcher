package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var testCmd = &cobra.Command{
	Use:   "test",
	Short: "Run a test command",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("Test command executed")
	},
}

func init() {
	rootCmd.AddCommand(testCmd)
}
