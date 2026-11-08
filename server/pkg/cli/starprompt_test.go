package cli

import (
	"context"
	"io"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"
)

// captureLogin runs `auth login` against a fake server and returns stdout.
func captureLogin(t *testing.T, format string) string {
	t.Helper()

	srv := httptest.NewServer(fakeAuthServer(t, false))
	t.Cleanup(srv.Close)

	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())

	reader, writer, err := os.Pipe()
	require.NoError(t, err)

	origOut := os.Stdout
	os.Stdout = writer

	root := &cli.Command{
		Name: "sp", Flags: GetGlobalFlags(), Commands: GetCommands(),
		ExitErrHandler: func(context.Context, *cli.Command, error) {},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = root.Run(ctx, []string{
		"sp", "-o", format, "--url", srv.URL, "auth", "login", "--email", "alice@acme.com", "--password", "secret",
	})
	cancel()

	os.Stdout = origOut
	require.NoError(t, writer.Close())

	out, err := io.ReadAll(reader)
	require.NoError(t, err)

	return string(out)
}

//nolint:paralleltest // process-global stdout, HOME and env
func TestLoginStarPrompt(t *testing.T) {
	t.Setenv(StarPromptEnv, "")

	text := captureLogin(t, "text")
	require.Contains(t, text, "Login successful!")
	require.Contains(t, text, StarPromptLine)

	require.NotContains(t, captureLogin(t, "json"), "a star helps")

	t.Setenv(StarPromptEnv, "false")
	require.NotContains(t, captureLogin(t, "text"), "a star helps")
}

//nolint:paralleltest // process-global env
func TestStarPromptText(t *testing.T) {
	t.Setenv(StarPromptEnv, "")
	require.Equal(t, StarPromptLine, StarPromptText())

	for _, v := range []string{"false", "FALSE", "0"} {
		t.Setenv(StarPromptEnv, v)
		require.Empty(t, StarPromptText(), v)
	}

	t.Setenv(StarPromptEnv, "true")
	require.Equal(t, StarPromptLine, StarPromptText())
}
