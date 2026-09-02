package main

import (
	"os"

	"github.com/Ameb8/agent-run/internal/cli"
)

func main() {
	app := cli.New(os.Stderr)
	os.Exit(app.Run(os.Args[1:]))
}
