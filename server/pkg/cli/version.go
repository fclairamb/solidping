package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/internal/version"
	"github.com/fclairamb/solidping/server/pkg/cli/output"
	"github.com/fclairamb/solidping/server/pkg/client"
)

const (
	flagClient = "client"
	// unknownValue stands in for a field a server leaves out of its version
	// response, the same fallback the server uses for a missing ldflag.
	unknownValue = "unknown"
)

// buildInfo is the build stamp of one binary. It leaves out the uptime
// /api/mgmt/version reports, which means nothing for a process that exits
// right away.
type buildInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	GitTime string `json:"gitTime"`
}

// serverVersion is the server half of `sp version`: either the build stamp of
// the server at URL, or why it could not be read.
type serverVersion struct {
	URL     string `json:"url"`
	Version string `json:"version,omitempty"`
	Commit  string `json:"commit,omitempty"`
	GitTime string `json:"gitTime,omitempty"`
	Error   string `json:"error,omitempty"`
}

// versionReport is what `sp version` prints.
type versionReport struct {
	Client buildInfo      `json:"client"`
	Server *serverVersion `json:"server,omitempty"`
}

// localBuildInfo returns the build stamp of the running binary, with the
// leading "v" stripped like the server does, so a client and a server built
// from the same tag print the same version.
func localBuildInfo() buildInfo {
	info := version.Get()

	return buildInfo{Version: info.Version, Commit: info.Commit, GitTime: info.GitTime}
}

// VersionCommand returns `sp version` (also `solidping client version`): the
// version of this client and of the server it talks to. --client skips the
// server, so it answers right after an install, before any server is set up.
func VersionCommand() *cli.Command {
	return &cli.Command{
		Name:  cmdVersion,
		Usage: "Print the version of this client and of the server it talks to",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  flagClient,
				Usage: "Print only the client version, without contacting the server",
			},
		},
		Action: versionAction,
	}
}

// BinaryVersionCommand returns the `version` command of the solidping binary:
// its own build stamp, read with no config file, no token and no network call.
func BinaryVersionCommand() *cli.Command {
	return &cli.Command{
		Name:   cmdVersion,
		Usage:  "Print the version, commit and build time of this binary",
		Action: binaryVersionAction,
	}
}

func binaryVersionAction(_ context.Context, cmd *cli.Command) error {
	writer := cmd.Root().Writer

	if format := outputFormat(cmd); format != output.FormatText {
		return output.NewOutputter(format, writer).Print(localBuildInfo())
	}

	_, err := fmt.Fprint(writer, version.String())

	return err
}

func versionAction(ctx context.Context, cmd *cli.Command) error {
	report := versionReport{Client: localBuildInfo()}

	if !cmd.Bool(flagClient) {
		cliCtx, err := NewCLIContext(cmd)
		if err != nil {
			return err
		}

		report.Server = &serverVersion{URL: cliCtx.Config.URL}

		resp, err := fetchServerVersion(ctx, cliCtx)
		if err != nil {
			report.Server.Error = err.Error()
		} else {
			report.Server.Version = derefMessage(resp.Version, unknownValue)
			report.Server.Commit = derefMessage(resp.Commit, unknownValue)
			report.Server.GitTime = derefMessage(resp.GitTime, unknownValue)
		}
	}

	writer := cmd.Root().Writer

	if format := outputFormat(cmd); format != output.FormatText {
		if err := output.NewOutputter(format, writer).Print(report); err != nil {
			return err
		}
	} else {
		writeVersionReport(writer, report)
	}

	// The client half is printed either way; an unreachable server still
	// fails the command, so a script can tell.
	if report.Server != nil && report.Server.Error != "" {
		return cli.Exit("", 1)
	}

	return nil
}

func writeVersionReport(writer io.Writer, report versionReport) {
	_, _ = fmt.Fprintf(writer, "Client:\n  Version:   %s\n  Commit:    %s\n  Git Time:  %s\n",
		report.Client.Version, report.Client.Commit, report.Client.GitTime)

	if report.Server == nil {
		return
	}

	_, _ = fmt.Fprintf(writer, "Server (%s):\n", report.Server.URL)

	if report.Server.Error != "" {
		output.PrintError(writer, "Failed to get server version: "+report.Server.Error)

		return
	}

	_, _ = fmt.Fprintf(writer, "  Version:   %s\n  Commit:    %s\n  Git Time:  %s\n",
		report.Server.Version, report.Server.Commit, report.Server.GitTime)
}

// fetchServerVersion reads GET /api/mgmt/version, which needs no auth.
func fetchServerVersion(ctx context.Context, cliCtx *Context) (*client.VersionResponse, error) {
	apiClient, err := client.New(client.Config{BaseURL: cliCtx.Config.URL, Verbose: cliCtx.Verbose})
	if err != nil {
		return nil, err
	}

	resp, err := apiClient.GetVersionWithResponse(ctx)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
		return nil, fmt.Errorf("%w (status: %d)", errStatusError, resp.StatusCode())
	}

	return resp.JSON200, nil
}
