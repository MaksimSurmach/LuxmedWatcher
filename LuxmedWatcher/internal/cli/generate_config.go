package cli

import (
	"fmt"
	"os"

	"LuxmedWatcher/internal/config"
	"github.com/spf13/cobra"
)

var generateConfigCmd = &cobra.Command{
	Use:   "generate-config",
	Short: "Generate a sample configuration file",
	Run: func(cmd *cobra.Command, args []string) {
		err := config.GenerateSampleConfigFile("config.yaml")
		if err != nil {
			fmt.Printf("Error generating config: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Sample config.yaml generated successfully.")
	},
}

func init() {
	rootCmd.AddCommand(generateConfigCmd)
}
