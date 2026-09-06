// Command aispace is the CLI for aispace.sh, a bot-friendly file drop.
package main

import (
	"os"

	"github.com/aispace-sh/aispace-client/cmd"
)

// version is injected at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(cmd.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, version))
}
