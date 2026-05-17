package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// apiClient is a minimal HTTP client that adds a Bearer token to every request.
type apiClient struct {
	baseURL string
	token   string
	http    *http.Client
}

// newAPIClient creates a new API client.
func newAPIClient(baseURL, token string) *apiClient {
	return &apiClient{
		baseURL: baseURL,
		token:   token,
		http:    &http.Client{},
	}
}

// do performs an authenticated HTTP request and decodes the JSON response.
func (c *apiClient) do(ctx context.Context, method, path string, body any, out any) (int, error) {
	var bodyReader io.Reader

	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, fmt.Errorf("marshal body: %w", err)
		}

		bodyReader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		var apiErr struct {
			Title  string `json:"title"`
			Detail string `json:"detail"`
		}
		if jsonErr := json.Unmarshal(respBody, &apiErr); jsonErr == nil && apiErr.Title != "" {
			return resp.StatusCode, fmt.Errorf("API error %d: %s — %s", resp.StatusCode, apiErr.Title, apiErr.Detail)
		}

		return resp.StatusCode, fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode response: %w", err)
		}
	}

	return resp.StatusCode, nil
}

// HeartbeatCheckResult holds the created check's UID, slug, and heartbeat token.
type HeartbeatCheckResult struct {
	UID   string
	Slug  string
	Token string
}

// CreateHeartbeatCheck creates a heartbeat check in the given org.
func CreateHeartbeatCheck(
	ctx context.Context,
	client *apiClient,
	org, slug string,
	confirmationPeriodSeconds, recoveryPeriodSeconds, escalationThreshold int,
) (*HeartbeatCheckResult, error) {
	var out map[string]any

	body := map[string]any{
		"name":                      slug,
		"slug":                      slug,
		"type":                      "heartbeat",
		"config":                    map[string]any{},
		"confirmationPeriodSeconds": confirmationPeriodSeconds,
		"recoveryPeriodSeconds":     recoveryPeriodSeconds,
	}

	if escalationThreshold > 0 {
		body["escalationThreshold"] = escalationThreshold
	}

	_, err := client.do(ctx, http.MethodPost,
		fmt.Sprintf("/api/v1/orgs/%s/checks", org),
		body, &out)
	if err != nil {
		return nil, fmt.Errorf("create heartbeat check: %w", err)
	}

	uid, _ := out["uid"].(string)
	checkSlug, _ := out["slug"].(string)

	var token string
	if cfg, ok := out["config"].(map[string]any); ok {
		token, _ = cfg["token"].(string)
	}

	if uid == "" {
		return nil, fmt.Errorf("create heartbeat check: no uid in response")
	}

	return &HeartbeatCheckResult{
		UID:   uid,
		Slug:  checkSlug,
		Token: token,
	}, nil
}

// ChannelResult holds the created channel's UID.
type ChannelResult struct {
	UID string
}

// CreateWebhookChannel creates a webhook notification channel in the given org.
func CreateWebhookChannel(ctx context.Context, client *apiClient, org, name, webhookURL string) (*ChannelResult, error) {
	var out map[string]any

	_, err := client.do(ctx, http.MethodPost,
		fmt.Sprintf("/api/v1/orgs/%s/channels", org),
		map[string]any{
			"name": name,
			"type": "webhook",
			"settings": map[string]any{
				"url": webhookURL,
			},
		}, &out)
	if err != nil {
		return nil, fmt.Errorf("create webhook channel: %w", err)
	}

	uid, _ := out["uid"].(string)
	if uid == "" {
		return nil, fmt.Errorf("create webhook channel: no uid in response")
	}

	return &ChannelResult{UID: uid}, nil
}

// EscalationPolicyStepTarget describes one target in an escalation step.
type EscalationPolicyStepTarget struct {
	Type      string // "connection", "user", "schedule", "all_admins"
	TargetUID string // Required for connection, user, schedule targets
}

// EscalationPolicyStep describes one step in an escalation policy.
type EscalationPolicyStep struct {
	DelaySeconds int
	Targets      []EscalationPolicyStepTarget
}

// EscalationPolicyResult holds the created policy's UID and slug.
type EscalationPolicyResult struct {
	UID  string
	Slug string
}

// CreateEscalationPolicy creates an escalation policy in the given org.
func CreateEscalationPolicy(
	ctx context.Context,
	client *apiClient,
	org, slug string,
	steps []EscalationPolicyStep,
	repeatMax int,
	repeatAfterSeconds *int,
) (*EscalationPolicyResult, error) {
	stepsPayload := make([]any, 0, len(steps))
	for _, step := range steps {
		targets := make([]any, 0, len(step.Targets))
		for _, tgt := range step.Targets {
			tgtMap := map[string]any{"type": tgt.Type}
			if tgt.TargetUID != "" {
				tgtMap["targetUid"] = tgt.TargetUID
			}

			targets = append(targets, tgtMap)
		}

		stepsPayload = append(stepsPayload, map[string]any{
			"delaySeconds": step.DelaySeconds,
			"targets":      targets,
		})
	}

	reqBody := map[string]any{
		"slug":      slug,
		"name":      "Scenario Policy " + slug,
		"repeatMax": repeatMax,
		"steps":     stepsPayload,
	}

	if repeatAfterSeconds != nil {
		reqBody["repeatAfterSeconds"] = *repeatAfterSeconds
	}

	var out map[string]any

	_, err := client.do(ctx, http.MethodPost,
		fmt.Sprintf("/api/v1/orgs/%s/escalation-policies", org),
		reqBody, &out)
	if err != nil {
		return nil, fmt.Errorf("create escalation policy: %w", err)
	}

	uid, _ := out["uid"].(string)
	outSlug, _ := out["slug"].(string)

	if uid == "" {
		return nil, fmt.Errorf("create escalation policy: no uid in response")
	}

	return &EscalationPolicyResult{UID: uid, Slug: outSlug}, nil
}

// AssignPolicyToCheck assigns an escalation policy UID to a check by slug.
func AssignPolicyToCheck(ctx context.Context, client *apiClient, org, checkSlug, policyUID string) error {
	_, err := client.do(ctx, http.MethodPatch,
		fmt.Sprintf("/api/v1/orgs/%s/checks/%s", org, checkSlug),
		map[string]any{
			"escalationPolicyUid": policyUID,
		}, nil)
	if err != nil {
		return fmt.Errorf("assign policy to check: %w", err)
	}

	return nil
}

// SendHeartbeat sends a heartbeat ping to the server with the given status.
func SendHeartbeat(ctx context.Context, client *apiClient, org, checkSlug, token, status string) error {
	url := fmt.Sprintf("/api/v1/heartbeat/%s/%s?token=%s&status=%s", org, checkSlug, token, status)
	_, err := client.do(ctx, http.MethodPost, url, nil, nil)

	if err != nil {
		return fmt.Errorf("send heartbeat: %w", err)
	}

	return nil
}

// DeleteCheck deletes a check by UID or slug.
func DeleteCheck(ctx context.Context, client *apiClient, org, checkUID string) error {
	_, err := client.do(ctx, http.MethodDelete,
		fmt.Sprintf("/api/v1/orgs/%s/checks/%s", org, checkUID),
		nil, nil)
	if err != nil {
		return fmt.Errorf("delete check %s: %w", checkUID, err)
	}

	return nil
}

// DeleteChannel deletes a channel by UID.
func DeleteChannel(ctx context.Context, client *apiClient, org, channelUID string) error {
	_, err := client.do(ctx, http.MethodDelete,
		fmt.Sprintf("/api/v1/orgs/%s/channels/%s", org, channelUID),
		nil, nil)
	if err != nil {
		return fmt.Errorf("delete channel %s: %w", channelUID, err)
	}

	return nil
}

// DeletePolicy deletes an escalation policy by slug.
func DeletePolicy(ctx context.Context, client *apiClient, org, policySlug string) error {
	_, err := client.do(ctx, http.MethodDelete,
		fmt.Sprintf("/api/v1/orgs/%s/escalation-policies/%s", org, policySlug),
		nil, nil)
	if err != nil {
		return fmt.Errorf("delete policy %s: %w", policySlug, err)
	}

	return nil
}
