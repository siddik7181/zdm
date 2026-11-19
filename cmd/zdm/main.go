// Command zdm plans, guards and orchestrates zero-downtime MySQL schema changes.
package main

import (
	"os"

	"github.com/siddik7181/zdm/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], cli.Streams{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}))
}
