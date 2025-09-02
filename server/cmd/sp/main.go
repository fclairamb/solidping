// Package main provides the command-line interface for SolidPing.
package main

import (
	"context"
	"log"
	"os"

	clilib "github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/pkg/cli"
)

func main() {
	app := &clilib.Command{
		Name:     "sp",
		Usage:    "solidping CLI - manage and monitor solidping instances",
		Version:  "1.0.0",
		Flags:    cli.GetGlobalFlags(),
		Commands: cli.GetCommands(),
	}

	if err := app.Run(context.Background(), os.Args); err != nil {
		log.Fatal(err)
	}
}
