package main

import (
	"context"
	"errors"
	"os"
	"runtime"

	"github.com/urfave/cli/v3"
)

const (
	flagServiceName = "name"
	flagServiceDir  = "dir"

	defaultServiceName = "SolidPing"

	goosWindows = "windows"
)

var errServiceUnsupported = errors.New("windows services are only supported on Windows")

// serviceCommand registers SolidPing as a native Windows service (spec
// 2026-11-07-04), so no wrapper such as NSSM is needed. It is hidden on other
// platforms, where every subcommand returns errServiceUnsupported.
func serviceCommand() *cli.Command {
	nameFlag := &cli.StringFlag{
		Name:  flagServiceName,
		Usage: "Service name",
		Value: defaultServiceName,
	}
	dirFlag := &cli.StringFlag{
		Name:  flagServiceDir,
		Usage: "Data folder: config.yml, the SQLite database and the logs (default: %ProgramData%\\SolidPing)",
	}

	return &cli.Command{
		Name:   "service",
		Usage:  "Run SolidPing as a Windows service",
		Hidden: runtime.GOOS != goosWindows,
		Commands: []*cli.Command{
			{
				Name:  "install",
				Usage: "Register the service (automatic start, restart on failure); needs an administrator prompt",
				Flags: []cli.Flag{nameFlag, dirFlag},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					return serviceInstall(ctx, cmd.String(flagServiceName), serviceDir(cmd))
				},
			},
			{
				Name:  "uninstall",
				Usage: "Stop and remove the service (the data folder is kept)",
				Flags: []cli.Flag{nameFlag},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					return serviceUninstall(ctx, cmd.String(flagServiceName))
				},
			},
			{
				Name:  "start",
				Usage: "Start the service",
				Flags: []cli.Flag{nameFlag},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					return serviceStart(ctx, cmd.String(flagServiceName))
				},
			},
			{
				Name:  "stop",
				Usage: "Stop the service and wait until it has stopped",
				Flags: []cli.Flag{nameFlag},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					return serviceStop(ctx, cmd.String(flagServiceName))
				},
			},
			{
				// What the Service Control Manager launches; install registers it.
				Name:   "run",
				Usage:  "Run as the service (started by the Service Control Manager, not by hand)",
				Hidden: true,
				Flags:  []cli.Flag{nameFlag, dirFlag},
				Action: func(_ context.Context, cmd *cli.Command) error {
					return serviceRun(cmd.String(flagServiceName), serviceDir(cmd))
				},
			},
		},
	}
}

// serviceDir is the --dir flag, or the default data folder when unset.
func serviceDir(cmd *cli.Command) string {
	if dir := cmd.String(flagServiceDir); dir != "" {
		return dir
	}

	return defaultServiceDir(os.Getenv)
}

// defaultServiceDir is %ProgramData%\SolidPing. ProgramData is always set on
// Windows; the fallback only covers a stripped environment.
func defaultServiceDir(getenv func(string) string) string {
	programData := getenv("ProgramData")
	if programData == "" {
		programData = `C:\ProgramData`
	}

	return programData + `\SolidPing`
}

// serviceRunArgs is the argument list the Service Control Manager launches
// solidping.exe with. absDir must be absolute, so the service never depends on
// the directory install ran from.
func serviceRunArgs(name, absDir string) []string {
	return []string{"service", "run", "--" + flagServiceName, name, "--" + flagServiceDir, absDir}
}
