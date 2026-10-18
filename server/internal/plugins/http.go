package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/plugins/protocol"
	"github.com/fclairamb/solidping/server/internal/servicesig"
)

// HTTP transport tuning (spec 2026-10-17-06).
const (
	// httpMaxBody is the largest answer read from a plugin service.
	httpMaxBody = 1024 * 1024
	// httpRefreshInterval is how often GET /checkers is called again.
	httpRefreshInterval = 60 * time.Second
	// httpDescribeTimeout bounds one GET /checkers.
	httpDescribeTimeout = 10 * time.Second
	// httpValidateTimeout bounds one POST /validate: past it the save goes
	// through on the schema alone.
	httpValidateTimeout = 2 * time.Second
	// httpSuspendAfter is how many refreshes in a row may fail before the
	// node stops advertising the plugin's types.
	httpSuspendAfter = 3
)

// HTTP plugin errors.
var (
	ErrHTTPPluginURL       = errors.New("http plugin url must be http:// to a loopback host or https://")
	ErrHTTPPluginKeys      = errors.New("http plugin signing key set is missing or invalid")
	ErrHTTPPluginVersion   = errors.New("http plugin speaks an unknown protocolVersion")
	ErrHTTPPluginStatus    = errors.New("http plugin answered with a non-200 status")
	ErrHTTPPluginBody      = errors.New("http plugin answer is invalid")
	ErrHTTPPluginTooLarge  = errors.New("http plugin answer is larger than 1 MiB")
	ErrHTTPPluginNoAnswer  = errors.New("http plugin is unreachable")
	errHTTPDescriptorTwice = errors.New("type listed twice in GET /checkers")
)

// httpTransport talks to one plugin service: describe, validate, execute,
// every request signed with the manifest's key set. It also owns the types
// the service described, and refreshes them every httpRefreshInterval.
type httpTransport struct {
	name   string
	base   *url.URL
	keys   servicesig.KeySet
	client *http.Client
	logger *slog.Logger
	plugin *Plugin

	mu sync.Mutex
	// types are the descriptors registered now, by type.
	types    map[string]protocol.TypeDescriptor
	version  string
	failures int
	closed   bool

	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

// newHTTPTransport checks an http manifest: URL scheme and host, signing key
// set. A plain http:// URL must point at loopback, since requests carry
// decrypted secrets; a non-loopback https:// URL loads with one warning.
func newHTTPTransport(manifest *Manifest, logger *slog.Logger) (*httpTransport, error) {
	base, err := url.Parse(manifest.URL)
	if err != nil || base.Host == "" {
		return nil, fmt.Errorf("%w: %q", ErrHTTPPluginURL, manifest.URL)
	}

	loopback := isLoopbackHost(base.Hostname())

	switch {
	case base.Scheme == "http" && loopback, base.Scheme == "https":
	default:
		return nil, fmt.Errorf("%w: %q", ErrHTTPPluginURL, manifest.URL)
	}

	if manifest.SecretEnv == "" {
		return nil, fmt.Errorf("%w: secretEnv is required", ErrHTTPPluginKeys)
	}

	keys, err := servicesig.ParseKeySet(os.Getenv(manifest.SecretEnv))
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrHTTPPluginKeys, manifest.SecretEnv, err)
	}

	if keys.Empty() {
		return nil, fmt.Errorf("%w: %s is unset or empty", ErrHTTPPluginKeys, manifest.SecretEnv)
	}

	if !loopback {
		logger.Warn("HTTP check plugin is not on loopback: its checks run from the service's host, "+
			"not from this worker's region", "plugin", manifest.Name, "url", manifest.URL)
	}

	base.Path = strings.TrimSuffix(base.Path, "/")

	return &httpTransport{
		name:   manifest.Name,
		base:   base,
		keys:   keys,
		client: newPluginHTTPClient(),
		logger: logger,
		types:  map[string]protocol.TypeDescriptor{},
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}, nil
}

// newPluginHTTPClient is one keep-alive client per plugin: TLS verification
// on, no proxy from the environment, and no redirects (a redirect could send
// the secrets elsewhere, so a 3xx is answered as is and maps to an error).
// It does not go through the egress guard: the URL is operator
// configuration, usually loopback, which the guard refuses in SaaS mode.
func newPluginHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy:               nil,
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 5 * time.Second,
		ForceAttemptHTTP2:   true,
	}

	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}

	ip := net.ParseIP(host)

	return ip != nil && ip.IsLoopback()
}

// loadHTTPPlugin loads an http plugin: describe once now, then refresh in
// the background. A service that does not answer yet is kept (a sidecar may
// start after the node) and registers its types on the first good refresh;
// one that speaks an unknown protocol version is refused.
func (s *Set) loadHTTPPlugin(manifest *Manifest, dir string, logger *slog.Logger) error {
	runner, err := newHTTPTransport(manifest, logger)
	if err != nil {
		return err
	}

	plugin := &Plugin{Manifest: *manifest, Dir: dir, transport: runner}
	runner.plugin = plugin

	ctx, cancel := context.WithTimeout(context.Background(), httpDescribeTimeout)
	desc, err := runner.describe(ctx)

	cancel()

	switch {
	case errors.Is(err, ErrHTTPPluginVersion):
		return err
	case err != nil:
		logger.Warn("HTTP check plugin did not describe its types, retrying in the background",
			"plugin", manifest.Name, "error", err)

		runner.mu.Lock()
		runner.failures = 1
		runner.mu.Unlock()
	default:
		for _, skipped := range runner.apply(desc) {
			s.fail(logger, fmt.Errorf("plugin %s: %w", manifest.Name, skipped))
		}
	}

	s.Types = append(s.Types, runner.currentTypes()...)
	s.Plugins = append(s.Plugins, plugin)

	go runner.refreshLoop()

	return nil
}

// currentTypes returns the types registered now.
func (t *httpTransport) currentTypes() []checkerdef.CheckType {
	t.mu.Lock()
	defer t.mu.Unlock()

	out := make([]checkerdef.CheckType, 0, len(t.types))
	for checkType := range t.types {
		out = append(out, checkerdef.CheckType(checkType))
	}

	return out
}

func (t *httpTransport) refreshLoop() {
	defer close(t.done)

	ticker := time.NewTicker(httpRefreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-t.stop:
			return
		case <-ticker.C:
			t.refresh()
		}
	}
}

// refresh calls GET /checkers once and applies the answer. On failure the
// last good descriptors stay registered; after httpSuspendAfter failures in
// a row they stop being advertised, until the next success.
func (t *httpTransport) refresh() {
	ctx, cancel := context.WithTimeout(context.Background(), httpDescribeTimeout)
	defer cancel()

	desc, err := t.describe(ctx)
	if err == nil {
		for _, skipped := range t.apply(desc) {
			t.logger.Error("Skipping check plugin type", "plugin", t.name, "error", skipped)
		}

		return
	}

	t.mu.Lock()
	t.failures++
	failures := t.failures
	t.mu.Unlock()

	t.logger.Warn("HTTP check plugin refresh failed", "plugin", t.name, "failures", failures, "error", err)

	if failures == httpSuspendAfter {
		t.logger.Error("HTTP check plugin is down, its types are no longer advertised",
			"plugin", t.name, "types", t.currentTypes())
	}

	if failures >= httpSuspendAfter {
		for _, checkType := range t.currentTypes() {
			setSuspended(checkType, true)
		}
	}
}

// apply registers the described types: new ones are added, changed ones are
// re-registered, missing ones are unregistered (their checks wait for a
// capable worker, they are never deleted). Returns the skipped descriptors.
func (t *httpTransport) apply(desc *protocol.DescribeResponse) []error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return nil
	}

	t.failures = 0

	manifest := t.plugin.Manifest
	manifest.Version = desc.Version

	var skipped []error

	seen := map[string]bool{}

	for i := range desc.Data {
		descriptor := desc.Data[i]

		if seen[descriptor.Type] {
			skipped = append(skipped, fmt.Errorf("%w: %s", errHTTPDescriptorTwice, descriptor.Type))

			continue
		}

		seen[descriptor.Type] = true

		// A known type whose new descriptor is invalid keeps its last good
		// registration: it is still in t.types.
		if err := t.applyOne(&manifest, &descriptor, desc.Version); err != nil {
			skipped = append(skipped, fmt.Errorf("type %s: %w", descriptor.Type, err))
		}
	}

	for checkType := range t.types {
		if !seen[checkType] {
			unregisterType(checkerdef.CheckType(checkType))
			delete(t.types, checkType)
			t.logger.Info("HTTP check plugin no longer serves a type", "plugin", t.name, "type", checkType)
		}
	}

	t.version = desc.Version

	for checkType := range t.types {
		setSuspended(checkerdef.CheckType(checkType), false)
	}

	return skipped
}

// applyOne registers one descriptor, or re-registers it when it changed.
// Caller holds t.mu.
func (t *httpTransport) applyOne(manifest *Manifest, descriptor *protocol.TypeDescriptor, version string) error {
	previous, known := t.types[descriptor.Type]
	if known && t.version == version && reflect.DeepEqual(previous, *descriptor) {
		return nil
	}

	if !typeNameRE.MatchString(descriptor.Type) {
		return ErrTypePrefix
	}

	if len(descriptor.Schema) == 0 {
		return fmt.Errorf("%w: schema is required", ErrInvalidManifest)
	}

	decl := &TypeDecl{
		Type:          descriptor.Type,
		Description:   descriptor.Description,
		Labels:        descriptor.Labels,
		MinPeriod:     descriptor.MinPeriod,
		DefaultPeriod: descriptor.DefaultPeriod,
		MaxPeriod:     descriptor.MaxPeriod,
		Validate:      descriptor.Validate,
	}

	meta, err := decl.meta(manifest)
	if err != nil {
		return err
	}

	// Compile before touching the registration, so a broken update keeps
	// the last good one.
	if _, err := compileSchema(decl.Type, descriptor.Schema, manifest); err != nil {
		return err
	}

	if known {
		unregisterType(meta.Type)
		delete(t.types, descriptor.Type)
	}

	if err := registerCompiled(t.plugin, manifest, decl, &meta, descriptor.Schema); err != nil {
		return err
	}

	t.types[descriptor.Type] = *descriptor

	return nil
}

// describe calls GET /checkers.
func (t *httpTransport) describe(ctx context.Context) (*protocol.DescribeResponse, error) {
	var desc protocol.DescribeResponse

	if err := t.do(ctx, http.MethodGet, protocol.PathCheckers, nil, &desc); err != nil {
		return nil, err
	}

	if desc.ProtocolVersion != protocol.ProtocolVersion {
		return nil, fmt.Errorf("%w: %d", ErrHTTPPluginVersion, desc.ProtocolVersion)
	}

	if desc.Version == "" {
		return nil, fmt.Errorf("%w: version is required", ErrHTTPPluginBody)
	}

	return &desc, nil
}

// do sends one signed request and decodes a 200 answer into out.
func (t *httpTransport) do(ctx context.Context, method, path string, payload, out any) error {
	var body []byte

	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encoding plugin request: %w", err)
		}

		body = encoded
	}

	target := *t.base
	target.Path += path

	req, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building plugin request: %w", err)
	}

	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if err = servicesig.SignRequest(req, t.keys, body, time.Now()); err != nil {
		return fmt.Errorf("signing plugin request: %w", err)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr //nolint:wrapcheck // the caller tests for DeadlineExceeded
		}

		return fmt.Errorf("%w: %w", ErrHTTPPluginNoAnswer, err)
	}

	// Closing without draining drops the connection: an oversized answer is
	// never read past the cap.
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: %d", ErrHTTPPluginStatus, resp.StatusCode)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, httpMaxBody+1))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr //nolint:wrapcheck // the caller tests for DeadlineExceeded
		}

		return fmt.Errorf("%w: %w", ErrHTTPPluginNoAnswer, err)
	}

	if len(raw) > httpMaxBody {
		return ErrHTTPPluginTooLarge
	}

	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%w: %w", ErrHTTPPluginBody, err)
	}

	return nil
}

func (t *httpTransport) execute(ctx context.Context, checkType string, cfg *Config) (*checkerdef.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout())
	defer cancel()

	deadline, _ := ctx.Deadline()
	start := time.Now()

	var res protocol.ExecuteResponse

	err := t.do(ctx, http.MethodPost, protocol.PathExecute, protocol.ExecuteRequest{
		Type: checkType, Config: cfg.GetConfig(), Deadline: deadline,
	}, &res)

	duration := time.Since(start)

	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return &checkerdef.Result{
			Status: checkerdef.StatusTimeout, Duration: duration,
			Output: map[string]any{checkerdef.OutputKeyError: "plugin " + t.name + " did not answer before the deadline"},
		}, nil
	case err != nil:
		// Never down: a broken plugin is not a down target. The full error
		// names the plugin's internal URL: it goes to the worker log only.
		t.logger.WarnContext(ctx, "HTTP check plugin execute failed", "plugin", t.name, "error", err)

		message := "plugin " + t.name + " failed: " + storedReason(err)

		return &checkerdef.Result{
			Status: checkerdef.StatusError, Duration: duration,
			Output: map[string]any{"message": message, checkerdef.OutputKeyError: message},
		}, nil
	}

	if _, known := checkerdef.ParseStatus(res.Status); !known {
		if res.Output == nil {
			res.Output = map[string]any{}
		}

		res.Output["message"] = fmt.Sprintf("plugin %s returned an unknown status %q", t.name, res.Status)
	}

	return resultFromProtocol(&res, duration), nil
}

// storedReason is the part of an execute error safe to store in a result
// that org users read: never the plugin's URL or address.
func storedReason(err error) string {
	switch {
	case errors.Is(err, ErrHTTPPluginStatus), errors.Is(err, ErrHTTPPluginBody), errors.Is(err, ErrHTTPPluginTooLarge):
		return err.Error()
	case errors.Is(err, ErrHTTPPluginNoAnswer):
		return ErrHTTPPluginNoAnswer.Error()
	default:
		return "the request could not be sent"
	}
}

// validate asks the service to validate a config the schema accepted. A
// service that is down, answers non-200 or takes over 2 s is logged and the
// save goes through on the schema alone.
func (t *httpTransport) validate(ctx context.Context, checkType string, config map[string]any) error {
	ctx, cancel := context.WithTimeout(ctx, httpValidateTimeout)
	defer cancel()

	var res protocol.ValidateResponse

	req := protocol.ValidateRequest{Type: checkType, Config: config}

	if err := t.do(ctx, http.MethodPost, protocol.PathValidate, req, &res); err != nil {
		t.logger.WarnContext(ctx, "HTTP check plugin could not validate a config, accepting it on the schema alone",
			"plugin", t.name, "type", checkType, "error", err)

		return nil
	}

	if len(res.Errors) == 0 {
		return nil
	}

	messages := make([]string, 0, len(res.Errors))
	for i := range res.Errors {
		messages = append(messages, res.Errors[i].Field+": "+res.Errors[i].Message)
	}

	return checkerdef.NewConfigError(res.Errors[0].Field, strings.Join(messages, "; "))
}

// close stops the refresh loop and unregisters the plugin's types.
func (t *httpTransport) close() {
	t.stopOnce.Do(func() { close(t.stop) })
	<-t.done

	t.mu.Lock()
	defer t.mu.Unlock()

	t.closed = true

	for checkType := range t.types {
		unregisterType(checkerdef.CheckType(checkType))
	}

	t.types = map[string]protocol.TypeDescriptor{}
	t.client.CloseIdleConnections()
}

//nolint:gochecknoglobals // process-wide, written by the http transport only
var (
	suspendedMu sync.RWMutex
	suspended   = map[checkerdef.CheckType]bool{}
)

func setSuspended(checkType checkerdef.CheckType, value bool) {
	suspendedMu.Lock()
	defer suspendedMu.Unlock()

	if value {
		suspended[checkType] = true
	} else {
		delete(suspended, checkType)
	}
}

// Advertised filters loaded plugin types down to the ones this node should
// advertise: an http plugin whose service failed httpSuspendAfter refreshes
// in a row keeps its types registered but stops advertising them, so jobs go
// to nodes whose service is up.
func Advertised(types []string) []string {
	suspendedMu.RLock()
	defer suspendedMu.RUnlock()

	if len(suspended) == 0 {
		return types
	}

	out := make([]string, 0, len(types))

	for _, checkType := range types {
		if !suspended[checkerdef.CheckType(checkType)] {
			out = append(out, checkType)
		}
	}

	return out
}
