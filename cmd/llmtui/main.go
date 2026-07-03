// llmtui is a terminal UI for chatting with local LLM backends.
package main

import (
	"os"

	"github.com/patrikcze/llmtui/internal/cli"
)

// Set via -ldflags at release time.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if err := cli.NewRootCmd(version, commit, date).Execute(); err != nil {
		os.Exit(1)
	}
}
