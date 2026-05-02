package jmap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// SystemParameterKey is the key under which the inbox config lives in the
// `system_parameters` table.
const SystemParameterKey = "email_inbox"

// Manager validation errors.
var (
	ErrSessionURLRequired    = errors.New("jmap: sessionUrl is required")
	ErrAddressDomainRequired = errors.New("jmap: addressDomain is required")
	ErrConfigNotFound        = errors.New("jmap: email_inbox system parameter not configured")
)

// Outcome describes how a Handler decided to dispose of an email.
type Outcome int

const (
	// OutcomeIgnored leaves the email in the inbox; the next handler is tried.
	// If every handler ignores it, the cleanup loop eventually moves it to
	// Trash after FailedRetentionDays.
	OutcomeIgnored Outcome = iota
	// OutcomeProcessed moves the email to the Processed mailbox.
	OutcomeProcessed
	// OutcomeRejected also moves the email to Processed but signals the
	// handler decided this email is permanently invalid (no retry).
	OutcomeRejected
)

// Mailboxes carries the IDs the manager resolved at startup so handlers can
// classify emails relative to them if needed.
type Mailboxes struct {
	Inbox     *Mailbox
	Processed *Mailbox
	Trash     *Mailbox // best effort; may be nil if the server has no trash role
}

// Handler turns an inbox email into a SolidPing-meaningful action. The first
// non-OutcomeIgnored handler wins.
type Handler interface {
	HandleEmail(ctx context.Context, mailboxes *Mailboxes, email Email) (Outcome, error)
}

// Status summarizes the current connection health, suitable for the admin
// status endpoint.
type Status struct {
	Enabled       bool       `json:"enabled"`
	Connected     bool       `json:"connected"`
	Mode          string     `json:"mode,omitempty"` // "push" | "poll" | ""
	LastSyncedAt  *time.Time `json:"lastSyncedAt,omitempty"`
	LastError     string     `json:"lastError,omitempty"`
	AddressDomain string     `json:"addressDomain,omitempty"`
	AccountID     string     `json:"accountId,omitempty"`
}

// Manager is the long-running JMAP inbox supervisor. Handlers are registered
// before Run is called (typically from app/server.go wiring).
type Manager struct {
	db db.Service

	mu        sync.RWMutex
	handlers  []Handler
	connected bool
	cfg       *Config
	client    *Client
	mboxes    *Mailboxes

	statusMu     sync.RWMutex
	lastSyncedAt time.Time
	lastError    string
	mode         string
	syncTrigger  chan struct{}
}

// setMode records whether the supervisor is currently running in push or poll
// mode. Called from runEventSource and runPolling.
func (m *Manager) setMode(mode string) {
	m.statusMu.Lock()
	defer m.statusMu.Unlock()

	m.mode = mode
}

// NewManager builds a Manager. The dbService is used to read the system
// parameter on each connect cycle.
func NewManager(dbService db.Service) *Manager {
	return &Manager{
		db:          dbService,
		syncTrigger: make(chan struct{}, 1),
	}
}

// RegisterHandler adds h to the chain. Must be called before Run.
func (m *Manager) RegisterHandler(h Handler) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.handlers = append(m.handlers, h)
}

// GetStatus returns a snapshot of the manager's current state.
func (m *Manager) GetStatus() Status {
	m.mu.RLock()
	connected := m.connected
	cfg := m.cfg
	client := m.client
	m.mu.RUnlock()

	m.statusMu.RLock()
	last := m.lastSyncedAt
	lastErr := m.lastError
	mode := m.mode
	m.statusMu.RUnlock()

	status := Status{
		Connected: connected,
		Mode:      mode,
	}

	if !connected {
		status.Mode = ""
	}

	if cfg != nil {
		status.Enabled = cfg.Enabled
		status.AddressDomain = cfg.AddressDomain
	}

	if client != nil {
		status.AccountID = client.AccountID()
	}

	if !last.IsZero() {
		t := last
		status.LastSyncedAt = &t
	}

	if lastErr != "" {
		status.LastError = lastErr
	}

	return status
}

// TriggerSync wakes the supervisor for an immediate sync. Returns nil even if
// the manager isn't connected — the trigger is queued and acted on once a
// connection is up. The channel is buffered to depth 1, so concurrent calls
// coalesce.
func (m *Manager) TriggerSync(_ context.Context) error {
	select {
	case m.syncTrigger <- struct{}{}:
	default:
	}

	return nil
}

// TestConnection tries to discover the JMAP session and resolve the inbox /
// processed mailboxes using cfg, without disturbing the running supervisor.
// If cfg is nil, the stored configuration is loaded from system_parameters.
func (m *Manager) TestConnection(ctx context.Context, cfg *Config) (*Mailboxes, error) {
	if cfg == nil {
		stored, err := m.loadConfig(ctx)
		if err != nil {
			return nil, err
		}

		cfg = stored
	}

	cfg.ApplyDefaults()

	if err := validateConfig(cfg); err != nil {
		return nil, err
	}

	client := NewClient(cfg)
	if _, err := client.DiscoverSession(ctx); err != nil {
		return nil, fmt.Errorf("test: discover session: %w", err)
	}

	return resolveMailboxes(ctx, client, cfg)
}

// Run is the supervisor goroutine. It blocks until ctx is canceled.
func (m *Manager) Run(ctx context.Context) error {
	logger := slog.Default().With("component", "jmap.Manager")

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		client, mboxes, cfg, retryAfter := m.connect(ctx, logger)
		if cfg != nil && client != nil && mboxes != nil {
			m.setConfig(cfg, client, mboxes)
			m.setConnected(true)
			logger.InfoContext(ctx, "JMAP session discovered",
				"accountId", client.AccountID(), "inbox", mboxes.Inbox.ID)

			if client.EventSourceURL() != "" {
				m.runEventSource(ctx, client, mboxes, cfg, logger)
			} else {
				m.runPolling(ctx, client, mboxes, cfg, logger)
			}

			m.setConnected(false)
			m.setMode("")
			logger.InfoContext(ctx, "JMAP supervisor cycle ended; will reconnect")

			continue
		}

		if !sleepOrDone(ctx, retryAfter) {
			return ctx.Err()
		}
	}
}

// connect performs one full setup cycle (load config → discover → resolve
// mailboxes). On any failure it returns the duration the caller should sleep
// before retrying, with all out parameters nil. On success all out parameters
// are non-nil.
func (m *Manager) connect(
	ctx context.Context, logger *slog.Logger,
) (*Client, *Mailboxes, *Config, time.Duration) {
	cfg, err := m.loadConfig(ctx)
	if err != nil {
		m.recordError(err)
		logger.WarnContext(ctx, "JMAP config load failed", "error", err)

		return nil, nil, nil, 60 * time.Second
	}

	if cfg == nil || !cfg.Enabled {
		m.setConfig(cfg, nil, nil)

		return nil, nil, nil, 60 * time.Second
	}

	cfg.ApplyDefaults()
	if vErr := validateConfig(cfg); vErr != nil {
		m.recordError(vErr)
		logger.WarnContext(ctx, "JMAP config invalid", "error", vErr)

		return nil, nil, nil, 60 * time.Second
	}

	client := NewClient(cfg)
	if _, dErr := client.DiscoverSession(ctx); dErr != nil {
		m.recordError(dErr)
		logger.WarnContext(ctx, "JMAP discovery failed", "error", dErr)

		return nil, nil, nil, 30 * time.Second
	}

	mboxes, mErr := resolveMailboxes(ctx, client, cfg)
	if mErr != nil {
		m.recordError(mErr)
		logger.WarnContext(ctx, "JMAP mailbox resolution failed", "error", mErr)

		return nil, nil, nil, 30 * time.Second
	}

	return client, mboxes, cfg, 0
}

// runEventSource listens on the SSE channel and triggers a sync each time the
// Email state actually changes. The shared syncLoop also runs a fallback
// ticker so a silently-dropped SSE stream doesn't strand the inbox.
func (m *Manager) runEventSource(
	ctx context.Context, client *Client, mboxes *Mailboxes, cfg *Config, logger *slog.Logger,
) {
	logger.InfoContext(ctx, "starting JMAP EventSource listener",
		"fallbackPollSeconds", cfg.PollIntervalSeconds)

	m.setMode("push")

	if err := m.syncEmails(ctx, client, mboxes, cfg); err != nil {
		m.recordError(err)
	}

	lastEmailState := ""

	go func() {
		_ = client.ListenEventSourceWithReconnect(ctx, "Email", func(event EventSourceEvent) error {
			emailState, ok := extractEmailState(event, client.AccountID())
			if !ok || emailState == lastEmailState {
				return nil
			}

			lastEmailState = emailState

			select {
			case m.syncTrigger <- struct{}{}:
			default:
			}

			return nil
		})
	}()

	m.syncLoop(ctx, client, mboxes, cfg, logger)
}

// runPolling does an initial sync and then enters the shared sync loop. Used
// when the JMAP server doesn't advertise an EventSource URL.
func (m *Manager) runPolling(
	ctx context.Context, client *Client, mboxes *Mailboxes, cfg *Config, logger *slog.Logger,
) {
	logger.InfoContext(ctx, "starting JMAP polling", "intervalSeconds", cfg.PollIntervalSeconds)

	m.setMode("poll")

	if err := m.syncEmails(ctx, client, mboxes, cfg); err != nil {
		m.recordError(err)
	}

	m.syncLoop(ctx, client, mboxes, cfg, logger)
}

// syncLoop runs the steady-state ticker, the cleanup ticker, and the
// SSE-driven trigger. Used by both runEventSource and runPolling so the
// fallback ticker is always active.
func (m *Manager) syncLoop(
	ctx context.Context, client *Client, mboxes *Mailboxes, cfg *Config, logger *slog.Logger,
) {
	syncTicker := time.NewTicker(time.Duration(cfg.PollIntervalSeconds) * time.Second)
	defer syncTicker.Stop()

	cleanupTicker := time.NewTicker(time.Hour)
	defer cleanupTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-syncTicker.C:
			if err := m.syncEmails(ctx, client, mboxes, cfg); err != nil {
				m.recordError(err)
				logger.WarnContext(ctx, "JMAP sync error", "error", err)
			}
		case <-m.syncTrigger:
			if err := m.syncEmails(ctx, client, mboxes, cfg); err != nil {
				m.recordError(err)
				logger.WarnContext(ctx, "JMAP sync error", "error", err)
			}
		case <-cleanupTicker.C:
			if err := m.cleanupOldEmails(ctx, client, mboxes, cfg); err != nil {
				logger.WarnContext(ctx, "JMAP cleanup error", "error", err)
			}
		}
	}
}

// syncEmails queries the inbox, fetches each email, runs the handler chain,
// and moves processed emails to the Processed mailbox.
func (m *Manager) syncEmails(
	ctx context.Context, client *Client, mboxes *Mailboxes, cfg *Config,
) error {
	ids, err := client.EmailQuery(ctx, client.AccountID(), EmailQueryFilter{InMailbox: mboxes.Inbox.ID})
	if err != nil {
		return fmt.Errorf("inbox query: %w", err)
	}

	if len(ids) == 0 {
		m.recordSyncTime(time.Now())

		return nil
	}

	emails, err := client.EmailGet(ctx, client.AccountID(), ids, nil)
	if err != nil {
		return fmt.Errorf("inbox get: %w", err)
	}

	handlers := m.cloneHandlers()

	for i := range emails {
		email := emails[i]

		processedID := ""

		for _, h := range handlers {
			outcome, hErr := h.HandleEmail(ctx, mboxes, email)
			if hErr != nil {
				slog.WarnContext(ctx, "JMAP handler error",
					"messageId", email.ID, "error", hErr)

				continue
			}

			if outcome == OutcomeIgnored {
				continue
			}

			processedID = email.ID

			break
		}

		if processedID == "" {
			continue
		}

		if err := client.EmailSetMailbox(
			ctx, client.AccountID(), []string{processedID}, mboxes.Inbox.ID, mboxes.Processed.ID,
		); err != nil {
			slog.WarnContext(ctx, "JMAP move-to-processed failed",
				"messageId", processedID, "error", err)
		}
	}

	_ = cfg // currently unused inside this function but reserved for retention/labeling decisions

	m.recordSyncTime(time.Now())

	return nil
}

// cleanupOldEmails moves expired emails to Trash and destroys old Trash items.
func (m *Manager) cleanupOldEmails(
	ctx context.Context, client *Client, mboxes *Mailboxes, cfg *Config,
) error {
	if mboxes.Trash == nil {
		return nil
	}

	now := time.Now().UTC()
	processedCutoff := now.Add(-time.Duration(cfg.ProcessedRetentionDays) * 24 * time.Hour).Format(time.RFC3339)
	failedCutoff := now.Add(-time.Duration(cfg.FailedRetentionDays) * 24 * time.Hour).Format(time.RFC3339)
	trashCutoff := now.Add(-7 * 24 * time.Hour).Format(time.RFC3339)

	if err := m.moveOld(ctx, client, mboxes.Processed.ID, mboxes.Trash.ID, processedCutoff); err != nil {
		return fmt.Errorf("processed cleanup: %w", err)
	}

	if err := m.moveOld(ctx, client, mboxes.Inbox.ID, mboxes.Trash.ID, failedCutoff); err != nil {
		return fmt.Errorf("inbox cleanup: %w", err)
	}

	return m.destroyOld(ctx, client, mboxes.Trash.ID, trashCutoff)
}

func (m *Manager) destroyOld(ctx context.Context, client *Client, mailboxID, before string) error {
	ids, err := client.EmailQuery(ctx, client.AccountID(), EmailQueryFilter{
		InMailbox: mailboxID,
		Before:    before,
	})
	if err != nil {
		return fmt.Errorf("trash query: %w", err)
	}

	if len(ids) == 0 {
		return nil
	}

	if err := client.EmailDestroy(ctx, client.AccountID(), ids); err != nil {
		return fmt.Errorf("trash destroy: %w", err)
	}

	return nil
}

func (m *Manager) moveOld(ctx context.Context, client *Client, fromMailboxID, toMailboxID, before string) error {
	ids, err := client.EmailQuery(ctx, client.AccountID(), EmailQueryFilter{
		InMailbox: fromMailboxID,
		Before:    before,
	})
	if err != nil {
		return err
	}

	if len(ids) == 0 {
		return nil
	}

	return client.EmailSetMailbox(ctx, client.AccountID(), ids, fromMailboxID, toMailboxID)
}

// loadConfig reads the stored email_inbox system parameter. If the parameter
// is missing, returns (nil, nil) so the supervisor loop can idle gracefully —
// missing config is not an error condition for the long-running manager.
//
//nolint:nilnil // missing-config is a valid steady state; sentinel would force callers to check both
func (m *Manager) loadConfig(ctx context.Context) (*Config, error) {
	param, err := m.db.GetSystemParameter(ctx, SystemParameterKey)
	if err != nil {
		return nil, err
	}

	if param == nil {
		return nil, nil
	}

	return JSONMapToConfig(param.Value)
}

func (m *Manager) setConfig(cfg *Config, client *Client, mboxes *Mailboxes) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.cfg = cfg
	m.client = client
	m.mboxes = mboxes
}

func (m *Manager) setConnected(v bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.connected = v
}

func (m *Manager) recordError(err error) {
	m.statusMu.Lock()
	defer m.statusMu.Unlock()

	m.lastError = err.Error()
}

func (m *Manager) recordSyncTime(at time.Time) {
	m.statusMu.Lock()
	defer m.statusMu.Unlock()

	m.lastSyncedAt = at
	m.lastError = ""
}

func (m *Manager) cloneHandlers() []Handler {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]Handler, len(m.handlers))
	copy(out, m.handlers)

	return out
}

// validateConfig checks the required fields. It does not validate URL syntax
// — the JMAP discovery step will surface those errors.
func validateConfig(cfg *Config) error {
	if cfg.SessionURL == "" {
		return ErrSessionURLRequired
	}

	if cfg.AddressDomain == "" {
		return ErrAddressDomainRequired
	}

	return nil
}

// resolveMailboxes resolves the Inbox / Processed (creating if missing) and
// best-effort Trash mailboxes. Trash failure is non-fatal — the cleanup loop
// just skips its work in that case.
func resolveMailboxes(ctx context.Context, client *Client, cfg *Config) (*Mailboxes, error) {
	inbox, err := client.FindMailboxByName(ctx, client.AccountID(), cfg.MailboxName)
	if err != nil {
		return nil, fmt.Errorf("inbox lookup: %w", err)
	}

	processed, err := client.FindOrCreateMailbox(ctx, client.AccountID(), cfg.ProcessedMailboxName)
	if err != nil {
		return nil, fmt.Errorf("processed lookup: %w", err)
	}

	mboxes := &Mailboxes{Inbox: inbox, Processed: processed}

	trash, err := client.FindMailboxByRole(ctx, client.AccountID(), MailboxRoleTrash)
	if err == nil {
		mboxes.Trash = trash
	}

	return mboxes, nil
}

// extractEmailState parses an EventSource event and returns the new Email
// state for accountID, or "", false if the event is not a usable state
// change (wrong type, JSON parse error, no Email field for this account).
func extractEmailState(event EventSourceEvent, accountID string) (string, bool) {
	if event.Type != "state" {
		return "", false
	}

	var change StateChange
	if err := json.Unmarshal([]byte(event.Data), &change); err != nil {
		return "", false
	}

	states, ok := change.Changed[accountID]
	if !ok {
		return "", false
	}

	emailState, ok := states["Email"]

	return emailState, ok
}

// sleepOrDone returns false if ctx was canceled before d elapsed.
func sleepOrDone(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// JSONMapToConfig is a helper for the system handler service: it takes the
// JSONMap a system parameter holds and returns a parsed Config. Used by the
// admin TestConnection endpoint when the operator passes credentials in the
// request body before persisting them.
//
// System parameters are stored as `{"value": <payload>}`, so the inner
// "value" entry is unwrapped before decoding.
func JSONMapToConfig(in models.JSONMap) (*Config, error) {
	payload := any(in)
	if inner, ok := in["value"]; ok {
		payload = inner
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal jsonmap: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse jsonmap: %w", err)
	}

	return &cfg, nil
}
