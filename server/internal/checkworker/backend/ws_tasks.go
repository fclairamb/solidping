package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkworker/tasks"
	"github.com/fclairamb/solidping/server/internal/workertasks"
)

// tasksPath is the prefix of the agent worker-task routes. Signed with the
// same challenge as the attachment upload (method|path|ts|nonce).
const tasksPath = "/api/v1/agent/tasks"

// taskRequestTimeout bounds one worker-task call.
const taskRequestTimeout = 30 * time.Second

// maxTaskResponseBytes bounds a response read: a claim carries up to 1 MiB of
// state plus the kind's input.
const maxTaskResponseBytes = 4 * 1024 * 1024

// ErrTaskRequestRejected is a worker-task call the server refused for a reason
// other than 404, 410 or 413.
var ErrTaskRequestRejected = errors.New("worker task request rejected")

// HTTPTaskClient is the agent's worker-task client: signed POSTs to the
// server, never the WebSocket.
type HTTPTaskClient struct {
	backend *WSBackend
}

// TaskClient returns the agent's worker-task client.
func (b *WSBackend) TaskClient() *HTTPTaskClient {
	return &HTTPTaskClient{backend: b}
}

// Claim implements tasks.Client: nil on 204, tasks.ErrNotSupported on 404.
func (c *HTTPTaskClient) Claim(ctx context.Context, kinds []string) (*workertasks.Claim, error) {
	body, err := json.Marshal(workertasks.ClaimRequest{Kinds: kinds})
	if err != nil {
		return nil, fmt.Errorf("encode claim: %w", err)
	}

	status, resp, err := c.post(ctx, tasksPath+"/claim", body)
	if err != nil {
		return nil, err
	}

	if status == http.StatusNoContent {
		return nil, nil //nolint:nilnil // no task
	}

	var claim workertasks.Claim
	if err := json.Unmarshal(resp, &claim); err != nil {
		return nil, fmt.Errorf("decode claim: %w", err)
	}

	return &claim, nil
}

// Heartbeat implements tasks.Client.
func (c *HTTPTaskClient) Heartbeat(ctx context.Context, uid string) error {
	_, _, err := c.post(ctx, tasksPath+"/"+uid+"/heartbeat", nil)

	return err
}

// Step implements tasks.Client.
func (c *HTTPTaskClient) Step(ctx context.Context, uid string, req workertasks.StepRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("encode step: %w", err)
	}

	_, _, err = c.post(ctx, tasksPath+"/"+uid+"/step", body)

	return err
}

// Finish implements tasks.Client.
func (c *HTTPTaskClient) Finish(ctx context.Context, uid string, body json.RawMessage) error {
	_, _, err := c.post(ctx, tasksPath+"/"+uid+"/finish", body)

	return err
}

// Fail implements tasks.Client.
func (c *HTTPTaskClient) Fail(ctx context.Context, uid, errorCode string) error {
	body, err := json.Marshal(workertasks.FailRequest{ErrorCode: errorCode})
	if err != nil {
		return fmt.Errorf("encode fail: %w", err)
	}

	_, _, err = c.post(ctx, tasksPath+"/"+uid+"/fail", body)

	return err
}

// post sends one signed POST and maps the status: 404 is ErrNotSupported (an
// old server), 410 ErrTaskGone, 413 ErrTooLarge.
func (c *HTTPTaskClient) post(ctx context.Context, path string, body []byte) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, taskRequestTimeout)
	defer cancel()

	req, err := c.signedRequest(ctx, path, body)
	if err != nil {
		return 0, nil, err
	}

	resp, err := c.backend.httpClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("post worker task request: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxTaskResponseBytes))
	if err != nil {
		return 0, nil, fmt.Errorf("read worker task response: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent:
		return resp.StatusCode, respBody, nil
	case http.StatusNotFound:
		return resp.StatusCode, nil, tasks.ErrNotSupported
	case http.StatusGone:
		return resp.StatusCode, nil, workertasks.ErrTaskGone
	case http.StatusRequestEntityTooLarge:
		return resp.StatusCode, nil, workertasks.ErrTooLarge
	default:
		detail := strings.TrimSpace(string(respBody[:min(len(respBody), 512)]))

		return resp.StatusCode, nil, fmt.Errorf("%w: status %d: %s", ErrTaskRequestRejected, resp.StatusCode, detail)
	}
}

// signedRequest builds a POST carrying the agent's signed headers.
func (c *HTTPTaskClient) signedRequest(ctx context.Context, path string, body []byte) (*http.Request, error) {
	wsBackend := c.backend

	wsBackend.mu.Lock()
	identity := *wsBackend.identity
	wsBackend.mu.Unlock()

	if identity.AgentUID == "" {
		return nil, ErrNotEnrolled
	}

	timestamp := time.Now().UTC().Format(time.RFC3339)

	nonce, err := randomID()
	if err != nil {
		return nil, err
	}

	signature, err := identity.Sign(http.MethodPost, path, timestamp, nonce)
	if err != nil {
		return nil, fmt.Errorf("sign worker task request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, httpBaseURL(wsBackend.serverURL)+path,
		bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build worker task request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Sp-Agent-Uid", identity.AgentUID)
	req.Header.Set("X-Sp-Timestamp", timestamp)
	req.Header.Set("X-Sp-Nonce", nonce)
	req.Header.Set("X-Sp-Signature", signature)

	return req, nil
}
