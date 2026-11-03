package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/xiaofantu/vantu/internal/cli"
)

func main() {
	if err := cli.NewRootCmd().Execute(); err != nil {
		var exitErr *cli.CLIExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode)
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
