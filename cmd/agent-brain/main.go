package main

import (
	"os"

	"github.com/ebrahim5801/agent-brain-cli/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
