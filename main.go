// Command callboard is a message hub for coding-agent sessions. One binary
// serves as the hub and as the client every session uses.
package main

import (
	"os"

	"github.com/philband/callboard/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
