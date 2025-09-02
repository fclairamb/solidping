package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Errors returned by HTTPBackend.
var (
	ErrHTTPRequest = errors.New("HTTP request failed")
	ErrHTTPStatus  = errors.New("unexpected HTTP status")
)

// HTTPBackend implements WorkerBackend by calling the master server
// API over HTTP.  Used by edge workers that run outside the main
// process.
type HTTPBackend struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// NewHTTPBackend creates a new HTTPBackend.
func NewHTTPBackend(baseURL, token string) *HTTPBackend {
	return &HTTPBackend{
		baseURL: baseURL,
		token:   token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// registerRequest is the JSON body for POST /workers/register.
type registerRequest struct {
	UID    string  `json:"uid"`
	Slug   string  `json:"slug"`
	Name   string  `json:"name"`
	Region *string `json:"region,omitempty"`
}

// registerResponse is the JSON response from POST /workers/register.
type registerResponse struct {
	UID    string  `json:"uid"`
	Slug   string  `json:"slug"`
	Name   string  `json:"name"`
	Region *string `json:"region,omitempty"`
	Token  *string `json:"token,omitempty"`
}

// Register calls POST /api/v1/workers/register.
func (b *HTTPBackend) Register(
	ctx context.Context, worker *models.Worker,
) (*models.Worker, error) {
	body := registerRequest{
		UID:    worker.UID,
		Slug:   worker.Slug,
		Name:   worker.Name,
		Region: worker.Region,
	}

	var resp registerResponse
	if err := b.post(ctx, "/workers/register", body, &resp); err != nil {
		return nil, err
	}

	now := time.Now()

	return &models.Worker{
		UID:          resp.UID,
		Slug:         resp.Slug,
		Name:         resp.Name,
		Region:       resp.Region,
		Token:        resp.Token,
		LastActiveAt: &now,
	}, nil
}

// heartbeatRequest is the JSON body for POST /workers/heartbeat.
type heartbeatRequest struct {
	WorkerUID string `json:"workerUid"`
}

// Heartbeat calls POST /api/v1/workers/heartbeat.
func (b *HTTPBackend) Heartbeat(
	ctx context.Context, workerUID string,
) error {
	body := heartbeatRequest{WorkerUID: workerUID}

	return b.post(ctx, "/workers/heartbeat", body, nil)
}

// claimJobsRequest is the JSON body for POST /workers/claim-jobs.
type claimJobsRequest struct {
	WorkerUID string  `json:"workerUid"`
	Region    *string `json:"region,omitempty"`
	Limit     int     `json:"limit"`
	MaxAhead  string  `json:"maxAhead"`
}

// claimJobsResponse is the JSON response from POST /workers/claim-jobs.
type claimJobsResponse struct {
	Jobs []*models.CheckJob `json:"jobs"`
}

// ClaimJobs calls POST /api/v1/workers/claim-jobs.
func (b *HTTPBackend) ClaimJobs(
	ctx context.Context,
	workerUID string,
	region *string,
	limit int,
	maxAhead time.Duration,
) ([]*models.CheckJob, error) {
	body := claimJobsRequest{
		WorkerUID: workerUID,
		Region:    region,
		Limit:     limit,
		MaxAhead:  maxAhead.String(),
	}

	var resp claimJobsResponse
	if err := b.post(
		ctx, "/workers/claim-jobs", body, &resp,
	); err != nil {
		return nil, err
	}

	return resp.Jobs, nil
}

// submitResultRequest is the JSON body for POST /workers/submit-result.
type submitResultRequest struct {
	JobUID    string         `json:"jobUid"`
	WorkerUID string         `json:"workerUid"`
	Status    int            `json:"status"`
	Duration  float32        `json:"duration"`
	Metrics   map[string]any `json:"metrics,omitempty"`
	Output    map[string]any `json:"output,omitempty"`
}

// SubmitResult calls POST /api/v1/workers/submit-result.
func (b *HTTPBackend) SubmitResult(
	ctx context.Context,
	jobUID, workerUID string,
	req *SubmitResultRequest,
) (*SubmitResultResponse, error) {
	body := submitResultRequest{
		JobUID:    jobUID,
		WorkerUID: workerUID,
		Status:    req.Status,
		Duration:  req.Duration,
		Metrics:   req.Metrics,
		Output:    req.Output,
	}

	var resp SubmitResultResponse
	if err := b.post(
		ctx, "/workers/submit-result", body, &resp,
	); err != nil {
		return nil, err
	}

	return &resp, nil
}

// post sends a JSON POST request and decodes the response.
func (b *HTTPBackend) post(
	ctx context.Context,
	path string,
	reqBody any,
	respBody any,
) error {
	data, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	url := b.baseURL + "/api/v1" + path

	httpReq, err := http.NewRequestWithContext(
		ctx, http.MethodPost, url, bytes.NewReader(data),
	)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrHTTPRequest, err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+b.token)

	httpResp, err := b.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrHTTPRequest, err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(httpResp.Body)

		return fmt.Errorf(
			"%w: %d %s", ErrHTTPStatus, httpResp.StatusCode, body,
		)
	}

	if respBody != nil {
		if err := json.NewDecoder(httpResp.Body).Decode(respBody); err != nil {
			return fmt.Errorf("failed to decode response: %w", err)
		}
	}

	return nil
}
