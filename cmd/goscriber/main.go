// Command goscriber is the Goscriber desktop app and its command-line tools.
package main

import (
	"os"

	"github.com/opragmatico/goscriber/internal/cli"
)

// version is stamped at link time: go build -ldflags "-X main.version=v1.2.3".
var version string

func main() {
	os.Exit(cli.Run(os.Args[1:], cli.Env{
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Version: version,
	}))
}
