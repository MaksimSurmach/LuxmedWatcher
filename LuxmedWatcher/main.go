package main

import (
	"fmt"
	"os"
	"LuxmedWatcher/internal/cli"
)

func main() {
	fmt.Println("Luxmed Watcher CLI")

	if err := run(); err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cli.RunCLI()
	return nil
}
