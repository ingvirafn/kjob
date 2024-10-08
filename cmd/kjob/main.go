package main

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var VERSION = "0.9.0"

var rootCmd = &cobra.Command{
	Use:     "kjob",
	Short:   "Kubernetes job runner",
	Version: VERSION,
}

func main() {
	log.SetFlags(0)

	rootCmd.SetArgs(os.Args[1:])
	if err := rootCmd.Execute(); err != nil {
		e := err.Error()
		fmt.Println(strings.ToUpper(e[:1]) + e[1:])
		os.Exit(1)
	}
}
