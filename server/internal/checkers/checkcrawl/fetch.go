package checkcrawl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

const (
	schemeHTTP  = "http"
	schemeHTTPS = "https"

	// maxRedirects is how many redirects a fetch follows (spec §2.2).
	maxRedirects = 5
	// maxBodyBytes caps how much of an internal HTML page is read.
	maxBodyBytes = 5 * 1024 * 1024
	// statusBroken is the first status code that counts as broken.
	statusBroken = 400
)

var errRedirectLoop = errors.New("redirect loop")

// fetchResult is the outcome of one fetch, redirects followed.
type fetchResult struct {
	status      int
	finalURL    *url.URL
	contentType string
	body        []byte
	errText     string
	// cut means the slice deadline (not the request's own timeout) aborted
	// the fetch: the item goes back on the queue instead of being reported.
	cut bool
}

func (f *fetchResult) broken() bool {
	return f.errText != "" || f.status >= statusBroken
}

func (f *fetchResult) isHTML() bool {
	mediaType, _, err := mime.ParseMediaType(f.contentType)

	return err == nil && mediaType == "text/html"
}

// fetch requests target with method, following up to maxRedirects
// redirects by hand so a loop is detected and reported.
func (r *runner) fetch(sliceCtx context.Context, method, target string, readBody bool) fetchResult {
	current, err := url.Parse(target)
	if err != nil {
		return fetchResult{errText: "invalid URL"}
	}

	visited := map[string]bool{}

	for range maxRedirects + 1 {
		if visited[current.String()] {
			return fetchResult{errText: errRedirectLoop.Error()}
		}

		visited[current.String()] = true

		res, next := r.fetchOnce(sliceCtx, method, current, readBody)
		if next == nil {
			return res
		}

		current = next
	}

	return fetchResult{errText: fmt.Sprintf("more than %d redirects", maxRedirects)}
}

// fetchOnce issues one request. A redirect returns the next URL.
func (r *runner) fetchOnce(
	sliceCtx context.Context, method string, target *url.URL, readBody bool,
) (fetchResult, *url.URL) {
	reqCtx, cancel := context.WithTimeout(sliceCtx, r.cfg.RequestTimeout())
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, method, target.String(), nil)
	if err != nil {
		return fetchResult{errText: err.Error()}, nil
	}

	req.Header.Set("User-Agent", userAgent())
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	resp, err := r.client.Do(req)
	if err != nil {
		return fetchResult{errText: networkError(err), cut: sliceCtx.Err() != nil}, nil
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		if location := resp.Header.Get("Location"); location != "" {
			if next, parseErr := target.Parse(location); parseErr == nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBodyBytes))

				return fetchResult{}, next
			}
		}
	}

	res := fetchResult{status: resp.StatusCode, finalURL: target, contentType: resp.Header.Get("Content-Type")}

	if readBody && res.status < statusBroken && res.isHTML() {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		if readErr != nil {
			return fetchResult{errText: networkError(readErr), cut: sliceCtx.Err() != nil}, nil
		}

		res.body = body
	}

	return res, nil
}

// networkError trims a transport error down to its useful tail.
func networkError(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}

	text := err.Error()
	if errors.Is(err, context.DeadlineExceeded) {
		text = "timeout"
	}

	return strings.TrimSpace(text)
}
