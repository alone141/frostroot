// Command frostroot freezes an Ubuntu root filesystem into a recipe, a
// lockfile and a golden image tarball.
package main

import (
	"os"

	"frostroot/internal/cli"
)

func main() { os.Exit(cli.New().Run(os.Args[1:])) }
