// Package agentregistry is a minimal, read-only OCI registry served at the
// host root (spec 2026-11-22-04). It exposes one repository, `solidping`, so a
// private-location agent can run `<server-host>/solidping:current` and follow
// the server's own version on every restart.
//
// It stores nothing. A manifest is fetched from the upstream registry (ghcr)
// with an anonymous pull token and returned byte for byte, with the upstream
// Content-Type and Docker-Content-Digest; a blob request is answered with a
// 307 to the signed URL the upstream redirects to, so layers never transit the
// server. The images are public: there is no auth.
package agentregistry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PathPrefix is the registry API root. Registries live at the host root:
// clients derive `/v2/` from the image reference and accept no path prefix.
const PathPrefix = "/v2"

// RepositoryName is the one repository this registry serves.
const RepositoryName = "solidping"

// CurrentTag resolves to the server's own version.
const CurrentTag = "current"

const (
	// DefaultUpstream is the registry the images are published to.
	DefaultUpstream = "https://ghcr.io"
	// DefaultUpstreamRepository is the image's path on the upstream.
	DefaultUpstreamRepository = "fclairamb/solidping"
	// DefaultTagTTL is how long a tag's resolution to a digest is cached.
	// Short on purpose: `current` must follow a server upgrade or rollback.
	DefaultTagTTL = 5 * time.Minute
	// tokenTTL bounds how long an anonymous pull token is reused.
	tokenTTL = 4 * time.Minute
	// upstreamTimeout bounds one upstream round trip.
	upstreamTimeout = 20 * time.Second
	// maxManifestBytes caps a manifest read from the upstream.
	maxManifestBytes = 4 << 20
	// maxCachedManifests bounds the by-digest cache.
	maxCachedManifests = 64
	// maxCachedTags bounds the tag cache.
	maxCachedTags = 64
)

// manifestAccept is sent upstream on every manifest request, whatever the
// client asked for: the answer is then the same for every client and can be
// cached by digest. Multi-arch images are an index, which every current client
// (Docker, containerd, CRI-O) understands.
const manifestAccept = "application/vnd.oci.image.index.v1+json, " +
	"application/vnd.docker.distribution.manifest.list.v2+json, " +
	"application/vnd.oci.image.manifest.v1+json, " +
	"application/vnd.docker.distribution.manifest.v2+json"

var (
	digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	// tagPattern accepts a release version tag (what the CI publishes, without
	// the leading "v"). Anything else is refused before any upstream call, so
	// a scanner cannot turn this endpoint into a fan-out of upstream requests.
	tagPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+(\.[0-9]+)?(-[0-9A-Za-z.-]+)?$`)
)

// Errors reported to the caller as a 502.
var (
	errUpstreamStatus   = errors.New("unexpected upstream status")
	errUpstreamDigest   = errors.New("upstream manifest digest mismatch")
	errUpstreamNoType   = errors.New("upstream manifest has no content type")
	errUpstreamTooLarge = errors.New("upstream manifest too large")
	errUpstreamNoToken  = errors.New("upstream returned no token")
	errUpstreamRedirect = errors.New("upstream did not redirect the blob")
	errNotFound         = errors.New("not found upstream")
)

// Config configures a Registry.
type Config struct {
	// Upstream is the upstream registry base URL (default DefaultUpstream).
	Upstream string
	// UpstreamRepository is the image path on the upstream.
	UpstreamRepository string
	// Version returns the server's own version; "" or "dev" means an
	// untagged build, for which `current` does not exist.
	Version func() string
	// TagTTL is how long a tag-to-digest resolution is cached.
	TagTTL time.Duration
	// HTTPClient performs the upstream calls. It must not follow redirects
	// for blobs; New wraps it to make sure.
	HTTPClient *http.Client
	// Logger defaults to slog.Default().
	Logger *slog.Logger
}

type cachedManifest struct {
	body        []byte
	contentType string
}

type cachedTag struct {
	digest    string
	expiresAt time.Time
}

// Registry serves the read-only registry API.
type Registry struct {
	upstream   string
	repository string
	version    func() string
	tagTTL     time.Duration
	client     *http.Client
	logger     *slog.Logger

	mu        sync.Mutex
	manifests map[string]cachedManifest
	tags      map[string]cachedTag
	token     string
	tokenExp  time.Time
}

// New creates a Registry.
func New(cfg Config) *Registry {
	upstream := strings.TrimRight(cfg.Upstream, "/")
	if upstream == "" {
		upstream = DefaultUpstream
	}

	repository := cfg.UpstreamRepository
	if repository == "" {
		repository = DefaultUpstreamRepository
	}

	tagTTL := cfg.TagTTL
	if tagTTL <= 0 {
		tagTTL = DefaultTagTTL
	}

	base := cfg.HTTPClient
	if base == nil {
		base = &http.Client{Timeout: upstreamTimeout}
	}

	client := *base
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &Registry{
		upstream:   upstream,
		repository: repository,
		version:    cfg.Version,
		tagTTL:     tagTTL,
		client:     &client,
		logger:     logger.With("component", "agent_registry"),
		manifests:  map[string]cachedManifest{},
		tags:       map[string]cachedTag{},
	}
}

// ServeHTTP implements http.Handler.
func (r *Registry) ServeHTTP(writer http.ResponseWriter, req *http.Request) {
	writer.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")

	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		writeError(writer, http.StatusMethodNotAllowed, "UNSUPPORTED", "the registry is read-only")

		return
	}

	rest := strings.TrimPrefix(req.URL.Path, PathPrefix)
	if rest == "" || rest == "/" {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)

		if req.Method == http.MethodGet {
			_, _ = writer.Write([]byte("{}"))
		}

		return
	}

	name, kind, ref, ok := splitPath(rest)
	if !ok {
		writeError(writer, http.StatusNotFound, "NAME_UNKNOWN", "repository name not known to registry")

		return
	}

	if name != RepositoryName {
		writeError(writer, http.StatusNotFound, "NAME_UNKNOWN", "repository name not known to registry")

		return
	}

	switch kind {
	case "manifests":
		r.serveManifest(writer, req, ref)
	case "blobs":
		r.serveBlob(writer, req, ref)
	default:
		writeError(writer, http.StatusNotFound, "NAME_UNKNOWN", "repository name not known to registry")
	}
}

// splitPath parses "/<name>/<kind>/<ref>" (name has no slash here: the one
// repository is a single segment).
func splitPath(rest string) (string, string, string, bool) {
	parts := strings.Split(strings.TrimPrefix(rest, "/"), "/")
	if len(parts) < 3 {
		return "", "", "", false
	}

	// A multi-segment repository name ("acme/other/manifests/x") is simply an
	// unknown repository.
	ref := parts[len(parts)-1]
	kind := parts[len(parts)-2]
	name := strings.Join(parts[:len(parts)-2], "/")

	return name, kind, ref, true
}

// serveManifest answers a manifest by `current`, a version tag or a digest.
func (r *Registry) serveManifest(writer http.ResponseWriter, req *http.Request, ref string) {
	if ref == CurrentTag {
		ref = r.currentVersion()
		if ref == "" {
			writeError(writer, http.StatusNotFound, "MANIFEST_UNKNOWN",
				"this server is an untagged build: there is no current image")

			return
		}
	}

	if !digestPattern.MatchString(ref) && !tagPattern.MatchString(ref) {
		writeError(writer, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest unknown")

		return
	}

	digest, manifest, err := r.manifest(req.Context(), ref)
	if err != nil {
		if errors.Is(err, errNotFound) {
			writeError(writer, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest unknown")

			return
		}

		r.logger.WarnContext(req.Context(), "upstream manifest fetch failed", "ref", ref, "error", err)
		writeError(writer, http.StatusBadGateway, "UNAVAILABLE", "upstream registry unavailable")

		return
	}

	header := writer.Header()
	header.Set("Content-Type", manifest.contentType)
	header.Set("Docker-Content-Digest", digest)
	header.Set("Content-Length", strconv.Itoa(len(manifest.body)))
	writer.WriteHeader(http.StatusOK)

	if req.Method == http.MethodGet {
		_, _ = writer.Write(manifest.body)
	}
}

// serveBlob answers a blob with a 307 to the upstream's signed URL.
func (r *Registry) serveBlob(writer http.ResponseWriter, req *http.Request, digest string) {
	if !digestPattern.MatchString(digest) {
		writeError(writer, http.StatusNotFound, "BLOB_UNKNOWN", "blob unknown to registry")

		return
	}

	location, err := r.blobLocation(req.Context(), digest)
	if err != nil {
		if errors.Is(err, errNotFound) {
			writeError(writer, http.StatusNotFound, "BLOB_UNKNOWN", "blob unknown to registry")

			return
		}

		r.logger.WarnContext(req.Context(), "upstream blob redirect failed", "digest", digest, "error", err)
		writeError(writer, http.StatusBadGateway, "UNAVAILABLE", "upstream registry unavailable")

		return
	}

	writer.Header().Set("Location", location)
	writer.WriteHeader(http.StatusTemporaryRedirect)
}

// currentVersion is the server's version, or "" on an untagged build.
func (r *Registry) currentVersion() string {
	if r.version == nil {
		return ""
	}

	v := strings.TrimPrefix(r.version(), "v")
	if v == "" || v == "dev" {
		return ""
	}

	return v
}

// manifest resolves ref (tag or digest) through the caches, then upstream.
func (r *Registry) manifest(ctx context.Context, ref string) (string, cachedManifest, error) {
	isDigest := digestPattern.MatchString(ref)

	r.mu.Lock()
	digest := ref

	if !isDigest {
		digest = ""
		if tag, ok := r.tags[ref]; ok && time.Now().Before(tag.expiresAt) {
			digest = tag.digest
		}
	}

	if digest != "" {
		if cached, ok := r.manifests[digest]; ok {
			r.mu.Unlock()

			return digest, cached, nil
		}
	}
	r.mu.Unlock()

	digest, fetched, err := r.fetchManifest(ctx, ref)
	if err != nil {
		return "", cachedManifest{}, err
	}

	if isDigest && digest != ref {
		return "", cachedManifest{}, fmt.Errorf("%w: asked %s, got %s", errUpstreamDigest, ref, digest)
	}

	r.mu.Lock()
	if len(r.manifests) >= maxCachedManifests {
		clear(r.manifests)
	}

	r.manifests[digest] = fetched

	if !isDigest {
		if len(r.tags) >= maxCachedTags {
			clear(r.tags)
		}

		r.tags[ref] = cachedTag{digest: digest, expiresAt: time.Now().Add(r.tagTTL)}
	}
	r.mu.Unlock()

	return digest, fetched, nil
}

// fetchManifest reads one manifest from the upstream and checks its digest.
func (r *Registry) fetchManifest(ctx context.Context, ref string) (string, cachedManifest, error) {
	resp, err := r.upstreamGet(ctx, "/manifests/"+ref, manifestAccept)
	if err != nil {
		return "", cachedManifest{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return "", cachedManifest{}, errNotFound
	case resp.StatusCode != http.StatusOK:
		return "", cachedManifest{}, fmt.Errorf("%w: manifest %s: %d", errUpstreamStatus, ref, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxManifestBytes+1))
	if err != nil {
		return "", cachedManifest{}, fmt.Errorf("read upstream manifest: %w", err)
	}

	if len(body) > maxManifestBytes {
		return "", cachedManifest{}, errUpstreamTooLarge
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		return "", cachedManifest{}, errUpstreamNoType
	}

	sum := sha256.Sum256(body)
	computed := "sha256:" + hex.EncodeToString(sum[:])

	if announced := resp.Header.Get("Docker-Content-Digest"); announced != "" && announced != computed {
		return "", cachedManifest{}, fmt.Errorf("%w: announced %s, body is %s", errUpstreamDigest, announced, computed)
	}

	return computed, cachedManifest{body: body, contentType: contentType}, nil
}

// blobLocation asks the upstream for a blob and returns where it redirects.
func (r *Registry) blobLocation(ctx context.Context, digest string) (string, error) {
	resp, err := r.upstreamGet(ctx, "/blobs/"+digest, "")
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusNotFound:
		return "", errNotFound
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
	default:
		return "", fmt.Errorf("%w: %d", errUpstreamRedirect, resp.StatusCode)
	}

	location, err := resp.Location()
	if err != nil {
		return "", fmt.Errorf("%w: %w", errUpstreamRedirect, err)
	}

	return location.String(), nil
}

// upstreamGet performs an authenticated GET on the upstream repository,
// refreshing the anonymous token once on a 401.
func (r *Registry) upstreamGet(ctx context.Context, suffix, accept string) (*http.Response, error) {
	target := r.upstream + "/v2/" + r.repository + suffix

	for attempt := 0; ; attempt++ {
		token, err := r.pullToken(ctx, attempt > 0)
		if err != nil {
			return nil, err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, fmt.Errorf("build upstream request: %w", err)
		}

		req.Header.Set("Authorization", "Bearer "+token)

		if accept != "" {
			req.Header.Set("Accept", accept)
		}

		resp, err := r.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("upstream request: %w", err)
		}

		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()

			continue
		}

		return resp, nil
	}
}

// pullToken returns an anonymous pull token for the upstream repository.
func (r *Registry) pullToken(ctx context.Context, refresh bool) (string, error) {
	r.mu.Lock()
	if !refresh && r.token != "" && time.Now().Before(r.tokenExp) {
		token := r.token
		r.mu.Unlock()

		return token, nil
	}
	r.mu.Unlock()

	host := r.upstream
	if parsed, err := url.Parse(r.upstream); err == nil {
		host = parsed.Host
	}

	query := url.Values{
		"scope":   []string{"repository:" + r.repository + ":pull"},
		"service": []string{host},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.upstream+"/token?"+query.Encode(), nil)
	if err != nil {
		return "", fmt.Errorf("build token request: %w", err)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("upstream token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: token: %d", errUpstreamStatus, resp.StatusCode)
	}

	var payload struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"` //nolint:tagliatelle // the registry token API's own field name
	}

	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode upstream token: %w", err)
	}

	token := payload.Token
	if token == "" {
		token = payload.AccessToken
	}

	if token == "" {
		return "", errUpstreamNoToken
	}

	r.mu.Lock()
	r.token = token
	r.tokenExp = time.Now().Add(tokenTTL)
	r.mu.Unlock()

	return token, nil
}

// registryError is one entry of the registry error shape.
type registryError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// registryErrors is the registry error body.
type registryErrors struct {
	Errors []registryError `json:"errors"`
}

// writeError writes the registry error shape.
func writeError(writer http.ResponseWriter, status int, code, message string) {
	body, err := json.Marshal(registryErrors{Errors: []registryError{{Code: code, Message: message}}})
	if err != nil {
		body = []byte(`{"errors":[]}`)
	}

	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Content-Length", strconv.Itoa(len(body)))
	writer.WriteHeader(status)
	_, _ = writer.Write(body)
}
