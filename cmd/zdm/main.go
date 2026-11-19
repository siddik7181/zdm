// Command zdm plans, guards and orchestrates zero-downtime MySQL schema changes.
package main

import (
	"fmt"
	"os"

	"github.com/siddik7181/zdm/internal/version"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(version.String())
		return
	}
	fmt.Fprintln(os.Stderr, "zdm: no commands implemented yet (see docs/04-roadmap.md)")
	os.Exit(2)
}
