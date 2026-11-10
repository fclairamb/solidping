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
	// flagServiceInstaller is the Windows account that installed the service,
	// which `service run` trusts like an administrator (see serviceInstall).
	flagServiceInstaller = "installer-sid"

	goosWindows = "windows"
	goosDarwin  = "darwin"
	goosLinux   = "linux"
)

var errServiceUnsupported = errors.New("services are only supported on Windows, macOS and Linux")

// serviceCommand registers SolidPing as a native Windows service (spec
// 2026-11-07-04, no wrapper such as NSSM), a macOS launchd job (spec
// 2026-11-08-06) or a systemd unit on Linux (spec 2026-11-09-01). It is hidden
// on other platforms, where every subcommand returns errServiceUnsupported.
func serviceCommand() *cli.Command {
	nameFlag := &cli.StringFlag{
		Name:  flagServiceName,
		Usage: "Service name (the launchd label on macOS, the systemd unit name on Linux)",
		Value: defaultServiceName(runtime.GOOS),
	}
	dirFlag := &cli.StringFlag{
		Name: flagServiceDir,
		Usage: "Data folder: config.yml, the SQLite database and the logs " +
			"(default: %ProgramData%\\SolidPing on Windows, Application Support/SolidPing on macOS, " +
			"/var/lib/solidping on Linux)",
	}

	return &cli.Command{
		Name:   "service",
		Usage:  "Run SolidPing as a Windows service, a macOS launchd job or a systemd unit",
		Hidden: !serviceSupported(runtime.GOOS),
		Commands: []*cli.Command{
			{
				Name: "install",
				Usage: "Register the service (start at boot, restart on failure); needs an administrator prompt " +
					"on Windows, sudo on macOS and Linux (without sudo: a launchd agent or a systemd user unit)",
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
				// What the Service Control Manager, launchd or systemd launches; install registers it.
				Name:   "run",
				Usage:  "Run as the service (started by the Service Control Manager, launchd or systemd, not by hand)",
				Hidden: true,
				Flags:  []cli.Flag{nameFlag, dirFlag, &cli.StringFlag{Name: flagServiceInstaller, Hidden: true}},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					return serviceRun(ctx, cmd.String(flagServiceName), serviceDir(cmd), cmd.String(flagServiceInstaller))
				},
			},
		},
	}
}

// serviceSupported reports whether `service` works on goos.
func serviceSupported(goos string) bool {
	return goos == goosWindows || goos == goosDarwin || goos == goosLinux
}

// defaultServiceName is the Windows service name, the launchd label on macOS,
// where labels are reverse-DNS, or the systemd unit name on Linux.
func defaultServiceName(goos string) string {
	switch goos {
	case goosDarwin:
		return "io.solidping"
	case goosLinux:
		return "solidping"
	default:
		return "SolidPing"
	}
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
//   - Linux as root (a system unit): /var/lib/solidping.
//   - Linux as a user (a user unit): $XDG_DATA_HOME/solidping, ~/.local/share/solidping by default.
func defaultServiceDir(goos string, root bool, getenv func(string) string) string {
	switch goos {
	case goosDarwin:
		if root {
			return "/Library/Application Support/SolidPing"
		}

		return getenv("HOME") + "/Library/Application Support/SolidPing"
	case goosLinux:
		if root {
			return "/var/lib/solidping"
		}

		if dataHome := getenv("XDG_DATA_HOME"); dataHome != "" {
			return dataHome + "/solidping"
		}

		return getenv("HOME") + "/.local/share/solidping"
	}

	programData := getenv("ProgramData")
	if programData == "" {
		programData = `C:\ProgramData`
	}

	return programData + `\SolidPing`
}

// serviceRunArgs is the argument list the Service Control Manager, launchd or
// systemd launches solidping with. absDir must be absolute, so the service never
// depends on the directory install ran from.
func serviceRunArgs(name, absDir string) []string {
	return []string{"service", "run", "--" + flagServiceName, name, "--" + flagServiceDir, absDir}
}
