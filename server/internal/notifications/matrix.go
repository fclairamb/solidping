package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

const matrixTimeout = 30 * time.Second

var (
	// ErrMatrixHomeserverNotConfigured is returned when the homeserver URL is missing.
	ErrMatrixHomeserverNotConfigured = errors.New("matrix homeserver URL not configured")
	// ErrMatrixAccessTokenNotConfigured is returned when the access token is missing.
	ErrMatrixAccessTokenNotConfigured = errors.New("matrix access token not configured")
	// ErrMatrixRoomNotConfigured is returned when the room id/alias is missing.
	ErrMatrixRoomNotConfigured = errors.New("matrix room not configured")
	// ErrMatrixUnauthorized is a permanent failure: the access token is bad or expired.
	ErrMatrixUnauthorized = errors.New("matrix request unauthorized: bad or expired access token")
	// ErrMatrixForbidden is a permanent failure: the bot is not a member of the room.
	ErrMatrixForbidden = errors.New("matrix request forbidden: invite the bot to the room")
	// ErrMatrixRoomNotFound is a permanent failure: the room id/alias is unknown.
	ErrMatrixRoomNotFound = errors.New("matrix room or alias not found")
	// ErrMatrixRateLimited is a retryable failure: the homeserver is rate-limiting us.
	ErrMatrixRateLimited = errors.New("matrix request rate limited")
	// ErrMatrixServerError is a retryable failure: the homeserver returned a 5xx.
	ErrMatrixServerError = errors.New("matrix homeserver returned a server error")
	// errMatrixRequestFailed is a generic, non-retryable failure for any other status.
	errMatrixRequestFailed = errors.New("matrix request failed")
)

// MatrixSender sends notifications to a Matrix room via the Client-Server API
// v3 (https://spec.matrix.org/latest/client-server-api/), authenticating with
// a static access token belonging to a bot/dedicated user that has already
// been invited to and joined the target room. Stateless HTTP, no OAuth, no
// webhook — a near-clone of NtfySender.
type MatrixSender struct{}

// Send sends a notification to a Matrix room.
func (s *MatrixSender) Send(ctx context.Context, _ *jobdef.JobContext, payload *Payload) error {
	settings, err := s.parseSettings(payload)
	if err != nil {
		return err
	}

	client := newHTTPClient(matrixTimeout)

	roomID, err := s.resolveRoomID(ctx, client, settings)
	if err != nil {
		return err
	}

	plain, formatted := s.buildContent(payload)

	body, err := json.Marshal(matrixMessageEvent{
		MsgType:       "m.text",
		Body:          plain,
		Format:        "org.matrix.custom.html",
		FormattedBody: formatted,
	})
	if err != nil {
		return fmt.Errorf("marshaling matrix message: %w", err)
	}

	// A fresh UUID per send: the Matrix API requires txnId to be unique per
	// access token, and reusing one would make a retry a silent no-op (the
	// homeserver treats a repeated txnId as the same send).
	txnID := uuid.New().String()
	sendURL := fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/send/m.room.message/%s",
		settings.HomeserverURL, url.PathEscape(roomID), url.PathEscape(txnID))

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, sendURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating matrix request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+settings.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", productName)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("sending matrix notification: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(resp.Body)

	return classifyMatrixResponse(resp.StatusCode, respBody)
}

type matrixSettings struct {
	HomeserverURL string `json:"homeserverUrl"`
	AccessToken   string `json:"accessToken"`
	RoomID        string `json:"roomId"`
}

func (s *MatrixSender) parseSettings(payload *Payload) (*matrixSettings, error) {
	data, err := json.Marshal(payload.Integration.Settings)
	if err != nil {
		return nil, fmt.Errorf("parsing matrix settings: %w", err)
	}

	var settings matrixSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("parsing matrix settings: %w", err)
	}

	// Trailing slash on the homeserver URL is tolerated — it's the most
	// natural way to copy the URL out of Element's settings page.
	settings.HomeserverURL = strings.TrimRight(settings.HomeserverURL, "/")

	if settings.HomeserverURL == "" {
		return nil, ErrMatrixHomeserverNotConfigured
	}

	if settings.AccessToken == "" {
		return nil, ErrMatrixAccessTokenNotConfigured
	}

	if settings.RoomID == "" {
		return nil, ErrMatrixRoomNotConfigured
	}

	return &settings, nil
}

// matrixRoomDirectoryResponse is the body of a successful
// GET /_matrix/client/v3/directory/room/{alias} lookup.
//
//nolint:tagliatelle // JSON tags match the Matrix Client-Server API field names.
type matrixRoomDirectoryResponse struct {
	RoomID  string   `json:"room_id"`
	Servers []string `json:"servers,omitempty"`
}

// resolveRoomID returns settings.RoomID unchanged when it is already a room
// id (`!room:server`). When it is an alias (`#room:server`) it resolves the
// alias to its current room id via the directory API on every call — aliases
// rarely move and the extra GET is cheap, so there is no caching.
func (s *MatrixSender) resolveRoomID(
	ctx context.Context, client *http.Client, settings *matrixSettings,
) (string, error) {
	if !strings.HasPrefix(settings.RoomID, "#") {
		return settings.RoomID, nil
	}

	resolveURL := fmt.Sprintf("%s/_matrix/client/v3/directory/room/%s",
		settings.HomeserverURL, url.PathEscape(settings.RoomID))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resolveURL, nil)
	if err != nil {
		return "", fmt.Errorf("creating matrix alias resolution request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+settings.AccessToken)
	req.Header.Set("User-Agent", productName)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("resolving matrix room alias: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(resp.Body)

	if err := classifyMatrixResponse(resp.StatusCode, respBody); err != nil {
		return "", fmt.Errorf("resolving matrix room alias %s: %w", settings.RoomID, err)
	}

	var directory matrixRoomDirectoryResponse
	if err := json.Unmarshal(respBody, &directory); err != nil {
		return "", fmt.Errorf("parsing matrix alias resolution response: %w", err)
	}

	if directory.RoomID == "" {
		return "", fmt.Errorf("%w: alias %s resolved to an empty room id", ErrMatrixRoomNotFound, settings.RoomID)
	}

	return directory.RoomID, nil
}

// matrixMessageEvent is the m.room.message event body sent to the
// Client-Server API. Both a plain-text and an HTML-formatted body are always
// sent, matching the spec example — Matrix clients that don't understand
// format/formatted_body fall back to body.
//
//nolint:tagliatelle // JSON tags match the Matrix event field names.
type matrixMessageEvent struct {
	MsgType       string `json:"msgtype"`
	Body          string `json:"body"`
	Format        string `json:"format,omitempty"`
	FormattedBody string `json:"formatted_body,omitempty"`
}

// buildContent renders the plain-text and HTML variants of a notification
// event. HTML is built with html.EscapeString on every dynamic value —
// never fmt.Sprintf directly into markup — so a check name or failure
// reason cannot inject markup into the room.
func (s *MatrixSender) buildContent(payload *Payload) (string, string) {
	checkName := getCheckName(payload.Check)

	var title string

	var lines []string

	switch payload.EventType {
	case eventTypeIncidentCreated:
		title = "[DOWN] " + checkName
		lines = s.downLines(payload, checkName)
	case eventTypeIncidentResolved:
		title = "[RECOVERED] " + checkName
		lines = s.resolvedLines(payload, checkName)
	case eventTypeIncidentEscalated:
		title = "[ESCALATED] " + checkName
		lines = s.escalatedLines(payload, checkName)
	case eventTypeIncidentReopened:
		title = fmt.Sprintf("[REOPENED] %s (relapse #%d)", checkName, payload.Incident.RelapseCount)
		lines = s.downLines(payload, checkName)
	case eventTypeIncidentComment:
		title = commentTitle(payload)
		lines = []string{commentPlainBody(payload)}
	default:
		title = "[UPDATE] " + checkName
		lines = []string{"An incident update occurred for " + checkName}
	}

	incidentURL := incidentDashURL(payload.AppBaseURL, payload.OrgSlug, payload.Incident)

	plain := title
	if len(lines) > 0 {
		plain += "\n" + strings.Join(lines, "\n")
	}

	if incidentURL != "" {
		plain += "\n" + incidentURL
	}

	htmlLines := make([]string, len(lines))
	for i, line := range lines {
		htmlLines[i] = html.EscapeString(line)
	}

	formatted := "<strong>" + html.EscapeString(title) + "</strong>"
	if len(htmlLines) > 0 {
		formatted += "<br/>" + strings.Join(htmlLines, "<br/>")
	}

	if incidentURL != "" {
		formatted += fmt.Sprintf(`<br/><a href="%s">View incident</a>`, html.EscapeString(incidentURL))
	}

	return plain, formatted
}

func (s *MatrixSender) downLines(payload *Payload, checkName string) []string {
	return []string{
		fmt.Sprintf("Check: %s (%s)", checkName, payload.Check.Type),
		"Cause: " + getFailureReason(payload.Incident),
		"Failure count: " + strconv.Itoa(payload.Incident.FailureCount),
		"Started: " + payload.Incident.StartedAt.Format(time.RFC3339),
	}
}

func (s *MatrixSender) resolvedLines(payload *Payload, checkName string) []string {
	lines := []string{fmt.Sprintf("Check: %s (%s)", checkName, payload.Check.Type)}

	if payload.Incident.ResolvedAt != nil {
		duration := payload.Incident.ResolvedAt.Sub(payload.Incident.StartedAt)
		lines = append(lines, "Duration: "+formatDuration(duration))
	}

	return lines
}

func (s *MatrixSender) escalatedLines(payload *Payload, checkName string) []string {
	return []string{
		fmt.Sprintf("Check: %s (%s)", checkName, payload.Check.Type),
		"Cause: " + getFailureReason(payload.Incident),
		fmt.Sprintf("Failures: %d", payload.Incident.FailureCount),
		"Duration: " + formatDuration(time.Since(payload.Incident.StartedAt)),
	}
}

// matrixErrorBody is the `{"errcode", "error", "retry_after_ms"}` shape the
// Matrix API returns on a non-2xx response.
//
//nolint:tagliatelle // JSON tags match the Matrix API's standard error body.
type matrixErrorBody struct {
	ErrCode      string `json:"errcode"`
	Error        string `json:"error"`
	RetryAfterMs int64  `json:"retry_after_ms"`
}

// classifyMatrixResponse turns an HTTP status + body into nil (success), a
// permanent error, or a retryable error (wrapped in jobdef.RetryableError, per
// the Sender interface contract that senders should surface transient
// failures as retryable). 401/403/404 are permanent — retrying them cannot
// succeed without operator action. 429 and 5xx are transient.
func classifyMatrixResponse(statusCode int, body []byte) error {
	if statusCode >= http.StatusOK && statusCode < http.StatusMultipleChoices {
		return nil
	}

	switch statusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("%w: %s", ErrMatrixUnauthorized, string(body))
	case http.StatusForbidden:
		return fmt.Errorf("%w: %s", ErrMatrixForbidden, string(body))
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s", ErrMatrixRoomNotFound, string(body))
	case http.StatusTooManyRequests:
		return jobdef.NewRetryableError(matrixRateLimitError(body))
	}

	if statusCode >= http.StatusInternalServerError {
		return jobdef.NewRetryableError(
			fmt.Errorf("%w: status %d: %s", ErrMatrixServerError, statusCode, string(body)),
		)
	}

	return fmt.Errorf("%w: status %d: %s", errMatrixRequestFailed, statusCode, string(body))
}

// matrixRateLimitError builds the 429 error, honoring retry_after_ms from the
// response body when the homeserver provides it.
func matrixRateLimitError(body []byte) error {
	var errBody matrixErrorBody

	_ = json.Unmarshal(body, &errBody)

	if errBody.RetryAfterMs > 0 {
		return fmt.Errorf("%w: retry after %dms: %s", ErrMatrixRateLimited, errBody.RetryAfterMs, string(body))
	}

	return fmt.Errorf("%w: %s", ErrMatrixRateLimited, string(body))
}
