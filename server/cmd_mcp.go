package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/internal/app"
	"github.com/fclairamb/solidping/server/internal/mcp"
)

// `solidping mcp --stdio` (spec 2026-09-26-05) is `serve` minus the HTTP
// listener, with the MCP dispatcher on stdin/stdout: database, migrations,
// jobs and check workers run in-process, so a check created over MCP produces
// results. It needs no token and no running server, which is what MCP
// directories that run `mcp-proxy -- <command>` in a container need.
//
// Stdout carries JSON-RPC and nothing else. slog is pointed at stderr by the
// command's Before hook and again once the config is loaded, and os.Stdout is
// swapped for os.Stderr for the whole run, so anything that prints to "stdout"
// on its own (embedded Postgres, a stray fmt.Print) lands on stderr.
//
// Unlike `solidping client mcp` / `sp mcp`, which bridge stdio to a remote
// server's HTTP endpoint with the caller's credential, this one IS the server.

const (
	flagMCPStdio = "stdio"
	flagMCPUser  = "user"
	flagOrg      = "org"
)

var (
	// errMCPStdioAgentMode refuses the one node role that has no database.
	errMCPStdioAgentMode = errors.New(
		"solidping mcp --stdio serves the local database, and SP_NODE_ROLE=agent has none")
	// errMCPHandlerMissing means SetupRoutes did not build the MCP handler.
	errMCPHandlerMissing = errors.New("MCP handler not initialized")
)

func mcpCommand() *cli.Command {
	return &cli.Command{
		Name:  "mcp",
		Usage: "Run the server without HTTP and serve MCP over stdin/stdout (for MCP directories and local clients)",
		Description: "Boots everything `serve` does except the HTTP listener (database, migrations, jobs, " +
			"check workers) and serves the MCP tools on stdin/stdout, as newline-delimited JSON-RPC. " +
			"Logs go to stderr.\n\n" +
			"There is no token: the session acts as the owner of the organization named by --org " +
			"(the default org unless set), or as the member named by --user. The process holds the " +
			"database credentials, so this grants nothing direct SQL access would not.",
		Before: logToStderr,
		// A bad flag fails before Before runs, and urfave would print the help
		// on stdout and hand the error to main's stdout logger. An MCP client
		// reads that stream as protocol, so say it on stderr and exit.
		OnUsageError: mcpUsageError,
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  flagMCPStdio,
				Usage: "Serve MCP on stdin/stdout (the only transport; required)",
			},
			&cli.StringFlag{
				Name: flagMCPUser,
				Usage: "Email or uid of the member of --org the session acts as " +
					"(default: the org's owner)",
			},
		},
		Action: mcpAction,
	}
}

func mcpUsageError(_ context.Context, _ *cli.Command, err error, _ bool) error {
	_, _ = fmt.Fprintf(os.Stderr, "solidping mcp: %v (see `solidping mcp --help`)\n", err)

	return cli.Exit("", 1)
}

func mcpAction(ctx context.Context, cmd *cli.Command) error {
	if !cmd.Bool(flagMCPStdio) {
		return cli.Exit("solidping mcp: stdio is the only transport, run `solidping mcp --stdio`", 1)
	}

	// Keep the real stdout for the protocol and point every other writer at
	// stderr. Restored on return, for tests that call the action in-process.
	protocolOut := os.Stdout
	os.Stdout = os.Stderr

	defer func() { os.Stdout = protocolOut }()

	if err := serveMCPStdio(ctx, cmd.String(flagOrg), cmd.String(flagMCPUser), os.Stdin, protocolOut); err != nil {
		// Logged here, on stderr, and turned into an exit code with no
		// message: the error then never reaches main's logger.
		slog.ErrorContext(ctx, "solidping mcp stopped", "error", err)

		return cli.Exit("", 1)
	}

	return nil
}

// serveMCPStdio boots the headless server, resolves the principal once the
// startup job has run, then serves MCP on input/out until input reaches EOF, a
// signal arrives or the server stops on its own (a database fault).
func serveMCPStdio(ctx context.Context, orgSlug, userRef string, input io.Reader, out io.Writer) error {
	cfg, shutdownOTel, err := loadServeConfig(ctx, os.Stderr)
	if err != nil {
		return err
	}

	defer shutdownOTel()

	if cfg.IsAgentMode() {
		return errMCPStdioAgentMode
	}

	server, err := prepareServer(ctx, cfg, true)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	ctx, stopParentWatch := watchParent(ctx, cfg)
	defer stopParentWatch()

	sessionErr, startErr := runHeadless(ctx, server, func(sessionCtx context.Context) error {
		return runMCPStdioSession(sessionCtx, server, orgSlug, userRef, input, out)
	})

	if closeErr := server.Close(ctx); closeErr != nil {
		slog.ErrorContext(ctx, "Error closing server", "error", closeErr)
	}

	if sessionErr != nil {
		return sessionErr
	}

	if startErr != nil && !errors.Is(startErr, context.Canceled) {
		return startErr
	}

	return nil
}

// runHeadless runs server.Start and, once the server is ready, session. The
// first of the two to finish stops the other: the session ending (stdin EOF,
// a principal error) shuts the server down, and the server stopping (signal,
// database fault) ends the session.
//
// It returns the session's error, then the server's.
func runHeadless(ctx context.Context, server *app.Server, session func(context.Context) error) (error, error) {
	runCtx, stopServer := context.WithCancel(ctx)
	defer stopServer()

	startDone := make(chan error, 1)

	go func() { startDone <- server.Start(runCtx) }()

	select {
	case <-server.Ready():
	case startErr := <-startDone:
		// Stopped before it was ready: a signal, or a failed boot step.
		return nil, startErr
	}

	sessionCtx, stopSession := context.WithCancel(ctx)
	defer stopSession()

	sessionDone := make(chan error, 1)

	go func() { sessionDone <- session(sessionCtx) }()

	select {
	case sessionErr := <-sessionDone:
		stopServer()

		return sessionErr, <-startDone
	case startErr := <-startDone:
		stopSession()

		return <-sessionDone, startErr
	}
}

// runMCPStdioSession resolves who the session acts as and serves it.
func runMCPStdioSession(
	ctx context.Context, server *app.Server, orgSlug, userRef string, input io.Reader, out io.Writer,
) error {
	handler := server.MCPHandler()
	if handler == nil {
		return errMCPHandlerMissing
	}

	principal, err := mcp.ResolveStdioPrincipal(ctx, server.DBService(), orgSlug, userRef)
	if err != nil {
		return fmt.Errorf("resolving the MCP principal: %w", err)
	}

	slog.InfoContext(ctx, "MCP stdio session ready",
		"org", principal.Org.Slug, "user", principal.User.Email, "role", principal.Role)

	return handler.ServeStdio(ctx, input, out, principal)
}
