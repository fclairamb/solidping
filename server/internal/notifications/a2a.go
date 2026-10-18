package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	neturl "net/url"
	"strings"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"
	"github.com/a2aproject/a2a-go/v2/a2acompat/a2av0"
	"github.com/google/uuid"
	"golang.org/x/mod/semver"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/egress"
	"github.com/fclairamb/solidping/server/internal/httpclientpool"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
)

// A2A (Agent2Agent) sender, spec 2026-10-18-02. SolidPing is the A2A client:
// every incident lifecycle event becomes one SendMessage to the agent the
// integration's Agent Card describes. The agent investigates through
// SolidPing's MCP server and its findings come back as incident comments,
// either straight from the SendMessage response or through the
// a2a_task_poll job chain.

// Settings keys of an A2A integration.
const (
	// A2ASettingAgentCardURL is the full Agent Card URL, or a bare origin to
	// which a2aWellKnownCardPath is appended.
	A2ASettingAgentCardURL = "agent_card_url"

	// A2AAuthNone, A2AAuthBearer and A2AAuthAPIKey are the values of the
	// "auth" setting. Empty means none.
	A2AAuthNone   = "none"
	A2AAuthBearer = "bearer"
	A2AAuthAPIKey = "api_key"
)

const (
	a2aWellKnownCardPath = "/.well-known/agent-card.json"
	a2aTimeout           = 30 * time.Second
	// a2aCardCacheTTL bounds how long a resolved card is reused. Short, so an
	// agent that moves its endpoint is picked up within minutes.
	a2aCardCacheTTL = 5 * time.Minute
	// a2aMaxErrorBody caps how much of an error response is kept in the
	// error message.
	a2aMaxErrorBody = 512
	// a2aLegacyMajor is the semver major of A2A 0.x (0.3), served through
	// the SDK's a2av0 compat codec.
	a2aLegacyMajor  = "v0"
	a2aCurrentMajor = "v1"

	// A2APollFirstDelay is when the first a2a_task_poll run happens after a
	// send that left the task SUBMITTED or WORKING.
	A2APollFirstDelay = 10 * time.Second
	// A2APollMaxWait is how long SolidPing keeps polling one task, counted
	// from the send. Past it the chain stops with a timeline note.
	A2APollMaxWait = 30 * time.Minute

	// a2aCommentPrefix opens every timeline note SolidPing writes on behalf
	// of the agent itself (not the agent's own words).
	a2aCommentPrefix = "AI agent (A2A)"
)

// a2aPollBackoff is the delay before each poll run, indexed by attempt: 10 s,
// 20 s, 40 s, then every 60 s.
//
//nolint:gochecknoglobals // constant lookup table
var a2aPollBackoff = []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second, 60 * time.Second}

var (
	// ErrA2ACardURLNotConfigured is returned when the Agent Card URL is
	// missing.
	ErrA2ACardURLNotConfigured = newConfigError("a2a agent card url not configured", "Agent Card URL")
	// ErrA2AInvalidSettings flags an unknown auth mode or a missing secret.
	ErrA2AInvalidSettings = errors.New("invalid a2a settings")
	// ErrA2ACardInvalid flags a card that could not be fetched as JSON or
	// parsed. Permanent.
	ErrA2ACardInvalid = errors.New("invalid a2a agent card")
	// ErrA2ANoJSONRPCInterface flags a card without a JSONRPC interface.
	ErrA2ANoJSONRPCInterface = errors.New("a2a agent card has no JSONRPC interface")
	// ErrA2AUnsupportedVersion flags an interface whose protocol version is
	// neither 0.x nor 1.x.
	ErrA2AUnsupportedVersion = errors.New("a2a protocol version not supported")
	// ErrA2AServerError flags a 429/5xx, a timeout or a network error. Always
	// wrapped as retryable.
	ErrA2AServerError = errors.New("a2a server error")
	// ErrA2ARequestFailed flags a 4xx or a JSON-RPC error. Permanent.
	ErrA2ARequestFailed = errors.New("a2a request failed")
)

// A2ASender sends incident events to an AI agent over A2A.
type A2ASender struct{}

type a2aSettings struct {
	AgentCardURL string `json:"agent_card_url"` //nolint:tagliatelle // matches the settings JSONB / dashboard form key
	Auth         string `json:"auth"`
	Token        string `json:"token"`
	APIKeyHeader string `json:"api_key_header"` //nolint:tagliatelle // matches the settings JSONB / dashboard form key
	APIKeyValue  string `json:"api_key_value"`  //nolint:tagliatelle // matches the settings JSONB / dashboard form key
	Instructions string `json:"instructions"`
}

func parseA2ASettings(settings models.JSONMap) (*a2aSettings, error) {
	data, err := json.Marshal(settings)
	if err != nil {
		return nil, fmt.Errorf("parsing a2a settings: %w", err)
	}

	var parsed a2aSettings
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("parsing a2a settings: %w", err)
	}

	parsed.AgentCardURL = strings.TrimSpace(parsed.AgentCardURL)
	if parsed.AgentCardURL == "" {
		return nil, ErrA2ACardURLNotConfigured
	}

	if _, err := parsed.headers(); err != nil {
		return nil, err
	}

	return &parsed, nil
}

// headers returns the auth headers sent on every A2A request.
func (s *a2aSettings) headers() (map[string]string, error) {
	switch s.Auth {
	case "", A2AAuthNone:
		return map[string]string{}, nil
	case A2AAuthBearer:
		if s.Token == "" {
			return nil, fmt.Errorf("%w: bearer token is empty", ErrA2AInvalidSettings)
		}

		return map[string]string{"Authorization": "Bearer " + s.Token}, nil
	case A2AAuthAPIKey:
		name := strings.TrimSpace(s.APIKeyHeader)
		if name == "" || s.APIKeyValue == "" {
			return nil, fmt.Errorf("%w: api key header name and value are required", ErrA2AInvalidSettings)
		}

		return map[string]string{name: s.APIKeyValue}, nil
	default:
		return nil, fmt.Errorf("%w: unknown auth %q", ErrA2AInvalidSettings, s.Auth)
	}
}

// a2aCardURL returns the URL the card is fetched from: the URL as typed when
// it has a path, the well-known card path of the origin otherwise.
func a2aCardURL(raw string) (string, error) {
	parsed, err := neturl.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrSenderURLInvalid, err)
	}

	if parsed.Path == "" || parsed.Path == "/" {
		parsed.Path = a2aWellKnownCardPath
	}

	return parsed.String(), nil
}

// a2aHTTPStatusError is a response status of 400 or more on any A2A request.
// Turned into an error by a2aStatusTransport because the SDK only reports a
// formatted status string, which cannot tell a 503 from a 401.
type a2aHTTPStatusError struct {
	StatusCode int
	Body       string
}

func (e *a2aHTTPStatusError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Body)
}

type a2aStatusTransport struct {
	base http.RoundTripper
}

func (t *a2aStatusTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	// 3xx goes back to the client, which follows it through the same
	// guarded transport.
	if resp.StatusCode < http.StatusBadRequest {
		return resp, nil
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, a2aMaxErrorBody))
	_ = resp.Body.Close()

	return nil, &a2aHTTPStatusError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(body))}
}

// a2aHTTPClient is the guarded client every A2A request goes through: card
// fetch, SendMessage and GetTask. The guard checks every dial, so a redirect
// or a DNS answer to a private address is refused too.
func a2aHTTPClient(guard *egress.Guard) *http.Client {
	base := httpclientpool.NewGuardedClient(a2aTimeout, guard)

	return &http.Client{
		Timeout:   base.Timeout,
		Transport: &a2aStatusTransport{base: base.Transport},
	}
}

// classifyA2AError turns an A2A failure into a retryable error (429, 5xx,
// timeout, network) or a permanent one (4xx, JSON-RPC error, egress denial,
// anything else). A permanent error never keeps the original chain: it may
// hold a *url.Error, which is a net.Error, and the notification job would
// retry it as a network error.
func classifyA2AError(err error) error {
	if err == nil {
		return nil
	}

	var statusErr *a2aHTTPStatusError
	if errors.As(err, &statusErr) {
		if statusErr.StatusCode == http.StatusTooManyRequests || statusErr.StatusCode >= http.StatusInternalServerError {
			return jobdef.NewRetryableError(fmt.Errorf("%w: %s", ErrA2AServerError, statusErr.Error()))
		}

		return fmt.Errorf("%w: %s", ErrA2ARequestFailed, statusErr.Error())
	}

	var denied *egress.DeniedError
	if errors.As(err, &denied) {
		return fmt.Errorf("%w: %s", ErrSenderURLInvalid, denied.Error())
	}

	var rpcErr *a2a.Error
	if errors.As(err, &rpcErr) {
		return fmt.Errorf("%w: %s", ErrA2ARequestFailed, rpcErr.Error())
	}

	if errors.Is(err, context.DeadlineExceeded) || IsNetworkError(err) {
		return jobdef.NewRetryableError(fmt.Errorf("%w: %w", ErrA2AServerError, err))
	}

	return fmt.Errorf("%w: %s", ErrA2ARequestFailed, err.Error())
}

// IsA2ARetryable reports whether an A2A error is worth another attempt.
func IsA2ARetryable(err error) bool {
	return jobdef.IsRetryable(err)
}

// a2aResolved is a card plus the interface SolidPing talks to.
type a2aResolved struct {
	card  *a2a.AgentCard
	iface *a2a.AgentInterface
}

func (r *a2aResolved) legacy() bool {
	return semver.Major("v"+string(r.iface.ProtocolVersion)) == a2aLegacyMajor
}

type a2aCacheEntry struct {
	resolved *a2aResolved
	expires  time.Time
}

// a2aCardCache keeps resolved cards per integration and card URL for
// a2aCardCacheTTL, so a burst of events does not refetch the card each time.
//
//nolint:gochecknoglobals // process-wide cache, keyed per integration.
var a2aCardCache sync.Map

func a2aCacheKey(integrationUID, cardURL, auth string) string {
	return integrationUID + "|" + cardURL + "|" + auth
}

// resolveA2A fetches (or reuses) the Agent Card, picks the first JSONRPC
// interface and checks both URLs against the egress policy.
func resolveA2A(
	ctx context.Context, guard *egress.Guard, integrationUID string, settings *a2aSettings,
) (*a2aResolved, error) {
	if err := ValidateSenderURL(ctx, guard, settings.AgentCardURL); err != nil {
		return nil, err
	}

	cardURL, err := a2aCardURL(settings.AgentCardURL)
	if err != nil {
		return nil, err
	}

	key := a2aCacheKey(integrationUID, cardURL, settings.Auth)
	if integrationUID != "" {
		if cached, ok := a2aCardCache.Load(key); ok {
			entry, _ := cached.(*a2aCacheEntry)
			if entry != nil && time.Now().Before(entry.expires) {
				return entry.resolved, nil
			}
		}
	}

	resolved, err := fetchA2ACard(ctx, guard, cardURL, settings)
	if err != nil {
		return nil, err
	}

	if integrationUID != "" {
		a2aCardCache.Store(key, &a2aCacheEntry{resolved: resolved, expires: time.Now().Add(a2aCardCacheTTL)})
	}

	return resolved, nil
}

func fetchA2ACard(
	ctx context.Context, guard *egress.Guard, cardURL string, settings *a2aSettings,
) (*a2aResolved, error) {
	headers, err := settings.headers()
	if err != nil {
		return nil, err
	}

	opts := make([]agentcard.ResolveOption, 0, len(headers))
	for name, value := range headers {
		opts = append(opts, agentcard.WithRequestHeader(name, value))
	}

	// The a2av0 parser reads both card formats: a 1.0 card's
	// supportedInterfaces as is, a 0.3 card's url/preferredTransport mapped
	// onto one interface with protocolVersion 0.3.
	resolver := &agentcard.Resolver{Client: a2aHTTPClient(guard), CardParser: a2av0.NewAgentCardParser()}

	card, err := resolver.Resolve(ctx, cardURL, opts...)
	if err != nil {
		var notOK *agentcard.ErrStatusNotOK
		if errors.As(err, &notOK) {
			return nil, fmt.Errorf("%w: %s", ErrA2ACardInvalid, notOK.Error())
		}

		var statusErr *a2aHTTPStatusError
		if errors.As(err, &statusErr) || IsNetworkError(err) || errors.Is(err, context.DeadlineExceeded) {
			return nil, classifyA2AError(err)
		}

		return nil, fmt.Errorf("%w: %s", ErrA2ACardInvalid, err.Error())
	}

	iface, err := pickA2AInterface(card)
	if err != nil {
		return nil, err
	}

	// The interface URL comes from the card, which is untrusted: it gets the
	// same check as the URL the user typed (SSRF through the card).
	if err := ValidateSenderURL(ctx, guard, iface.URL); err != nil {
		return nil, err
	}

	return &a2aResolved{card: card, iface: iface}, nil
}

// pickA2AInterface returns a copy of the first JSONRPC interface of the card.
// A missing protocolVersion is read as the current version.
func pickA2AInterface(card *a2a.AgentCard) (*a2a.AgentInterface, error) {
	for _, candidate := range card.SupportedInterfaces {
		if candidate == nil || candidate.ProtocolBinding != a2a.TransportProtocolJSONRPC {
			continue
		}

		iface := *candidate
		if iface.ProtocolVersion == "" {
			iface.ProtocolVersion = a2a.Version
		}

		switch semver.Major("v" + string(iface.ProtocolVersion)) {
		case a2aLegacyMajor, a2aCurrentMajor:
			return &iface, nil
		default:
			return nil, fmt.Errorf("%w: %s", ErrA2AUnsupportedVersion, iface.ProtocolVersion)
		}
	}

	return nil, ErrA2ANoJSONRPCInterface
}

// a2aHeaderInterceptor adds the auth headers to every call, and drops the
// A2A-Version header on a 0.3 interface (0.3 predates it; absent means 0.3).
type a2aHeaderInterceptor struct {
	a2aclient.PassthroughInterceptor

	headers map[string]string
	legacy  bool
}

func (i *a2aHeaderInterceptor) Before(
	ctx context.Context, req *a2aclient.Request,
) (context.Context, any, error) {
	for name, value := range i.headers {
		req.ServiceParams[name] = []string{value}
	}

	if i.legacy {
		delete(req.ServiceParams, a2a.SvcParamVersion)
	}

	return ctx, nil, nil
}

// newA2AClient opens an SDK client on the resolved interface. The 1.0 codec
// and the a2av0 codec are both registered; the interface's protocolVersion
// picks one.
func newA2AClient(
	ctx context.Context, guard *egress.Guard, resolved *a2aResolved, settings *a2aSettings,
) (*a2aclient.Client, error) {
	headers, err := settings.headers()
	if err != nil {
		return nil, err
	}

	httpClient := a2aHTTPClient(guard)

	client, err := a2aclient.NewFromEndpoints(ctx, []*a2a.AgentInterface{resolved.iface},
		a2aclient.WithDefaultsDisabled(),
		a2aclient.WithJSONRPCTransport(httpClient),
		a2av0.WithJSONRPCTransport(a2av0.JSONRPCTransportConfig{Client: httpClient}),
		a2aclient.WithCallInterceptors(&a2aHeaderInterceptor{headers: headers, legacy: resolved.legacy()}),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrA2ARequestFailed, err.Error())
	}

	return client, nil
}

// a2aConnect parses the settings, resolves the card and opens a client.
func a2aConnect(
	ctx context.Context, jctx *jobdef.JobContext, integration *models.Integration,
) (*a2aclient.Client, error) {
	settings, err := parseA2ASettings(integration.Settings)
	if err != nil {
		return nil, err
	}

	guard := egressGuardFrom(jctx)

	resolved, err := resolveA2A(ctx, guard, integration.UID, settings)
	if err != nil {
		return nil, err
	}

	return newA2AClient(ctx, guard, resolved, settings)
}

// a2aHandlesEvent lists the events an AI agent receives. Everything else
// (degraded, SLO burn, check deleted, comments, reminders...) is dropped.
func a2aHandlesEvent(eventType string) bool {
	switch eventType {
	case eventTypeIncidentCreated, eventTypeIncidentReopened, eventTypeIncidentResolved,
		eventTypeIncidentAcknowledged, eventTypeIncidentUnacknowledged:
		return true
	default:
		return false
	}
}

// Send sends one incident event to the agent.
func (s *A2ASender) Send(ctx context.Context, jctx *jobdef.JobContext, payload *Payload) error {
	if !a2aHandlesEvent(payload.EventType) {
		return nil
	}

	client, err := a2aConnect(ctx, jctx, payload.Integration)
	if err != nil {
		return err
	}
	defer func() { _ = client.Destroy() }()

	settings, err := parseA2ASettings(payload.Integration.Settings)
	if err != nil {
		return err
	}

	msg := buildA2AMessage(payload, settings.Instructions)

	result, err := client.SendMessage(ctx, &a2a.SendMessageRequest{
		Message: msg,
		// 1.0 blocks until the task ends by default, which would hold the
		// notification job for the whole investigation.
		Config: &a2a.SendMessageConfig{ReturnImmediately: true},
	})
	if err != nil {
		return classifyA2AError(err)
	}

	payload.MessageID = msg.ID

	s.handleResult(ctx, jctx, payload, result)

	return nil
}

// handleResult records an immediate answer, or starts the poll chain for a
// task still running. Never fails the send: the message was delivered, and a
// retry would only resend it.
func (s *A2ASender) handleResult(
	ctx context.Context, jctx *jobdef.JobContext, payload *Payload, result a2a.SendMessageResult,
) {
	integration := payload.Integration
	incidentUID := payload.Incident.UID

	switch res := result.(type) {
	case *a2a.Message:
		RecordA2AComment(ctx, jctx, integration, incidentUID, a2aMessageText(res))
	case *a2a.Task:
		if A2ATaskSettled(res.Status.State) {
			RecordA2AComment(ctx, jctx, integration, incidentUID, A2ATaskComment(res))

			return
		}

		EnqueueA2ATaskPoll(ctx, jctx, &A2ATaskPollConfig{
			OrgUID:         integration.OrganizationUID,
			IntegrationUID: integration.UID,
			A2AIncidentUID: incidentUID,
			TaskID:         string(res.ID),
			Attempt:        0,
			Deadline:       time.Now().Add(A2APollMaxWait),
		}, A2APollFirstDelay)
	}
}

// a2aMessageID derives a stable messageId from the incident and the event
// that triggered the delivery, so a retried notification job resends the
// same id and the agent can drop the duplicate.
func a2aMessageID(payload *Payload) string {
	key := payload.EventKey
	if key == "" {
		key = payload.EventType
	}

	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("solidping:a2a:"+payload.Incident.UID+":"+key)).String()
}

// a2aEventLabel names the event in the message the agent reads:
// "incident.created" reads "opened", the others drop their prefix.
func a2aEventLabel(eventType string) string {
	if eventType == eventTypeIncidentCreated {
		return "opened"
	}

	return strings.TrimPrefix(eventType, "incident.")
}

func a2aIncidentStatus(incident *models.Incident) string {
	switch {
	case incident.State == models.IncidentStateResolved:
		return a2aEventLabel(eventTypeIncidentResolved)
	case incident.AcknowledgedAt != nil:
		return "acknowledged"
	default:
		return "active"
	}
}

func a2aMCPURL(baseURL string) string {
	if baseURL == "" {
		return ""
	}

	return strings.TrimRight(baseURL, "/") + "/api/mcp"
}

func a2aTime(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}

	return t.UTC().Format(time.RFC3339)
}

// buildA2AMessage is the message the agent receives: a human summary in a
// text part and the ids in a data part.
func buildA2AMessage(payload *Payload, instructions string) *a2a.Message {
	incident := payload.Incident
	check := payload.Check
	checkName := getCheckName(check)
	status := a2aIncidentStatus(incident)
	reason := getFailureReason(incident)
	dashboardURL := incidentDashURL(payload.AppBaseURL, payload.OrgSlug, incident)
	mcpURL := a2aMCPURL(payload.AppBaseURL)
	startedAt := a2aTime(&incident.StartedAt)
	resolvedAt := a2aTime(incident.ResolvedAt)

	var text strings.Builder

	fmt.Fprintf(&text, "SolidPing incident %s: %s%s\n", a2aEventLabel(payload.EventType),
		incidentRefPrefix(incident), checkName)
	fmt.Fprintf(&text, "Status: %s\n", status)
	fmt.Fprintf(&text, "Failure reason: %s\n", reason)
	fmt.Fprintf(&text, "Started at: %s\n", startedAt)

	if resolvedAt != "" {
		fmt.Fprintf(&text, "Resolved at: %s\n", resolvedAt)
	}

	if dashboardURL != "" {
		fmt.Fprintf(&text, "Incident: %s\n", dashboardURL)
	}

	if url := checkDashURL(payload.AppBaseURL, payload.OrgSlug, check); url != "" {
		fmt.Fprintf(&text, "Check: %s\n", url)
	}

	if trimmed := strings.TrimSpace(instructions); trimmed != "" {
		fmt.Fprintf(&text, "\n%s\n", trimmed)
	}

	mcpHint := "\nYou can investigate with SolidPing's MCP tools (get_incident, diagnose_check, list_results) " +
		"using the incidentUid, checkUid and orgSlug of the data part"
	if mcpURL != "" {
		mcpHint += ", MCP server: " + mcpURL
	}

	text.WriteString(mcpHint + ".")

	checkSlug := ""
	if check.Slug != nil {
		checkSlug = *check.Slug
	}

	data := map[string]any{
		"incidentUid":   incident.UID,
		"checkUid":      check.UID,
		"checkSlug":     checkSlug,
		"orgSlug":       payload.OrgSlug,
		"eventType":     payload.EventType,
		"status":        status,
		"failureReason": reason,
		"startedAt":     startedAt,
		"resolvedAt":    resolvedAt,
		"dashboardUrl":  dashboardURL,
		"mcpUrl":        mcpURL,
	}

	return &a2a.Message{
		ID: a2aMessageID(payload),
		// One conversation per incident; no taskId, so each event starts a
		// new task in that context (1.0 rejects a message to an ended task).
		ContextID: incident.UID,
		Role:      a2a.MessageRoleUser,
		Parts:     a2a.ContentParts{a2a.NewTextPart(text.String()), a2a.NewDataPart(data)},
	}
}

// a2aPartsText joins the text parts. Data and file parts are ignored.
func a2aPartsText(parts a2a.ContentParts) string {
	texts := make([]string, 0, len(parts))

	for _, part := range parts {
		if part == nil {
			continue
		}

		if text := strings.TrimSpace(part.Text()); text != "" {
			texts = append(texts, text)
		}
	}

	return strings.Join(texts, "\n\n")
}

func a2aMessageText(msg *a2a.Message) string {
	if msg == nil {
		return ""
	}

	return a2aPartsText(msg.Parts)
}

// A2ATaskSettled reports whether a task state ends the polling: terminal
// (completed, failed, canceled, rejected) or interrupted (input or auth
// required, which SolidPing does not answer).
func A2ATaskSettled(state a2a.TaskState) bool {
	return state.Terminal() || state == a2a.TaskStateInputRequired || state == a2a.TaskStateAuthRequired
}

// A2ATaskComment renders a settled task as the comment recorded on the
// incident timeline.
func A2ATaskComment(task *a2a.Task) string {
	statusText := a2aMessageText(task.Status.Message)

	switch task.Status.State { //nolint:exhaustive // the default branch renders every other settled state.
	case a2a.TaskStateCompleted:
		texts := make([]string, 0, len(task.Artifacts)+1)

		for _, artifact := range task.Artifacts {
			if artifact == nil {
				continue
			}

			if text := a2aPartsText(artifact.Parts); text != "" {
				texts = append(texts, text)
			}
		}

		if len(texts) == 0 && statusText != "" {
			texts = append(texts, statusText)
		}

		if len(texts) == 0 {
			return a2aCommentPrefix + ": task completed without a text result."
		}

		return strings.Join(texts, "\n\n")
	case a2a.TaskStateInputRequired, a2a.TaskStateAuthRequired:
		what := "input"
		if task.Status.State == a2a.TaskStateAuthRequired {
			what = "authorization"
		}

		line := fmt.Sprintf("%s is waiting for a human (%s required). SolidPing does not answer it.",
			a2aCommentPrefix, what)
		if statusText != "" {
			line += "\n\n" + statusText
		}

		return line
	default:
		line := fmt.Sprintf("%s task %s.", a2aCommentPrefix, a2aStateWord(task.Status.State))
		if statusText != "" {
			line += "\n\n" + statusText
		}

		return line
	}
}

func a2aStateWord(state a2a.TaskState) string {
	switch state { //nolint:exhaustive // the default branch spells any other state.
	case a2a.TaskStateFailed:
		return "failed"
	case a2a.TaskStateCanceled:
		return "canceled"
	case a2a.TaskStateRejected:
		return "rejected"
	default:
		return strings.ToLower(strings.TrimPrefix(string(state), "TASK_STATE_"))
	}
}

// A2ATimeoutNote is the note recorded when the poll chain gives up.
const A2ATimeoutNote = a2aCommentPrefix + " still working after 30 min, stopped waiting."

// RecordA2AComment records text on the incident timeline as a comment
// authored by the integration. Best-effort: a failure is logged, since the
// agent call itself already happened.
func RecordA2AComment(
	ctx context.Context, jctx *jobdef.JobContext, integration *models.Integration, incidentUID, text string,
) {
	if strings.TrimSpace(text) == "" {
		return
	}

	if jctx == nil || jctx.Services == nil || jctx.Services.A2AComments == nil {
		a2aLogger(jctx).WarnContext(ctx, "No A2A comment recorder, dropping the AI agent reply",
			"integrationUid", integration.UID, "incidentUid", incidentUID)

		return
	}

	if _, err := jctx.Services.A2AComments.AddCommentFromA2A(
		ctx, integration.OrganizationUID, incidentUID, integration.UID, integration.Name, text,
	); err != nil {
		a2aLogger(jctx).WarnContext(ctx, "Failed to record the AI agent reply on the incident",
			"integrationUid", integration.UID, "incidentUid", incidentUID, "error", err)
	}
}

// A2ATaskPollConfig is the whole state of one a2a_task_poll run. The incident
// is stored under a2aIncidentUid, not incidentUid: the incident cancel sweep
// drops every pending job whose config names the incident under incidentUid,
// and an investigation's findings still belong on a resolved incident.
type A2ATaskPollConfig struct {
	OrgUID         string    `json:"orgUid"`
	IntegrationUID string    `json:"integrationUid"`
	A2AIncidentUID string    `json:"a2aIncidentUid"`
	TaskID         string    `json:"taskId"`
	Attempt        int       `json:"attempt"`
	Deadline       time.Time `json:"deadline"`
}

// A2APollDelay is the delay before the poll run of the given attempt.
func A2APollDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}

	if attempt >= len(a2aPollBackoff) {
		return a2aPollBackoff[len(a2aPollBackoff)-1]
	}

	return a2aPollBackoff[attempt]
}

// EnqueueA2ATaskPoll schedules one a2a_task_poll run after delay.
func EnqueueA2ATaskPoll(ctx context.Context, jctx *jobdef.JobContext, cfg *A2ATaskPollConfig, delay time.Duration) {
	if jctx == nil || jctx.Services == nil || jctx.Services.Jobs == nil {
		a2aLogger(jctx).WarnContext(ctx, "No job service, cannot poll the AI agent task", "taskId", cfg.TaskID)

		return
	}

	raw, err := json.Marshal(cfg)
	if err != nil {
		a2aLogger(jctx).WarnContext(ctx, "Failed to marshal the A2A poll config", "error", err)

		return
	}

	scheduledAt := time.Now().Add(delay)

	if _, err := jctx.Services.Jobs.CreateJob(
		ctx, cfg.OrgUID, string(jobdef.JobTypeA2ATaskPoll), raw, &jobsvc.JobOptions{ScheduledAt: &scheduledAt},
	); err != nil {
		a2aLogger(jctx).WarnContext(ctx, "Failed to schedule the A2A poll job",
			"taskId", cfg.TaskID, "error", err)
	}
}

// A2AGetTask makes one GetTask call for the integration's agent.
func A2AGetTask(
	ctx context.Context, jctx *jobdef.JobContext, integration *models.Integration, taskID string,
) (*a2a.Task, error) {
	client, err := a2aConnect(ctx, jctx, integration)
	if err != nil {
		return nil, err
	}
	defer func() { _ = client.Destroy() }()

	task, err := client.GetTask(ctx, &a2a.GetTaskRequest{ID: a2a.TaskID(taskID)})
	if err != nil {
		return nil, classifyA2AError(err)
	}

	return task, nil
}

// A2AAgentInfo is what the dashboard's Test button shows before saving.
type A2AAgentInfo struct {
	Name            string          `json:"name"`
	Description     string          `json:"description,omitempty"`
	ProtocolVersion string          `json:"protocolVersion"`
	InterfaceURL    string          `json:"interfaceUrl"`
	Skills          []A2AAgentSkill `json:"skills"`
}

// A2AAgentSkill is one skill of the card.
type A2AAgentSkill struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ResolveA2AAgent resolves the card for settings that may not be saved yet,
// with no cache, and describes the agent SolidPing would talk to.
func ResolveA2AAgent(ctx context.Context, guard *egress.Guard, settings models.JSONMap) (*A2AAgentInfo, error) {
	parsed, err := parseA2ASettings(settings)
	if err != nil {
		return nil, err
	}

	resolved, err := resolveA2A(ctx, guard, "", parsed)
	if err != nil {
		return nil, err
	}

	info := &A2AAgentInfo{
		Name:            resolved.card.Name,
		Description:     resolved.card.Description,
		ProtocolVersion: string(resolved.iface.ProtocolVersion),
		InterfaceURL:    resolved.iface.URL,
		Skills:          make([]A2AAgentSkill, 0, len(resolved.card.Skills)),
	}

	for i := range resolved.card.Skills {
		skill := &resolved.card.Skills[i]
		info.Skills = append(info.Skills, A2AAgentSkill{ID: skill.ID, Name: skill.Name, Description: skill.Description})
	}

	return info, nil
}

func a2aLogger(jctx *jobdef.JobContext) *slog.Logger {
	if jctx == nil || jctx.Logger == nil {
		return slog.Default()
	}

	return jctx.Logger
}
