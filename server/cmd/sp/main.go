// Package main provides the command-line interface for SolidPing.
package main

import (
	"context"
	"log"
	"os"

	clilib "github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/internal/version"
	"github.com/fclairamb/solidping/server/pkg/cli"
)

func main() {
	app := &clilib.Command{
		Name:  "sp",
		Usage: "solidping CLI - manage and monitor solidping instances",
		// Not a literal: the release workflow stamps the real version in with
		// -X internal/version.Version, and a hardcoded string here made every
		// one of those ldflags inert — v0.28.0's published binaries and image
		// all reported "1.0.0".
		Version:  version.Version,
		Flags:    cli.GetGlobalFlags(),
		Commands: cli.GetCommands(),
	}

	if err := app.Run(context.Background(), os.Args); err != nil {
		log.Fatal(err)
	}
}
