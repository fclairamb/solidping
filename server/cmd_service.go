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

	goosWindows = "windows"
	goosDarwin  = "darwin"
)

var errServiceUnsupported = errors.New("services are only supported on Windows and macOS")

// serviceCommand registers SolidPing as a native Windows service (spec
// 2026-11-07-04, no wrapper such as NSSM) or a macOS launchd job (spec
// 2026-11-08-06). It is hidden on other platforms, where every subcommand
// returns errServiceUnsupported.
func serviceCommand() *cli.Command {
	nameFlag := &cli.StringFlag{
		Name:  flagServiceName,
		Usage: "Service name (the launchd label on macOS)",
		Value: defaultServiceName(runtime.GOOS),
	}
	dirFlag := &cli.StringFlag{
		Name: flagServiceDir,
		Usage: "Data folder: config.yml, the SQLite database and the logs " +
			"(default: %ProgramData%\\SolidPing on Windows, Application Support/SolidPing on macOS)",
	}

	return &cli.Command{
		Name:   "service",
		Usage:  "Run SolidPing as a Windows service or a macOS launchd job",
		Hidden: !serviceSupported(runtime.GOOS),
		Commands: []*cli.Command{
			{
				Name: "install",
				Usage: "Register the service (start at boot, restart on failure); needs an administrator prompt " +
					"on Windows, sudo on macOS (without sudo: a launchd agent of the current user)",
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
				// What the Service Control Manager or launchd launches; install registers it.
				Name:   "run",
				Usage:  "Run as the service (started by the Service Control Manager or launchd, not by hand)",
				Hidden: true,
				Flags:  []cli.Flag{nameFlag, dirFlag},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					return serviceRun(ctx, cmd.String(flagServiceName), serviceDir(cmd))
				},
			},
		},
	}
}

// serviceSupported reports whether `service` works on goos.
func serviceSupported(goos string) bool {
	return goos == goosWindows || goos == goosDarwin
}

// defaultServiceName is the Windows service name, or the launchd label on
// macOS, where labels are reverse-DNS.
func defaultServiceName(goos string) string {
	if goos == goosDarwin {
		return "io.solidping"
	}

	return "SolidPing"
}

// serviceDir is the --dir flag, or the default data folder when unset.
func serviceDir(cmd *cli.Command) string {
	if dir := cmd.String(flagServiceDir); dir != "" {
		return dir
	}

	return defaultServiceDir(runtime.GOOS, os.Geteuid() == 0, os.Getenv)
}

// defaultServiceDir is the data folder used when --dir is unset:
//   - Windows: %ProgramData%\SolidPing. ProgramData is always set on Windows;
//     the fallback only covers a stripped environment.
//   - macOS as root (a launchd daemon): /Library/Application Support/SolidPing.
//   - macOS as a user (a launchd agent): ~/Library/Application Support/SolidPing.
func defaultServiceDir(goos string, root bool, getenv func(string) string) string {
	if goos == goosDarwin {
		if root {
			return "/Library/Application Support/SolidPing"
		}

		return getenv("HOME") + "/Library/Application Support/SolidPing"
	}

	programData := getenv("ProgramData")
	if programData == "" {
		programData = `C:\ProgramData`
	}

	return programData + `\SolidPing`
}

// serviceRunArgs is the argument list the Service Control Manager or launchd
// launches solidping with. absDir must be absolute, so the service never
// depends on the directory install ran from.
func serviceRunArgs(name, absDir string) []string {
	return []string{"service", "run", "--" + flagServiceName, name, "--" + flagServiceDir, absDir}
}
