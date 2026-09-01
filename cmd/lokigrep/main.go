package main

import (
	"fmt"
	"os"

	"github.com/ochaton/lokigrep/internal/cli"
)

func main() {
	code, err := cli.Run(os.Args[1:])
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "lokigrep: %v\n", err)
	}
	os.Exit(code)
}
