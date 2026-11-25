// Command gonestly creates, generates, runs and inspects Gonestly applications.
package main

import (
	"os"

	"github.com/yaboylino/gonestly/cmd/gonestly/internal/cli"
)

func main() {
	os.Exit(cli.Execute(os.Args[1:], os.Stdout, os.Stderr))
}
