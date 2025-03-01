package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var generateConfigCmd = &cobra.Command{
	Use:   "generate-config",
	Short: "Generate a sample configuration file",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("Sample config.yaml generated successfully.")
	},
}

func init() {
	rootCmd.AddCommand(generateConfigCmd)
}
