package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"
)

var (
	errTestBoom         = errors.New("boom")
	errTestPrinted      = errors.New("printed")
	errTestNoCredential = errors.New("no credential")
	errTestKaput        = errors.New("kaput")
	errTestFetch        = errors.New("fetch failed")
)

const watchItem = `{"uid":"00000000-0000-0000-0000-000000000001","slug":"web","name":"Web","type":"http",` +
	`"state":"up","enabled":true,"period":"00:01:00","title":"Outage","startedAt":"2026-01-01T00:00:00Z"}`

// runWatchCLI runs `sp <args>` against a fake server (the live socket is a
// 404, so the watch polls) until timeout, and returns what went to stdout.
func runWatchCLI(t *testing.T, restStatus int, restBody string, args ...string) (string, error) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/events/ws") {
			w.WriteHeader(http.StatusNotFound)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(restStatus)
		_, _ = w.Write([]byte(restBody))
	}))
	t.Cleanup(srv.Close)

	home := t.TempDir()
	t.Setenv("HOME", home)

	cfgDir := filepath.Join(home, ".config", "solidping")
	require.NoError(t, os.MkdirAll(cfgDir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "settings.json"),
		[]byte(`{"url":"`+srv.URL+`","org":"acme","auth":{"pat":"sp_pat_test"}}`), 0o600))

	pr, pw, err := os.Pipe()
	require.NoError(t, err)

	origOut := os.Stdout
	os.Stdout = pw

	ctx, cancel := context.WithTimeout(t.Context(), 600*time.Millisecond)
	defer cancel()

	root := &cli.Command{
		Name:           "sp",
		Flags:          GetGlobalFlags(),
		Commands:       GetCommands(),
		ExitErrHandler: func(context.Context, *cli.Command, error) {},
	}
	runErr := root.Run(ctx, append([]string{"sp"}, args...))

	os.Stdout = origOut
	require.NoError(t, pw.Close())

	raw, err := io.ReadAll(pr)
	require.NoError(t, err)
	require.NoError(t, pr.Close())

	return string(raw), runErr
}

//nolint:paralleltest // see runWatchCLI
func TestWatchCommandTableChecks(t *testing.T) {
	out, err := runWatchCLI(t, http.StatusOK, `{"data":[`+watchItem+`]}`, "checks", "list", "--watch")
	require.NoError(t, err)
	require.Contains(t, out, clearScreen)
	require.Contains(t, out, "sp checks list --watch | org: acme")
	require.Contains(t, out, "polling")
	require.Contains(t, out, "web")
}

//nolint:paralleltest // see runWatchCLI
func TestWatchCommandTableIncidents(t *testing.T) {
	out, err := runWatchCLI(t, http.StatusOK, `{"data":[`+watchItem+`]}`, "incidents", "list", "--watch")
	require.NoError(t, err)
	require.Contains(t, out, "sp incidents list --watch")
	require.Contains(t, out, "Outage")
}

//nolint:paralleltest // see runWatchCLI
func TestWatchCommandTableShowsRefreshError(t *testing.T) {
	out, err := runWatchCLI(t, http.StatusInternalServerError, `{"title":"boom"}`,
		"-o", "text", "checks", "list", "--watch")
	require.NoError(t, err)
	require.Contains(t, out, "polling")
}

//nolint:paralleltest // see runWatchCLI
func TestWatchCommandJSONLChecksSnapshots(t *testing.T) {
	out, err := runWatchCLI(t, http.StatusOK, `{"data":[`+watchItem+`]}`, "-o", "jsonl", "checks", "list", "--watch")
	require.NoError(t, err)
	require.NotContains(t, out, "\x1b")

	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.NotEmpty(t, lines)

	var snap struct {
		Source string           `json:"source"`
		Data   []map[string]any `json:"data"`
	}

	require.NoError(t, json.Unmarshal([]byte(lines[0]), &snap))
	require.Equal(t, "polling", snap.Source)
	require.Len(t, snap.Data, 1)
	require.Equal(t, "web", snap.Data[0]["slug"])
}

//nolint:paralleltest // see runWatchCLI
func TestWatchCommandJSONLIncidentsEmptyData(t *testing.T) {
	out, err := runWatchCLI(t, http.StatusOK, `{"data":null}`, "-o", "jsonl", "incidents", "list", "--watch")
	require.NoError(t, err)
	require.Contains(t, out, `"data":[]`)
}

//nolint:paralleltest // see runWatchCLI
func TestWatchCommandJSONLFetchErrorWritesNoSnapshot(t *testing.T) {
	out, err := runWatchCLI(t, http.StatusInternalServerError, `{"title":"boom"}`,
		"-o", "jsonl", "checks", "list", "--watch")
	require.NoError(t, err)
	require.NotContains(t, out, `"refreshedAt"`)
}

func TestCaptureData(t *testing.T) {
	t.Parallel()

	newCtx := func() *Context { return &Context{Outputter: &captureOutputter{}} }

	t.Run("returns the data array and restores the outputter", func(t *testing.T) {
		t.Parallel()

		cliCtx := newCtx()
		orig := cliCtx.Outputter
		data, err := captureData(t.Context(), cliCtx, func(context.Context) error {
			return cliCtx.Outputter.Print(map[string]any{"data": []string{"a"}})
		})
		require.NoError(t, err)
		require.JSONEq(t, `["a"]`, string(data))
		require.Same(t, orig, cliCtx.Outputter)
	})

	t.Run("missing data is an empty array", func(t *testing.T) {
		t.Parallel()

		cliCtx := newCtx()
		data, err := captureData(t.Context(), cliCtx, func(context.Context) error {
			return cliCtx.Outputter.Print(map[string]any{"other": 1})
		})
		require.NoError(t, err)
		require.JSONEq(t, `[]`, string(data))
	})

	t.Run("run error is returned", func(t *testing.T) {
		t.Parallel()

		errBoom := errTestBoom
		_, err := captureData(t.Context(), newCtx(), func(context.Context) error { return errBoom })
		require.ErrorIs(t, err, errBoom)
	})

	t.Run("printed error is returned", func(t *testing.T) {
		t.Parallel()

		cliCtx := newCtx()
		errBoom := errTestPrinted
		_, err := captureData(t.Context(), cliCtx, func(context.Context) error {
			return cliCtx.Outputter.PrintError(errBoom)
		})
		require.Equal(t, errBoom, err)
	})

	t.Run("unmarshalable payload", func(t *testing.T) {
		t.Parallel()

		cliCtx := newCtx()
		_, err := captureData(t.Context(), cliCtx, func(context.Context) error {
			return cliCtx.Outputter.Print(make(chan int))
		})
		require.ErrorContains(t, err, "marshal")
	})

	t.Run("payload that is not an object", func(t *testing.T) {
		t.Parallel()

		cliCtx := newCtx()
		_, err := captureData(t.Context(), cliCtx, func(context.Context) error {
			return cliCtx.Outputter.Print([]int{1})
		})
		require.ErrorContains(t, err, "decode")
	})

	t.Run("canceled context", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err := captureData(ctx, newCtx(), func(context.Context) error { return nil })
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("success message is swallowed", func(t *testing.T) {
		t.Parallel()

		require.NoError(t, (&captureOutputter{}).Success("ok"))
	})
}

func TestApplyDefaultsKeepsExplicitValues(t *testing.T) {
	t.Parallel()

	empty := &watchConfig{}
	empty.applyDefaults()
	require.Equal(t, watchDebounce, empty.Debounce)
	require.Equal(t, watchPollInterval, empty.PollInterval)
	require.Equal(t, watchBackoffStart, empty.BackoffStart)
	require.Equal(t, watchBackoffMax, empty.BackoffMax)

	set := &watchConfig{
		Debounce: time.Second, PollInterval: 2 * time.Second, BackoffStart: 3 * time.Second, BackoffMax: 4 * time.Second,
	}
	set.applyDefaults()
	require.Equal(t, time.Second, set.Debounce)
	require.Equal(t, 2*time.Second, set.PollInterval)
	require.Equal(t, 3*time.Second, set.BackoffStart)
	require.Equal(t, 4*time.Second, set.BackoffMax)
}

func TestWatchFallsBackToPollingWithoutToken(t *testing.T) {
	t.Parallel()

	var refreshes atomic.Int32

	out := &lockedBuffer{}
	cfg := testWatchConfig("http://127.0.0.1:1", &refreshes, out)
	cfg.Token = func(context.Context) (string, error) { return "", errTestNoCredential }

	cancel, done := startWatch(t, cfg)

	waitFor(t, func() bool { return refreshes.Load() >= 2 })
	stopWatch(t, cancel, done)
	require.Contains(t, out.String(), "polling")
}

func TestWatchRetriesWhenServerUnreachable(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens any more: dial fails without an HTTP response

	var refreshes atomic.Int32

	out := &lockedBuffer{}
	errOut := &lockedBuffer{}
	cfg := testWatchConfig(url, &refreshes, out)
	cfg.JSONL = true
	cfg.Err = errOut
	cfg.Fetch = func(context.Context) (json.RawMessage, error) {
		refreshes.Add(1)

		return json.RawMessage(`[]`), nil
	}

	cancel, done := startWatch(t, cfg)

	waitFor(t, func() bool { return refreshes.Load() >= 3 })
	stopWatch(t, cancel, done)
	require.Contains(t, errOut.String(), "retrying")
}

func TestWatchHandshakeProtocolErrors(t *testing.T) {
	t.Parallel()

	cases := map[string]func(ctx context.Context, conn *websocket.Conn){
		"wrong hello": func(ctx context.Context, conn *websocket.Conn) {
			_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"nope"}`))
		},
		"closed before hello": func(_ context.Context, conn *websocket.Conn) {
			_ = conn.Close(websocket.StatusNormalClosure, "")
		},
		"wrong subscribed": func(ctx context.Context, conn *websocket.Conn) {
			_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"hello"}`))
			_, _, _ = conn.Read(ctx)
			_ = conn.Write(ctx, websocket.MessageText, []byte(`not json`))
		},
	}

	for name, serve := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var attempts atomic.Int32

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{liveSubprotocol}})
				if err != nil {
					return
				}

				attempts.Add(1)
				serve(r.Context(), conn)
				time.Sleep(50 * time.Millisecond)
				_ = conn.CloseNow()
			}))
			defer srv.Close()

			cfg := testWatchConfig(srv.URL, new(atomic.Int32), &lockedBuffer{})
			conn, err := cfg.dial(t.Context())
			require.Error(t, err)
			require.Nil(t, conn)
			require.EqualValues(t, 1, attempts.Load())
		})
	}
}

func TestWatchRedrawPrintsRefreshError(t *testing.T) {
	t.Parallel()

	out := &lockedBuffer{}
	cfg := &watchConfig{
		Title: "checks list", Org: "acme", Out: out,
		Refresh: func(context.Context) error { return errTestKaput },
	}
	cfg.redraw(t.Context(), modePolling)
	require.Contains(t, out.String(), "Error: kaput")

	// A silent (empty) error is not printed.
	out2 := &lockedBuffer{}
	cfg.Out = out2
	cfg.Refresh = func(context.Context) error { return cli.Exit("", 1) }
	cfg.redraw(t.Context(), modePolling)
	require.NotContains(t, out2.String(), "Error:")

	// A canceled context draws nothing.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	out3 := &lockedBuffer{}
	cfg.Out = out3
	cfg.redraw(ctx, modePolling)
	require.Empty(t, out3.String())
}

func TestWatchEmitSnapshotIgnoresErrorAfterCancel(t *testing.T) {
	t.Parallel()

	errOut := &lockedBuffer{}
	cfg := &watchConfig{
		JSONL: true, Err: errOut, Out: &lockedBuffer{},
		Fetch: func(context.Context) (json.RawMessage, error) { return nil, errTestFetch },
	}
	cfg.emitSnapshot(t.Context(), modeLive)
	require.Contains(t, errOut.String(), "refresh failed")

	// Invalid raw JSON cannot be marshaled into the snapshot line.
	errOut2 := &lockedBuffer{}
	cfg.Err = errOut2
	cfg.Fetch = func(context.Context) (json.RawMessage, error) { return json.RawMessage("{"), nil }
	cfg.emitSnapshot(t.Context(), modeLive)
	require.Contains(t, errOut2.String(), "refresh failed")
}
