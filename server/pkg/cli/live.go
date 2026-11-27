package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/urfave/cli/v3"
)

const (
	// liveSubprotocol is the protocol the org live socket negotiates.
	liveSubprotocol = "solidping.v2"
	// clearScreen moves the cursor home and clears the screen.
	clearScreen = "\x1b[H\x1b[2J"

	watchDebounce     = 500 * time.Millisecond
	watchPollInterval = 10 * time.Second
	watchBackoffStart = time.Second
	watchBackoffMax   = 30 * time.Second
	watchHandshake    = 10 * time.Second

	modeLive    = "live"
	modePolling = "polling"
)

// ErrWatchNeedsTable is returned when --watch is combined with json/jsonl output.
var ErrWatchNeedsTable = errors.New("--watch only works with table output (not --output json or jsonl)")

// errLiveProtocol reports an unexpected frame during the live socket handshake.
var errLiveProtocol = errors.New("live socket protocol error")

// watchConfig describes one live view.
type watchConfig struct {
	// Title names the command in the header (e.g. "checks list").
	Title string
	// BaseURL is the server URL, Org the organization slug.
	BaseURL string
	Org     string
	// Entity is the collection scope to subscribe to ("checks", "incidents").
	Entity string
	// Token resolves the bearer credential.
	Token func(ctx context.Context) (string, error)
	// Refresh fetches the data and prints the table.
	Refresh func(ctx context.Context) error
	// Out receives the clear sequence and the header.
	Out io.Writer

	Debounce     time.Duration
	PollInterval time.Duration
	BackoffStart time.Duration
	BackoffMax   time.Duration
}

func (c *watchConfig) applyDefaults() {
	if c.Debounce <= 0 {
		c.Debounce = watchDebounce
	}
	if c.PollInterval <= 0 {
		c.PollInterval = watchPollInterval
	}
	if c.BackoffStart <= 0 {
		c.BackoffStart = watchBackoffStart
	}
	if c.BackoffMax <= 0 {
		c.BackoffMax = watchBackoffMax
	}
}

// validateWatch rejects --watch with a non-table output format.
func validateWatch(cmd *cli.Command, cliCtx *Context) error {
	if cmd.Bool("watch") && !cliCtx.IsText() {
		return ErrWatchNeedsTable
	}

	return nil
}

// runWatchCommand runs a live view until SIGINT/SIGTERM, which is a clean exit.
func runWatchCommand(ctx context.Context, cliCtx *Context, title, entity string,
	refresh func(ctx context.Context) error,
) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	return runWatch(ctx, &watchConfig{
		Title:   title,
		BaseURL: cliCtx.Config.URL,
		Org:     cliCtx.GetOrg(),
		Entity:  entity,
		Token:   cliCtx.APIHelper.Token,
		Refresh: refresh,
		Out:     os.Stdout,
	})
}

// runWatch drives the live view: it redraws on live hints, reconnects with
// backoff when the socket drops, and falls back to polling when the socket
// cannot be used at all. It returns nil when ctx is canceled.
func runWatch(ctx context.Context, cfg *watchConfig) error {
	cfg.applyDefaults()

	backoff := cfg.BackoffStart

	for ctx.Err() == nil {
		conn, err := cfg.dial(ctx)
		if err != nil {
			var rejected *handshakeRejectedError
			if errors.As(err, &rejected) {
				return cfg.pollLoop(ctx)
			}

			if ctx.Err() != nil {
				return nil //nolint:nilerr // cancellation is a clean exit
			}

			// Network error: keep the screen fresh while retrying.
			cfg.redraw(ctx, modePolling)

			if !sleepCtx(ctx, backoff) {
				return nil
			}

			backoff = min(backoff*2, cfg.BackoffMax)

			continue
		}

		backoff = cfg.BackoffStart

		cfg.redraw(ctx, modeLive)
		cfg.liveLoop(ctx, conn)

		if !sleepCtx(ctx, backoff) {
			return nil
		}
	}

	return nil
}

// handshakeRejectedError means the server answered the upgrade with an HTTP
// response (401, 404 on an older server, ...): retrying will not help.
type handshakeRejectedError struct {
	status int
}

func (e *handshakeRejectedError) Error() string {
	return fmt.Sprintf("live socket handshake rejected (status %d)", e.status)
}

// dial connects, waits for hello, subscribes and waits for subscribed.
func (c *watchConfig) dial(ctx context.Context) (*websocket.Conn, error) {
	token, err := c.Token(ctx)
	if err != nil {
		// No usable credential: the socket cannot work, REST will report why.
		return nil, &handshakeRejectedError{status: http.StatusUnauthorized}
	}

	url := strings.TrimRight(c.BaseURL, "/") + "/api/v1/orgs/" + c.Org + "/events/ws"

	header := http.Header{}
	header.Set("Authorization", "Bearer "+token)

	//nolint:bodyclose // websocket owns the upgraded body
	conn, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader:   header,
		Subprotocols: []string{liveSubprotocol},
	})
	if err != nil {
		if resp != nil {
			return nil, &handshakeRejectedError{status: resp.StatusCode}
		}

		return nil, fmt.Errorf("dial live socket: %w", err)
	}

	hsCtx, cancel := context.WithTimeout(ctx, watchHandshake)
	defer cancel()

	if err = c.handshake(hsCtx, conn); err != nil {
		_ = conn.CloseNow()

		return nil, err
	}

	return conn, nil
}

type liveMessage struct {
	Type string `json:"type"`
}

func (c *watchConfig) handshake(ctx context.Context, conn *websocket.Conn) error {
	if err := expectMessage(ctx, conn, "hello"); err != nil {
		return err
	}

	sub, err := json.Marshal(map[string]string{"type": "subscribe", "entity": c.Entity})
	if err != nil {
		return fmt.Errorf("marshal subscribe: %w", err)
	}

	if err = conn.Write(ctx, websocket.MessageText, sub); err != nil {
		return fmt.Errorf("send subscribe: %w", err)
	}

	return expectMessage(ctx, conn, "subscribed")
}

func expectMessage(ctx context.Context, conn *websocket.Conn, want string) error {
	_, data, err := conn.Read(ctx)
	if err != nil {
		return fmt.Errorf("read %s: %w", want, err)
	}

	var msg liveMessage
	if err = json.Unmarshal(data, &msg); err != nil || msg.Type != want {
		return fmt.Errorf("%w: expected %s, got %q", errLiveProtocol, want, string(data))
	}

	return nil
}

// liveLoop redraws on debounced update/resync hints until the socket drops or
// ctx is canceled. It always closes conn before returning.
func (c *watchConfig) liveLoop(ctx context.Context, conn *websocket.Conn) {
	hints := make(chan struct{}, 1)
	// The reader must outlive ctx's cancellation: coder/websocket tears the
	// connection down abruptly when a Read's context ends, and we want a
	// clean close handshake first.
	readCtx, stopRead := context.WithCancel(context.WithoutCancel(ctx))
	readDone := make(chan struct{})

	go func() {
		defer close(readDone)

		for {
			_, data, err := conn.Read(readCtx)
			if err != nil {
				return
			}

			var msg liveMessage
			if json.Unmarshal(data, &msg) != nil {
				continue
			}

			if msg.Type == "update" || msg.Type == "resync" {
				select {
				case hints <- struct{}{}:
				default:
				}
			}
		}
	}()

	defer func() {
		_ = conn.Close(websocket.StatusNormalClosure, "bye")
		stopRead()
		_ = conn.CloseNow()
		<-readDone
	}()

	var timer *time.Timer
	var fire <-chan time.Time

	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-readDone:
			return
		case <-hints:
			if timer == nil {
				timer = time.NewTimer(c.Debounce)
				fire = timer.C
			}
		case <-fire:
			timer = nil
			fire = nil

			c.redraw(ctx, modeLive)
		}
	}
}

// pollLoop refetches on a fixed interval when the live socket is unusable.
func (c *watchConfig) pollLoop(ctx context.Context) error {
	for {
		c.redraw(ctx, modePolling)

		if !sleepCtx(ctx, c.PollInterval) {
			return nil
		}
	}
}

func (c *watchConfig) redraw(ctx context.Context, mode string) {
	if ctx.Err() != nil {
		return
	}

	_, _ = fmt.Fprint(c.Out, clearScreen)
	_, _ = fmt.Fprintf(c.Out, "sp %s --watch | org: %s | refreshed: %s | %s\n\n",
		c.Title, c.Org, time.Now().Format("15:04:05"), mode)

	if err := c.Refresh(ctx); err != nil && err.Error() != "" {
		_, _ = fmt.Fprintf(c.Out, "Error: %v\n", err)
	}
}

// sleepCtx waits for d; it returns false when ctx ended first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
