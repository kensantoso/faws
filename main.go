package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/kensantoso/faws/cmd"
)

func main() {
	if err := cmd.NewRootCmd().Execute(); err != nil {
		// An exec child's own exit status carries no message of faws's own.
		if msg := err.Error(); msg != "" {
			fmt.Fprintln(os.Stderr, "faws:", msg)
		}
		var ec cmd.ExitCoder
		if errors.As(err, &ec) {
			os.Exit(ec.ExitCode())
		}
		os.Exit(1)
	}
}
