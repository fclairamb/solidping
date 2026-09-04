package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/membench"
)

// client talks to the server under measurement. Test-mode credentials, because
// /api/mgmt/memory is super-admin gated and the test-mode user is the only
// seeded account that is a super-admin without a forced password rotation.
type client struct {
	baseURL string
	token   string
	http    *http.Client
}

const (
	benchOrg      = "test"
	benchEmail    = "test@test.com"
	benchPassword = "test"
)

func newClient(baseURL string) *client {
	return &client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 60 * time.Second},
	}
}

// waitHealthy polls /api/mgmt/health until the server answers or the deadline
// passes. Boot includes migrations and (for embedded Postgres) a database
// bootstrap, so this can legitimately take a while.
func (c *client) waitHealthy(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		resp, err := c.do(ctx, http.MethodGet, "/api/mgmt/health", nil, false)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()

			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}

	return fmt.Errorf("server did not become healthy within %s", timeout)
}

// login obtains a super-admin access token.
func (c *client) login(ctx context.Context) error {
	body, err := json.Marshal(map[string]string{"org": benchOrg, "email": benchEmail, "password": benchPassword})
	if err != nil {
		return err
	}

	resp, err := c.do(ctx, http.MethodPost, "/api/v1/auth/login", body, false)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("login returned %d: %s", resp.StatusCode, truncate(raw))
	}

	var parsed struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return err
	}

	if parsed.AccessToken == "" {
		return fmt.Errorf("login returned no access token")
	}

	c.token = parsed.AccessToken

	return nil
}

// memorySnapshot is the subset of GET /api/mgmt/memory the harness reports on.
// Declared locally rather than importing the app package: the harness must be
// able to sample an OLDER server binary (a released image) without the two
// having to agree on a Go type.
type memorySnapshot struct {
	Data struct {
		Runtime struct {
			NumGoroutine int `json:"numGoroutine"`
			Classes      struct {
				TotalBytes    float64 `json:"totalBytes"`
				HeapLiveBytes float64 `json:"heapLiveBytes"`
			} `json:"classes"`
		} `json:"runtime"`
		Process struct {
			RSSBytes float64 `json:"rssBytes"`
			Status   struct {
				Present      bool    `json:"present"`
				RssAnonBytes float64 `json:"rssAnonBytes"`
				RssFileBytes float64 `json:"rssFileBytes"`
				Threads      int     `json:"threads"`
			} `json:"status"`
			Smaps struct {
				Present  bool    `json:"present"`
				PssBytes float64 `json:"pssBytes"`
			} `json:"smaps"`
		} `json:"process"`
		Cgroup struct {
			Present            bool    `json:"present"`
			PeakBytes          float64 `json:"peakBytes"`
			UnreclaimableBytes float64 `json:"unreclaimableBytes"`
		} `json:"cgroup"`
	} `json:"data"`
}

// sample takes one reading. Absent sources contribute no value at all rather
// than a zero: a missing metric must show up as "not measured here", never as
// "measured zero".
func (c *client) sample(ctx context.Context) (membench.Sample, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/mgmt/memory", nil, true)
	if err != nil {
		return membench.Sample{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return membench.Sample{}, fmt.Errorf("memory snapshot returned %d: %s", resp.StatusCode, truncate(raw))
	}

	var snap memorySnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return membench.Sample{}, err
	}

	values := map[string]float64{
		membench.MetricGoTotal:    snap.Data.Runtime.Classes.TotalBytes,
		membench.MetricHeapLive:   snap.Data.Runtime.Classes.HeapLiveBytes,
		membench.MetricGoroutines: float64(snap.Data.Runtime.NumGoroutine),
	}

	if snap.Data.Process.Status.Present {
		values[membench.MetricRssAnon] = snap.Data.Process.Status.RssAnonBytes
		values[membench.MetricRssFile] = snap.Data.Process.Status.RssFileBytes
		values[membench.MetricThreads] = float64(snap.Data.Process.Status.Threads)
	}

	if snap.Data.Process.Smaps.Present {
		values[membench.MetricPss] = snap.Data.Process.Smaps.PssBytes
	}

	if snap.Data.Cgroup.Present {
		values[membench.MetricCgroupUnreclaimable] = snap.Data.Cgroup.UnreclaimableBytes
		values[membench.MetricCgroupPeak] = snap.Data.Cgroup.PeakBytes
	}

	return membench.Sample{At: time.Now().UTC(), Values: values}, nil
}

// get fetches a path and drains it, which is the point: the page cache and heap
// cost of *serving* it is what the docs-crawl and dash0-reload scenarios
// measure.
func (c *client) get(ctx context.Context, path string) error {
	resp, err := c.do(ctx, http.MethodGet, path, nil, false)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	_, err = io.Copy(io.Discard, resp.Body)

	return err
}

// bulkCreateChecks uses the test-mode bulk endpoint loadgen already relies on.
func (c *client) bulkCreateChecks(ctx context.Context, count int, period, targetURL string) error {
	query := fmt.Sprintf("type=http&slug=membench-{nb}&count=%d&period=%s&url=%s&org=%s",
		count, period, targetURL, benchOrg)

	resp, err := c.do(ctx, http.MethodPost, "/api/v1/test/checks/bulk?"+query, nil, true)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bulk-create returned %d: %s", resp.StatusCode, truncate(raw))
	}

	return nil
}

// docsPaths reads the docs sitemap and returns every documented URL path. Using
// the server's own sitemap (rather than a directory listing on this host) is
// what makes the crawl valid against a released image whose embedded docs
// differ from the working tree.
func (c *client) docsPaths(ctx context.Context) ([]string, error) {
	resp, err := c.do(ctx, http.MethodGet, "/docs/sitemap.xml", nil, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sitemap returned %d", resp.StatusCode)
	}

	var parsed struct {
		URLs []struct {
			Loc string `xml:"loc"`
		} `xml:"url"`
	}
	if err := xml.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}

	paths := make([]string, 0, len(parsed.URLs))

	for _, u := range parsed.URLs {
		path := u.Loc
		if idx := strings.Index(path, "//"); idx >= 0 {
			if slash := strings.Index(path[idx+2:], "/"); slash >= 0 {
				path = path[idx+2+slash:]
			}
		}

		if strings.HasPrefix(path, "/") {
			paths = append(paths, path)
		}
	}

	return paths, nil
}

// do issues one request, optionally authenticated.
func (c *client) do(ctx context.Context, method, path string, body []byte, auth bool) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, err
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if auth && c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	return c.http.Do(req)
}

// truncate keeps an error body readable in a terminal.
func truncate(raw []byte) string {
	const limit = 300
	if len(raw) > limit {
		return string(raw[:limit]) + "…"
	}

	return string(raw)
}
