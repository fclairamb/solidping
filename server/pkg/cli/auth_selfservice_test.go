package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"
)

// selfServiceServer is a fake server for the sign-up and password-reset
// forms: it hands out a form ticket and records what the submissions carried.
type selfServiceServer struct {
	mu         sync.Mutex
	minAgeMs   int
	submitted  map[string]map[string]any
	ticketCall int
}

func (s *selfServiceServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")

	switch r.URL.Path {
	case "/api/v1/auth/form-ticket":
		s.ticketCall++
		_ = json.NewEncoder(w).Encode(map[string]any{ //nolint:errchkjson // test server, nothing to do on failure
			"ticket": "ticket-" + r.URL.Query().Get("form"), "minAgeMs": s.minAgeMs,
		})
	case "/api/v1/auth/register", "/api/v1/auth/request-password-reset":
		var body map[string]any

		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)

		if s.submitted == nil {
			s.submitted = map[string]map[string]any{}
		}

		s.submitted[r.URL.Path] = body
		_, _ = io.WriteString(w, `{"message":"ok"}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// runSelfService runs one CLI invocation against handler with stdin at EOF
// and returns its error.
func runSelfService(t *testing.T, handler http.Handler, timeout time.Duration, args ...string) error {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	home := t.TempDir()
	t.Chdir(t.TempDir())
	t.Setenv("HOME", home)

	cfgDir := filepath.Join(home, ".config", "solidping")
	require.NoError(t, os.MkdirAll(cfgDir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "settings.json"),
		[]byte(`{"url":"`+srv.URL+`","org":"acme"}`), 0o600))

	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	require.NoError(t, err)

	defer func() { _ = devNull.Close() }()

	origOut, origErr, origIn := os.Stdout, os.Stderr, os.Stdin
	os.Stdout, os.Stderr, os.Stdin = devNull, devNull, devNull

	defer func() { os.Stdout, os.Stderr, os.Stdin = origOut, origErr, origIn }()

	root := &cli.Command{
		Name: "sp", Flags: GetGlobalFlags(), Commands: GetCommands(),
		ExitErrHandler: func(context.Context, *cli.Command, error) {},
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	return root.Run(ctx, append([]string{"sp", "-o", "json"}, args...))
}

//nolint:paralleltest // process-global stdio and HOME
func TestAuthSelfServiceSendsTheFormTicket(t *testing.T) {
	tests := []struct {
		name string
		path string
		args []string
	}{
		{
			name: "register", path: "/api/v1/auth/register",
			args: []string{"auth", "register", "--email", "jane@acme.com", "--password", "supersecret123"},
		},
		{
			name: "request password reset", path: "/api/v1/auth/request-password-reset",
			args: []string{"auth", "request-password-reset", "--email", "jane@acme.com"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := &selfServiceServer{}

			require.NoError(t, runSelfService(t, srv, 5*time.Second, tc.args...))

			srv.mu.Lock()
			defer srv.mu.Unlock()

			require.Equal(t, 1, srv.ticketCall)
			require.Equal(t, "jane@acme.com", srv.submitted[tc.path]["email"])
			require.NotEmpty(t, srv.submitted[tc.path]["formTicket"])
		})
	}
}

// With no --email and nothing on stdin, the prompt fails and nothing is sent.
//
//nolint:paralleltest // process-global stdio and HOME
func TestAuthSelfServiceFailsWithoutEmailInput(t *testing.T) {
	for _, args := range [][]string{
		{"auth", "register"},
		{"auth", "request-password-reset"},
	} {
		t.Run(args[1], func(t *testing.T) {
			srv := &selfServiceServer{}

			require.ErrorIs(t, runSelfService(t, srv, 5*time.Second, args...), io.EOF)

			srv.mu.Lock()
			defer srv.mu.Unlock()

			require.Empty(t, srv.submitted)
		})
	}
}

// A ticket that is not old enough yet makes the CLI wait; an interrupted wait
// aborts before anything is submitted.
//
//nolint:paralleltest // process-global stdio and HOME
func TestAuthSelfServiceInterruptedTicketWait(t *testing.T) {
	for _, args := range [][]string{
		{"auth", "register", "--email", "jane@acme.com", "--password", "supersecret123"},
		{"auth", "request-password-reset", "--email", "jane@acme.com"},
	} {
		t.Run(args[1], func(t *testing.T) {
			srv := &selfServiceServer{minAgeMs: 60000}

			require.ErrorIs(t, runSelfService(t, srv, 300*time.Millisecond, args...), context.DeadlineExceeded)

			srv.mu.Lock()
			defer srv.mu.Unlock()

			require.Empty(t, srv.submitted)
		})
	}
}
