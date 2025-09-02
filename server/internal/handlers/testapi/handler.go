// Package testapi provides test HTTP handlers for development and testing.
package testapi

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

var (
	// ErrPeriodRange is returned when period is outside valid range.
	ErrPeriodRange = errors.New("period must be between 1 and 86400 seconds")
	// ErrDelayRange is returned when delay is outside valid range.
	ErrDelayRange = errors.New("delay must be between 0 and 30000 milliseconds")
	// ErrInvalidFormat is returned when format is not supported.
	ErrInvalidFormat = errors.New("format must be 'json', 'xml', or 'text'")
	// ErrInvalidMethod is returned when HTTP method is not supported.
	ErrInvalidMethod = errors.New("supportedMethod must be GET, POST, PUT, DELETE, or PATCH")
	// ErrInvalidSlowResponse is returned when slow response format is invalid.
	ErrInvalidSlowResponse = errors.New("slowResponse must be in format: iterations,bytes,delay_ms")
	// ErrSlowResponseIterations is returned when iterations value is invalid.
	ErrSlowResponseIterations = errors.New("slowResponse iterations must be between 1 and 100")
	// ErrSlowResponseBytes is returned when bytes value is invalid.
	ErrSlowResponseBytes = errors.New("slowResponse bytes must be positive")
	// ErrSlowResponseDelay is returned when delay value is invalid.
	ErrSlowResponseDelay = errors.New("slowResponse delay_ms must be non-negative")
	// ErrRedirectSSRF is returned when redirect target is internal/private IP.
	ErrRedirectSSRF = errors.New("redirect to internal/private IPs not allowed")
	// ErrStreamingNotSupported is returned when streaming is not supported.
	ErrStreamingNotSupported = errors.New("streaming not supported")
)

// Handler provides test HTTP endpoints.
type Handler struct {
	jobSvc        jobsvc.Service
	dbService     db.Service
	eventNotifier notifier.EventNotifier
}

// NewHandler creates a new test handler.
func NewHandler(jobSvc jobsvc.Service, dbService db.Service, eventNotifier notifier.EventNotifier) *Handler {
	return &Handler{
		jobSvc:        jobSvc,
		dbService:     dbService,
		eventNotifier: eventNotifier,
	}
}

// ListStateEntries lists state entries matching a prefix.
// GET /api/v1/test/state-entries?prefix=email_registration:.
func (h *Handler) ListStateEntries(writer http.ResponseWriter, req bunrouter.Request) error {
	prefix := req.URL.Query().Get("prefix")

	entries, err := h.dbService.ListStateEntries(req.Context(), nil, prefix)
	if err != nil {
		return h.writeInternalError(writer, err)
	}

	return h.writeJSON(writer, http.StatusOK, map[string]any{
		"data": entries,
	})
}

// EmailJobRequest represents the request body for triggering an email job.
type EmailJobRequest struct {
	To     string         `json:"to"`
	Type   string         `json:"type"`
	Params map[string]any `json:"params"`
}

// getTemplateForType returns the template file for an email type.
func getTemplateForType(emailType string) (string, bool) {
	switch emailType {
	case "welcome":
		return "welcome.html", true
	case "incident":
		return "incident.html", true
	default:
		return "", false
	}
}

// CreateEmailJob creates an email job for testing.
// POST /api/v1/test/jobs.
func (h *Handler) CreateEmailJob(writer http.ResponseWriter, req bunrouter.Request) error {
	var body EmailJobRequest

	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.writeError(writer, http.StatusBadRequest, "VALIDATION_ERROR", "Invalid request body")
	}

	// Validate request
	if body.To == "" {
		return h.writeError(writer, http.StatusBadRequest, "VALIDATION_ERROR", "Field 'to' is required")
	}

	if body.Type == "" {
		return h.writeError(writer, http.StatusBadRequest, "VALIDATION_ERROR", "Field 'type' is required")
	}

	// Look up template
	template, ok := getTemplateForType(body.Type)
	if !ok {
		return h.writeError(writer, http.StatusBadRequest, "VALIDATION_ERROR",
			"Unknown email type '"+body.Type+"'. Supported types: welcome, incident")
	}

	// Build email job config
	emailConfig := jobtypes.EmailJobConfig{
		To:           []string{body.To},
		Subject:      h.getSubjectForType(body.Type, body.Params),
		Template:     template,
		TemplateData: body.Params,
	}

	configBytes, err := json.Marshal(emailConfig)
	if err != nil {
		return h.writeInternalError(writer, err)
	}

	// Create the job (system job, no org)
	job, err := h.jobSvc.CreateJob(req.Context(), "", string(jobdef.JobTypeEmail), configBytes, nil)
	if err != nil {
		return h.writeInternalError(writer, err)
	}

	return h.writeJSON(writer, http.StatusCreated, map[string]any{
		"message": "Email job created",
		"job":     job,
	})
}

// getSubjectForType returns a subject line based on email type.
func (h *Handler) getSubjectForType(emailType string, params map[string]any) string {
	switch emailType {
	case "welcome":
		return "[SolidPing] Welcome!"
	case "incident":
		if checkName, ok := params["CheckName"].(string); ok {
			return "[SolidPing] Alert: " + checkName
		}

		return "[SolidPing] Alert"
	default:
		return "[SolidPing] Notification"
	}
}

// writeJSON writes a JSON response.
func (h *Handler) writeJSON(w http.ResponseWriter, status int, data any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	return json.NewEncoder(w).Encode(data)
}

// writeError writes a JSON error response.
func (h *Handler) writeError(w http.ResponseWriter, status int, code, message string) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	return json.NewEncoder(w).Encode(map[string]any{
		"title":  message,
		"code":   code,
		"detail": message,
	})
}

// writeInternalError writes a 500 error response.
func (h *Handler) writeInternalError(w http.ResponseWriter, err error) error {
	return h.writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
}

// fakeParams holds parsed parameters for the fake endpoint.
type fakeParams struct {
	period          int64
	format          string
	delay           int
	statusUp        int
	statusDown      int
	supportedMethod string
	requiredHeader  string
	requiredAuth    string
	redirectTo      string
	redirectStatus  int
	setCookie       string
	setHeader       string
	slowResponse    *slowResponseParams
}

// slowResponseParams holds parsed slow response parameters.
type slowResponseParams struct {
	iterations int
	bytes      int
	delayMs    int
}

// fakeResponse represents the JSON/XML response structure.
type fakeResponse struct {
	XMLName   xml.Name `json:"-"         xml:"response"`
	Status    string   `json:"status"    xml:"status"`
	Timestamp string   `json:"timestamp" xml:"timestamp"`
}

// FakeAPI simulates various up/down states and response behaviors.
// GET /api/v1/fake.
//
//nolint:cyclop,funlen,nestif
func (h *Handler) FakeAPI(writer http.ResponseWriter, req bunrouter.Request) error {
	params, err := h.parseFakeParams(req.Request)
	if err != nil {
		return h.writeError(writer, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
	}

	// Check authentication first (401)
	if params.requiredAuth != "" {
		if !h.checkBasicAuth(req.Request, params.requiredAuth) {
			writer.Header().Set("WWW-Authenticate", `Basic realm="Fake API"`)
			return h.writeError(writer, http.StatusUnauthorized, "UNAUTHORIZED", "Invalid credentials")
		}
	}

	// Check required header (400)
	if params.requiredHeader != "" {
		if !h.checkRequiredHeader(req.Request, params.requiredHeader) {
			return h.writeError(writer, http.StatusBadRequest, "VALIDATION_ERROR", "Required header missing or invalid")
		}
	}

	// Check HTTP method (405)
	if req.Method != params.supportedMethod {
		writer.Header().Set("Allow", params.supportedMethod)
		return h.writeError(writer, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED",
			fmt.Sprintf("Only %s is allowed", params.supportedMethod))
	}

	// Calculate state based on time
	isUp := h.calculateState(params.period)
	status := params.statusDown
	statusText := "down"

	if isUp {
		status = params.statusUp
		statusText = "up"
	}

	// Apply delay before responding
	if params.delay > 0 {
		time.Sleep(time.Duration(params.delay) * time.Millisecond)
	}

	// Handle redirects (only when up)
	if params.redirectTo != "" && isUp {
		// Validate redirect URL to prevent SSRF
		if err := h.validateRedirectURL(params.redirectTo); err != nil {
			return h.writeError(writer, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		}
		writer.Header().Set("Location", params.redirectTo)
		writer.WriteHeader(params.redirectStatus)
		return nil
	}

	// Set cookies and headers when up
	if isUp {
		if params.setCookie != "" {
			parts := strings.SplitN(params.setCookie, "=", 2)
			if len(parts) == 2 {
				http.SetCookie(writer, &http.Cookie{
					Name:  parts[0],
					Value: parts[1],
					Path:  "/",
				})
			}
		}

		if params.setHeader != "" {
			parts := strings.SplitN(params.setHeader, "=", 2)
			if len(parts) == 2 {
				writer.Header().Set(parts[0], parts[1])
			}
		}
	}

	// Handle slow response
	if params.slowResponse != nil {
		return h.writeSlowResponse(writer, status, params.slowResponse)
	}

	// Build response
	response := fakeResponse{
		Status:    statusText,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	// Format response based on requested format
	switch params.format {
	case "xml":
		writer.Header().Set("Content-Type", "application/xml")
		writer.WriteHeader(status)
		xmlBytes, err := xml.MarshalIndent(response, "", "  ")
		if err != nil {
			return err
		}
		_, err = writer.Write([]byte(xml.Header + string(xmlBytes)))
		return err

	case "text":
		writer.Header().Set("Content-Type", "text/plain")
		writer.WriteHeader(status)
		_, err := fmt.Fprintf(writer, "status: %s\ntimestamp: %s\n", response.Status, response.Timestamp)
		return err

	default: // json
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(status)
		return json.NewEncoder(writer).Encode(response)
	}
}

// parseFakeParams parses and validates query parameters for the fake endpoint.
//
//nolint:cyclop
func (h *Handler) parseFakeParams(req *http.Request) (*fakeParams, error) {
	query := req.URL.Query()

	params := &fakeParams{
		period:          getInt64Param(query, "period", 120),
		format:          getStringParam(query, "format", "json"),
		delay:           getIntParam(query, "delay", 0),
		statusUp:        getIntParam(query, "statusUp", 200),
		statusDown:      getIntParam(query, "statusDown", 500),
		supportedMethod: strings.ToUpper(getStringParam(query, "supportedMethod", "GET")),
		requiredHeader:  query.Get("requiredHeader"),
		requiredAuth:    query.Get("requiredAuth"),
		redirectTo:      query.Get("redirectTo"),
		redirectStatus:  getIntParam(query, "redirectStatus", 302),
		setCookie:       query.Get("setCookie"),
		setHeader:       query.Get("setHeader"),
	}

	// Validate parameters
	if params.period < 1 || params.period > 86400 {
		return nil, ErrPeriodRange
	}

	if params.delay < 0 || params.delay > 30000 {
		return nil, ErrDelayRange
	}

	if params.format != "json" && params.format != "xml" && params.format != "text" {
		return nil, ErrInvalidFormat
	}

	validMethods := map[string]bool{"GET": true, "POST": true, "PUT": true, "DELETE": true, "PATCH": true}
	if !validMethods[params.supportedMethod] {
		return nil, ErrInvalidMethod
	}

	// Parse slow response if provided
	if slowResponseStr := query.Get("slowResponse"); slowResponseStr != "" {
		parts := strings.Split(slowResponseStr, ",")
		if len(parts) != 3 {
			return nil, ErrInvalidSlowResponse
		}

		iterations, err := strconv.Atoi(parts[0])
		if err != nil || iterations < 1 || iterations > 100 {
			return nil, ErrSlowResponseIterations
		}

		bytes, err := strconv.Atoi(parts[1])
		if err != nil || bytes < 1 {
			return nil, ErrSlowResponseBytes
		}

		delayMs, err := strconv.Atoi(parts[2])
		if err != nil || delayMs < 0 {
			return nil, ErrSlowResponseDelay
		}

		params.slowResponse = &slowResponseParams{
			iterations: iterations,
			bytes:      bytes,
			delayMs:    delayMs,
		}
	}

	return params, nil
}

// calculateState determines if the endpoint should be "up" based on time.
func (h *Handler) calculateState(period int64) bool {
	return (time.Now().Unix()/period)%2 == 0
}

// checkBasicAuth validates Basic authentication credentials.
func (h *Handler) checkBasicAuth(req *http.Request, requiredAuth string) bool {
	parts := strings.SplitN(requiredAuth, ",", 2)
	if len(parts) != 2 {
		return false
	}

	expectedUsername, expectedPassword := parts[0], parts[1]

	username, password, ok := req.BasicAuth()
	if !ok {
		return false
	}

	return username == expectedUsername && password == expectedPassword
}

// checkRequiredHeader validates that a required header is present with correct value.
func (h *Handler) checkRequiredHeader(req *http.Request, requiredHeader string) bool {
	parts := strings.SplitN(requiredHeader, "=", 2)
	if len(parts) != 2 {
		return false
	}

	headerName, expectedValue := parts[0], parts[1]
	actualValue := req.Header.Get(headerName)

	return actualValue == expectedValue
}

// validateRedirectURL validates the redirect URL to prevent SSRF attacks.
func (h *Handler) validateRedirectURL(redirectURL string) error {
	parsedURL, err := url.Parse(redirectURL)
	if err != nil {
		return fmt.Errorf("invalid redirect URL: %w", err)
	}

	// Block internal/private IPs
	if parsedURL.Hostname() == "localhost" ||
		strings.HasPrefix(parsedURL.Hostname(), "127.") ||
		strings.HasPrefix(parsedURL.Hostname(), "192.168.") ||
		strings.HasPrefix(parsedURL.Hostname(), "10.") ||
		strings.HasPrefix(parsedURL.Hostname(), "172.16.") {
		return ErrRedirectSSRF
	}

	return nil
}

// writeSlowResponse writes a slow streaming response with random data.
func (h *Handler) writeSlowResponse(writer http.ResponseWriter, status int, params *slowResponseParams) error {
	writer.Header().Set("Content-Type", "text/plain")
	writer.WriteHeader(status)

	flusher, ok := writer.(http.Flusher)
	if !ok {
		return ErrStreamingNotSupported
	}

	// Use a fixed seed for reproducible random data
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	for i := 0; i < params.iterations; i++ {
		// Generate random printable ASCII bytes
		chunk := make([]byte, params.bytes)
		for j := 0; j < params.bytes; j++ {
			chunk[j] = byte(rng.Intn(94) + 32) // ASCII printable characters (32-126)
		}

		if _, err := writer.Write(chunk); err != nil {
			return err
		}

		if _, err := writer.Write([]byte("\n")); err != nil {
			return err
		}

		flusher.Flush()

		// Don't delay after the last chunk
		if i < params.iterations-1 && params.delayMs > 0 {
			time.Sleep(time.Duration(params.delayMs) * time.Millisecond)
		}
	}

	return nil
}

// getIntParam extracts an integer query parameter with a default value.
func getIntParam(query url.Values, key string, defaultValue int) int {
	if value := query.Get(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}

	return defaultValue
}

// getInt64Param extracts an int64 query parameter with a default value.
func getInt64Param(query url.Values, key string, defaultValue int64) int64 {
	if value := query.Get(key); value != "" {
		if intValue, err := strconv.ParseInt(value, 10, 64); err == nil {
			return intValue
		}
	}

	return defaultValue
}

// getStringParam extracts a string query parameter with a default value.
func getStringParam(query url.Values, key string, defaultValue string) string {
	if value := query.Get(key); value != "" {
		return value
	}

	return defaultValue
}
