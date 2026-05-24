package slack

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	slackgo "github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"

	"github.com/fclairamb/solidping/server/internal/config"
)

// reconnectBackoff is how long the supervisor waits before re-dialing Slack
// after a connection error / clean disconnect. Slack's Socket Mode endpoint
// itself reconnects automatically inside slack-go's Run loop; this backoff
// only applies when Run() returns an error.
const reconnectBackoff = 5 * time.Second

// SocketStatus is the snapshot returned by the operator status endpoint.
// Notably it never contains the App-Level Token — the token must never
// surface from the supervisor.
type SocketStatus struct {
	Enabled         bool       `json:"enabled"`
	Connected       bool       `json:"connected"`
	LastConnectedAt *time.Time `json:"lastConnectedAt,omitempty"`
	LastError       string     `json:"lastError,omitempty"`
	TeamCount       int        `json:"teamCount"`
}

// SlackSocketSupervisor opens a long-lived Socket Mode WebSocket to Slack
// and dispatches each inbound request into the transport-agnostic
// DispatchEvent / DispatchCommand / DispatchInteraction functions.
//
// It mirrors jmap.Manager — Run blocks until ctx is cancelled, reconnects
// on disconnect, and exposes a GetStatus snapshot.
type SlackSocketSupervisor struct {
	svc *Service
	cfg *config.Config
	log *slog.Logger

	// dialClient lets tests inject a fake socketmode client (e.g. pointed at
	// a httptest server). The default factory uses the real Slack endpoint.
	dialClient func(appToken string) socketModeRunner

	mu        sync.RWMutex
	connected bool
	lastError string
	lastConn  *time.Time
	teamCount int
}

// socketModeRunner is the subset of *socketmode.Client that the supervisor
// uses. Lets tests substitute a fake without depending on a live Slack.
type socketModeRunner interface {
	RunContext(ctx context.Context) error
	AckCtx(ctx context.Context, reqID string, payload any) error
	EventsChan() <-chan socketmode.Event
}

// realSocketModeClient adapts *socketmode.Client to socketModeRunner.
type realSocketModeClient struct {
	c *socketmode.Client
}

func (r *realSocketModeClient) RunContext(ctx context.Context) error {
	return r.c.RunContext(ctx)
}

func (r *realSocketModeClient) AckCtx(ctx context.Context, reqID string, payload any) error {
	return r.c.AckCtx(ctx, reqID, payload)
}

func (r *realSocketModeClient) EventsChan() <-chan socketmode.Event { return r.c.Events }

// NewSlackSocketSupervisor constructs a supervisor wired to svc and cfg.
// The supervisor does not auto-start — call Run from a goroutine tracked
// by the caller's WaitGroup.
func NewSlackSocketSupervisor(svc *Service, cfg *config.Config, log *slog.Logger) *SlackSocketSupervisor {
	if log == nil {
		log = slog.Default()
	}

	s := &SlackSocketSupervisor{
		svc: svc,
		cfg: cfg,
		log: log.With("component", "slack.SlackSocketSupervisor"),
	}
	s.dialClient = s.defaultDialClient

	return s
}

// defaultDialClient is the production factory: builds a real socketmode.Client
// from cfg.Slack.AppToken.
func (s *SlackSocketSupervisor) defaultDialClient(appToken string) socketModeRunner {
	api := slackgo.New("", slackgo.OptionAppLevelToken(appToken))
	c := socketmode.New(api)

	return &realSocketModeClient{c: c}
}

// GetStatus returns a current snapshot. Safe to call concurrently with Run.
// The returned struct never contains the App-Level Token.
func (s *SlackSocketSupervisor) GetStatus() SocketStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	status := SocketStatus{
		Enabled:   s.cfg != nil && s.cfg.Slack.Enabled && s.cfg.Slack.SocketModeEnabled,
		Connected: s.connected,
		LastError: s.lastError,
	}

	if s.lastConn != nil {
		t := *s.lastConn
		status.LastConnectedAt = &t
	}

	status.TeamCount = s.teamCount

	return status
}

// refreshTeamCount asks the service for the current number of installed Slack
// workspaces. Best-effort: errors are swallowed (the dashboard tolerates a
// stale 0). Called on each (re)connect — cheap, runs at most every 5 seconds.
func (s *SlackSocketSupervisor) refreshTeamCount(ctx context.Context) {
	if s.svc == nil {
		return
	}

	count, err := s.svc.CountInstalledTeams(ctx)
	if err != nil {
		return
	}

	s.mu.Lock()
	s.teamCount = count
	s.mu.Unlock()
}

// Run is the supervisor goroutine. Blocks until ctx is cancelled. Returns
// ctx.Err() on graceful shutdown; any other error indicates an unrecoverable
// configuration problem.
func (s *SlackSocketSupervisor) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		appToken := ""
		if s.cfg != nil {
			appToken = s.cfg.Slack.AppToken
		}

		if appToken == "" {
			s.recordError(errors.New("slack: app_token is empty"))
			s.log.WarnContext(ctx, "Slack Socket Mode disabled: app_token is empty")

			if !sleepOrDoneSocket(ctx, reconnectBackoff) {
				return ctx.Err()
			}

			continue
		}

		client := s.dialClient(appToken)
		s.runOnce(ctx, client)

		s.setConnected(false)

		if !sleepOrDoneSocket(ctx, reconnectBackoff) {
			return ctx.Err()
		}
	}
}

// runOnce starts a single RunContext cycle and pumps Events until either
// ctx is cancelled or RunContext exits.
func (s *SlackSocketSupervisor) runOnce(ctx context.Context, client socketModeRunner) {
	cycleCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	runDone := make(chan error, 1)

	go func() {
		runDone <- client.RunContext(cycleCtx)
	}()

	events := client.EventsChan()
	for {
		select {
		case <-ctx.Done():
			cancel()
			<-runDone

			return
		case err := <-runDone:
			if err != nil && !errors.Is(err, context.Canceled) {
				s.recordError(err)
				s.log.WarnContext(ctx, "Slack Socket Mode RunContext exited", "error", err)
			}

			return
		case evt, ok := <-events:
			if !ok {
				cancel()
				<-runDone

				return
			}

			s.handleEvent(cycleCtx, client, evt)
		}
	}
}

// handleEvent dispatches one socketmode.Event. Connection lifecycle events
// update the status snapshot; payload-bearing events are translated and
// routed into the package-level Dispatch* functions.
func (s *SlackSocketSupervisor) handleEvent(ctx context.Context, client socketModeRunner, evt socketmode.Event) {
	switch evt.Type {
	case socketmode.EventTypeConnecting:
		s.log.DebugContext(ctx, "Slack Socket Mode connecting")
	case socketmode.EventTypeConnected:
		s.setConnected(true)
		s.refreshTeamCount(ctx)
		s.log.InfoContext(ctx, "Slack Socket Mode connected")
	case socketmode.EventTypeConnectionError, socketmode.EventTypeIncomingError:
		s.setConnected(false)

		if err, ok := evt.Data.(error); ok {
			s.recordError(err)
		}
	case socketmode.EventTypeInvalidAuth:
		s.setConnected(false)
		s.recordError(errors.New("slack: invalid app-level token (invalid_auth)"))
	case socketmode.EventTypeDisconnect:
		s.setConnected(false)
		s.log.InfoContext(ctx, "Slack Socket Mode disconnected by Slack")
	case socketmode.EventTypeHello:
		// Slack hello — connection established. EventTypeConnected was
		// already fired by the library, so nothing to do here.
	case socketmode.EventTypeEventsAPI:
		s.dispatchEventsAPI(ctx, client, evt)
	case socketmode.EventTypeSlashCommand:
		s.dispatchSlashCommand(ctx, client, evt)
	case socketmode.EventTypeInteractive:
		s.dispatchInteractive(ctx, client, evt)
	default:
		// Unknown internal event type; safe to ignore.
	}
}

// dispatchEventsAPI handles a Slack Events API delivery: ack immediately,
// translate the envelope into our internal *Event, then call DispatchEvent.
func (s *SlackSocketSupervisor) dispatchEventsAPI(
	ctx context.Context, client socketModeRunner, evt socketmode.Event,
) {
	if evt.Request != nil {
		_ = client.AckCtx(ctx, evt.Request.EnvelopeID, nil)
	}

	if evt.Request == nil || len(evt.Request.Payload) == 0 {
		return
	}

	var event Event
	if err := json.Unmarshal(evt.Request.Payload, &event); err != nil {
		s.log.WarnContext(ctx, "Slack Socket Mode: failed to decode events payload", "error", err)

		return
	}

	// Socket Mode delivers the inner shape directly; ensure the dispatcher
	// sees the same envelope it would receive from the HTTP path. The
	// dispatcher only inspects event.TeamID and event.Event.Type, both of
	// which are preserved by the raw decode.
	if err := DispatchEvent(ctx, s.svc, &event); err != nil {
		s.log.WarnContext(ctx, "Slack Socket Mode: DispatchEvent failed",
			"error", err, "team_id", event.TeamID, "event_type", event.Event.Type)
	}
}

// dispatchSlashCommand translates a slack-go SlashCommand into our internal
// Command, acks immediately, and calls DispatchCommand. If DispatchCommand
// returns a synchronous response, we ack with it as the payload.
func (s *SlackSocketSupervisor) dispatchSlashCommand(
	ctx context.Context, client socketModeRunner, evt socketmode.Event,
) {
	sc, ok := evt.Data.(slackgo.SlashCommand)
	if !ok {
		s.log.WarnContext(ctx, "Slack Socket Mode: slash command payload type assertion failed")

		return
	}

	cmd := slashCommandToInternal(sc)

	response, err := DispatchCommand(ctx, s.svc, &cmd)
	if err != nil {
		s.log.WarnContext(ctx, "Slack Socket Mode: DispatchCommand failed",
			"error", err, "command", cmd.Command, "team_id", cmd.TeamID)
	}

	if evt.Request != nil {
		var payload any
		if response != nil {
			payload = response
		}

		_ = client.AckCtx(ctx, evt.Request.EnvelopeID, payload)
	}
}

// dispatchInteractive translates a slack-go InteractionCallback into our
// internal Interaction, acks immediately, and calls DispatchInteraction.
func (s *SlackSocketSupervisor) dispatchInteractive(
	ctx context.Context, client socketModeRunner, evt socketmode.Event,
) {
	cb, ok := evt.Data.(slackgo.InteractionCallback)
	if !ok {
		s.log.WarnContext(ctx, "Slack Socket Mode: interaction payload type assertion failed")

		return
	}

	// Re-marshal-then-unmarshal is the cleanest way to translate between
	// the slack-go struct and our internal Interaction shape (JSON tag
	// names match upstream); it also keeps the conversion centralised in
	// a single place that the existing HTTP path already exercises.
	raw, err := json.Marshal(cb)
	if err != nil {
		s.log.WarnContext(ctx, "Slack Socket Mode: marshal interaction failed", "error", err)

		return
	}

	var interaction Interaction
	if err := json.Unmarshal(raw, &interaction); err != nil {
		s.log.WarnContext(ctx, "Slack Socket Mode: unmarshal interaction failed", "error", err)

		return
	}

	response, err := DispatchInteraction(ctx, s.svc, &interaction)
	if err != nil {
		s.log.WarnContext(ctx, "Slack Socket Mode: DispatchInteraction failed",
			"error", err, "type", interaction.Type, "team_id", interaction.Team.ID)
	}

	if evt.Request != nil {
		var payload any
		if response != nil {
			payload = response
		}

		_ = client.AckCtx(ctx, evt.Request.EnvelopeID, payload)
	}
}

// slashCommandToInternal converts slack-go's SlashCommand into our internal
// Command. ThreadTS is not part of slack-go's struct (the HTTP form carries
// it as `thread_ts`); when Socket Mode delivers the envelope, the field is
// not preserved by the library.
func slashCommandToInternal(sc slackgo.SlashCommand) Command {
	return Command{
		Token:          sc.Token,
		TeamID:         sc.TeamID,
		TeamDomain:     sc.TeamDomain,
		EnterpriseID:   sc.EnterpriseID,
		EnterpriseName: sc.EnterpriseName,
		ChannelID:      sc.ChannelID,
		ChannelName:    sc.ChannelName,
		UserID:         sc.UserID,
		UserName:       sc.UserName,
		Command:        sc.Command,
		Text:           sc.Text,
		ResponseURL:    sc.ResponseURL,
		TriggerID:      sc.TriggerID,
		APIAppID:       sc.APIAppID,
	}
}

func (s *SlackSocketSupervisor) setConnected(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.connected = v

	if v {
		now := time.Now()
		s.lastConn = &now
		s.lastError = ""
	}
}

func (s *SlackSocketSupervisor) recordError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.lastError = err.Error()
}

// sleepOrDoneSocket returns false if ctx was cancelled before d elapsed.
func sleepOrDoneSocket(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
