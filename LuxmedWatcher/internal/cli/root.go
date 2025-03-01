package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	log "github.com/sirupsen/logrus"
)

var rootCmd = &cobra.Command{
	Use:   "luxmed-watcher",
	Short: "Luxmed Watcher monitors appointments on Luxmed and sends notifications",
	Long:  `Luxmed Watcher automatically checks for available appointments on Luxmed and sends notifications via various channels.`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("Please provide a subcommand. Use --help to see available commands.")
	},
}

func Execute() {
	log.SetLevel(log.InfoLevel)
	log.SetOutput(os.Stdout)
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
