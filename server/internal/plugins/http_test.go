package plugins

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/configregistry"
	"github.com/fclairamb/solidping/server/internal/checkers/registrycore"
	"github.com/fclairamb/solidping/server/internal/plugins/protocol"
	"github.com/fclairamb/solidping/server/internal/servicesig"
)

// fakeHTTPPlugin is a plugin service on httptest. It verifies every request
// against keys and records what it got.
type fakeHTTPPlugin struct {
	keys servicesig.KeySet

	mu          sync.Mutex
	descriptors []protocol.TypeDescriptor
	version     int
	failDescr   bool
	execute     http.HandlerFunc
	validate    http.HandlerFunc
	calls       map[string]int
	bodies      []string
	keyIDs      []string
	verifyErrs  []error

	server *httptest.Server
}

func newFakeHTTPPlugin(t *testing.T, keys servicesig.KeySet, descriptors ...protocol.TypeDescriptor) *fakeHTTPPlugin {
	t.Helper()

	fake := &fakeHTTPPlugin{
		keys: keys, descriptors: descriptors, version: protocol.ProtocolVersion, calls: map[string]int{},
	}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(fake.server.Close)

	return fake
}

func (f *fakeHTTPPlugin) serve(writer http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	_, verifyErr := servicesig.VerifyRequest(req, f.keys, body, time.Now())

	f.mu.Lock()
	f.calls[req.URL.Path]++
	f.bodies = append(f.bodies, string(body))
	f.keyIDs = append(f.keyIDs, req.Header.Get(servicesig.HeaderKeyID))

	if verifyErr != nil {
		f.verifyErrs = append(f.verifyErrs, verifyErr)
	}

	descriptors, version, failDescr := f.descriptors, f.version, f.failDescr
	execute, validate := f.execute, f.validate
	f.mu.Unlock()

	req.Body = io.NopCloser(bytes.NewReader(body))

	switch req.URL.Path {
	case protocol.PathCheckers:
		if failDescr {
			http.Error(writer, "down", http.StatusInternalServerError)

			return
		}

		raw, err := json.Marshal(protocol.DescribeResponse{
			ProtocolVersion: version, Version: "1.2.0", Data: descriptors,
		})
		if err != nil {
			http.Error(writer, err.Error(), http.StatusInternalServerError)

			return
		}

		_, _ = writer.Write(raw)
	case protocol.PathExecute:
		if execute == nil {
			_, _ = io.WriteString(writer, `{"status":"up","output":{"message":"ok"}}`)

			return
		}

		execute(writer, req)
	case protocol.PathValidate:
		if validate == nil {
			_, _ = io.WriteString(writer, `{"errors":[]}`)

			return
		}

		validate(writer, req)
	default:
		http.NotFound(writer, req)
	}
}

func (f *fakeHTTPPlugin) set(fn func(f *fakeHTTPPlugin)) {
	f.mu.Lock()
	defer f.mu.Unlock()

	fn(f)
}

func (f *fakeHTTPPlugin) callCount(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.calls[path]
}

func descriptor(checkType string) protocol.TypeDescriptor {
	return protocol.TypeDescriptor{
		Type: checkType, Description: "http test type", Labels: []string{"safe", "category:network"},
		Schema: json.RawMessage(testSchema), MinPeriod: "1m",
	}
}

// testKeys returns a key set and an environment variable holding it. The
// name is unique, so parallel tests never share one.
func testKeys(t *testing.T, ids ...string) (servicesig.KeySet, string) {
	t.Helper()

	if len(ids) == 0 {
		ids = []string{"2026-10"}
	}

	keys := make(servicesig.KeySet, 0, len(ids))
	for _, id := range ids {
		keys = append(keys, servicesig.Key{ID: id, Secret: "secret-" + id})
	}

	raw, err := json.Marshal(keys)
	require.NoError(t, err)

	name := fmt.Sprintf("SP_PLUGIN_TEST_KEYS_%d", typeSeq.Add(1))
	setEnv(t, name, string(raw))

	return keys, name
}

// setEnv sets a variable no other test reads; t.Setenv cannot run in a
// parallel test.
func setEnv(t *testing.T, name, value string) {
	t.Helper()

	require.NoError(t, os.Setenv(name, value)) //nolint:usetesting // unique name, parallel test
	t.Cleanup(func() { _ = os.Unsetenv(name) })
}

func httpManifest(name, url, env string) map[string]any {
	return map[string]any{"name": name, "protocol": "http", "url": url, "secretEnv": env}
}

// syncBuffer is a log sink safe for the refresh goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p) //nolint:wrapcheck // test sink
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// loadHTTPForTest loads one http plugin and returns its set, transport and
// log output.
func loadHTTPForTest(t *testing.T, manifest map[string]any) (*Set, *httpTransport, *syncBuffer) {
	t.Helper()

	root := t.TempDir()
	writePlugin(t, root, "plugin", manifest, nil)

	logs := &syncBuffer{}
	set := Load(t.Context(), root, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(set.Close)

	var runner *httpTransport
	if len(set.Plugins) == 1 {
		runner, _ = set.Plugins[0].transport.(*httpTransport)
	}

	return set, runner, logs
}

func runPluginType(t *testing.T, checkType string, config map[string]any) *checkerdef.Result {
	t.Helper()

	checker, ok := registrycore.GetChecker(checkerdef.CheckType(checkType))
	require.True(t, ok)

	cfg, ok := registrycore.ParseConfig(checkerdef.CheckType(checkType))
	require.True(t, ok)
	require.NoError(t, cfg.FromMap(config))

	res, err := checker.Execute(t.Context(), cfg)
	require.NoError(t, err)

	return res
}

func TestHTTPPluginLoadsDescribedTypes(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	keys, env := testKeys(t)
	checkType := uniqueType("http")
	fake := newFakeHTTPPlugin(t, keys, descriptor(checkType))

	set, runner, logs := loadHTTPForTest(t, httpManifest("svc", fake.server.URL, env))
	r.Empty(set.Errors)
	r.NotNil(runner)
	r.Equal([]checkerdef.CheckType{checkerdef.CheckType(checkType)}, set.Types)
	r.Equal(1, fake.callCount(protocol.PathCheckers))
	r.NotContains(logs.String(), "level=WARN", "a loopback http:// plugin loads without a warning")

	meta := checkerdef.GetCheckTypeMeta(checkerdef.CheckType(checkType))
	r.NotNil(meta)
	r.Equal(&checkerdef.PluginRef{Name: "svc", Version: "1.2.0"}, meta.Plugin)
	r.Equal([]string{"unsafe", "category:network"}, meta.Labels, "http types are always unsafe")
	r.True(meta.ExcludedFromTally)

	var reported *protocol.TypeManifest

	for _, manifest := range LoadedTypeManifests() {
		if manifest.Type == checkType {
			reported = &manifest
		}
	}

	r.NotNil(reported, "the descriptor is reported to the server like an agent manifest")
	r.Equal(ProtocolHTTP, reported.Protocol)

	remote, err := CompileRemote(reported)
	r.NoError(err)
	r.Contains(remote.Meta().Labels, "unsafe")

	set.Close()
	r.Nil(checkerdef.GetCheckTypeMeta(checkerdef.CheckType(checkType)))
}

func TestHTTPPluginManifestRefused(t *testing.T) {
	t.Parallel()

	_, goodEnv := testKeys(t)

	badEnv := fmt.Sprintf("SP_PLUGIN_TEST_KEYS_%d", typeSeq.Add(1))
	setEnv(t, badEnv, "not json")

	cases := []struct {
		name string
		url  string
		env  string
	}{
		{"plain http to a remote host", "http://plugin.acme.com:7979", goodEnv},
		{"not http", "ftp://127.0.0.1:7979", goodEnv},
		{"unset key variable", "http://127.0.0.1:7979", "SP_PLUGIN_TEST_KEYS_NEVER_SET"},
		{"no secretEnv", "http://127.0.0.1:7979", ""},
		{"unparsable key set", "http://127.0.0.1:7979", badEnv},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			set, runner, logs := loadHTTPForTest(t, httpManifest("refused", tc.url, tc.env))
			r.Nil(runner)
			r.Empty(set.Plugins)
			r.Len(set.Errors, 1)
			r.Contains(logs.String(), "level=ERROR")
		})
	}
}

func TestHTTPPluginRemoteHTTPSWarnsOnce(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	_, env := testKeys(t)

	set, runner, logs := loadHTTPForTest(t, httpManifest("remote", "https://plugin.acme.invalid:7979", env))
	r.Empty(set.Errors)
	r.NotNil(runner, "a non-loopback https:// URL loads")
	r.Equal(1, strings.Count(logs.String(), "is not on loopback"))
}

func TestHTTPPluginUnknownProtocolVersionIsRefused(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	keys, env := testKeys(t)
	checkType := uniqueType("httpv")
	fake := newFakeHTTPPlugin(t, keys, descriptor(checkType))
	fake.set(func(f *fakeHTTPPlugin) { f.version = 2 })

	set, runner, _ := loadHTTPForTest(t, httpManifest("v2", fake.server.URL, env))
	r.Nil(runner)
	r.Len(set.Errors, 1)
	r.ErrorIs(set.Errors[0], ErrHTTPPluginVersion)
	r.Nil(checkerdef.GetCheckTypeMeta(checkerdef.CheckType(checkType)))
}

func TestHTTPPluginSkipsInvalidDescriptors(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// Another plugin already holds this type.
	taken := uniqueType("taken")
	jsRoot := t.TempDir()
	writePlugin(t, jsRoot, "owner", jsManifest("owner", typeDecl(taken)),
		map[string]string{"check.js": testScript, "schema.json": testSchema})
	r.Empty(loadForTest(t, jsRoot).Errors)

	good := uniqueType("good")
	badSchema := descriptor(uniqueType("badschema"))
	badSchema.Schema = json.RawMessage(`{"type": 12}`)
	noPrefix := descriptor("finger-" + good)

	keys, env := testKeys(t)
	fake := newFakeHTTPPlugin(t, keys, descriptor(good), noPrefix, descriptor(taken), badSchema)

	set, runner, _ := loadHTTPForTest(t, httpManifest("mixed", fake.server.URL, env))
	r.NotNil(runner)
	r.Equal([]checkerdef.CheckType{checkerdef.CheckType(good)}, set.Types)
	r.Len(set.Errors, 3)
	r.ErrorIs(set.Errors[0], ErrTypePrefix)
	r.ErrorIs(set.Errors[1], ErrDuplicateType)
	r.ErrorIs(set.Errors[2], ErrBadSchema)

	meta := checkerdef.GetCheckTypeMeta(checkerdef.CheckType(taken))
	r.Equal("owner", meta.Plugin.Name, "the other plugin keeps its type")
}

func TestHTTPPluginRefreshAddsAndRemovesTypes(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	keys, env := testKeys(t)
	first, second := uniqueType("first"), uniqueType("second")
	fake := newFakeHTTPPlugin(t, keys, descriptor(first))

	_, runner, _ := loadHTTPForTest(t, httpManifest("refresh", fake.server.URL, env))
	r.NotNil(runner)
	r.Contains(registrycore.PluginTypes(), first)

	fake.set(func(f *fakeHTTPPlugin) { f.descriptors = []protocol.TypeDescriptor{descriptor(second)} })
	runner.refresh(t.Context())

	r.Contains(registrycore.PluginTypes(), second)
	r.NotContains(registrycore.PluginTypes(), first, "a type gone from GET /checkers is unregistered")
	r.Nil(checkerdef.GetCheckTypeMeta(checkerdef.CheckType(first)))

	// A changed descriptor is re-registered.
	changed := descriptor(second)
	changed.Description = "changed"
	fake.set(func(f *fakeHTTPPlugin) { f.descriptors = []protocol.TypeDescriptor{changed} })
	runner.refresh(t.Context())
	r.Equal("changed", checkerdef.GetCheckTypeMeta(checkerdef.CheckType(second)).Description)
}

func TestHTTPPluginStopsAdvertisingAfterThreeFailures(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	keys, env := testKeys(t)
	checkType := uniqueType("flaky")
	fake := newFakeHTTPPlugin(t, keys, descriptor(checkType))

	_, runner, _ := loadHTTPForTest(t, httpManifest("flaky", fake.server.URL, env))
	r.NotNil(runner)

	advertised := func() bool {
		for _, advertisedType := range Advertised(registrycore.PluginTypes()) {
			if advertisedType == checkType {
				return true
			}
		}

		return false
	}

	r.True(advertised(), "positive control: advertised while the service is up")

	fake.set(func(f *fakeHTTPPlugin) { f.failDescr = true })

	runner.refresh(t.Context())
	runner.refresh(t.Context())
	r.True(advertised(), "two failures keep the last good descriptors advertised")

	runner.refresh(t.Context())
	r.False(advertised(), "three failures stop advertising")
	r.Contains(registrycore.PluginTypes(), checkType, "the type stays registered")

	fake.set(func(f *fakeHTTPPlugin) { f.failDescr = false })
	runner.refresh(t.Context())
	r.True(advertised(), "the next success resumes advertising")
}

func TestHTTPPluginExecuteMapping(t *testing.T) {
	t.Parallel()

	keys, env := testKeys(t)
	checkType := uniqueType("exec")
	fake := newFakeHTTPPlugin(t, keys, descriptor(checkType))

	_, runner, _ := loadHTTPForTest(t, httpManifest("mapping", fake.server.URL, env))
	require.NotNil(t, runner)

	answer := func(status int, body string) http.HandlerFunc {
		return func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(status)
			_, _ = io.WriteString(writer, body)
		}
	}

	cases := []struct {
		name    string
		handler http.HandlerFunc
		want    checkerdef.Status
	}{
		{
			"up", answer(http.StatusOK, `{"status":"up","metrics":{"lines":3},"output":{"message":"hi"}}`),
			checkerdef.StatusUp,
		},
		{"down", answer(http.StatusOK, `{"status":"down"}`), checkerdef.StatusDown},
		{"warning", answer(http.StatusOK, `{"status":"warning"}`), checkerdef.StatusWarning},
		{"500", answer(http.StatusInternalServerError, `{"status":"up"}`), checkerdef.StatusError},
		{"invalid JSON", answer(http.StatusOK, `{"status":`), checkerdef.StatusError},
		{"unknown status", answer(http.StatusOK, `{"status":"great"}`), checkerdef.StatusError},
		{"slower than the deadline", func(writer http.ResponseWriter, req *http.Request) {
			select {
			case <-req.Context().Done():
			case <-time.After(5 * time.Second):
			}

			_, _ = io.WriteString(writer, `{"status":"up"}`)
		}, checkerdef.StatusTimeout},
	}

	// The cases share one fake: run them in order.
	for _, tc := range cases {
		fake.set(func(f *fakeHTTPPlugin) { f.execute = tc.handler })

		res := runPluginType(t, checkType, map[string]any{"host": "plugin.acme.com", "timeout": "300ms"})
		require.Equal(t, tc.want, res.Status, "%s: %v", tc.name, res.Output)

		if tc.want == checkerdef.StatusError {
			require.Contains(t, res.Output["message"], "plugin", tc.name)
		}
	}
}

func TestHTTPPluginOversizedAnswerIsAnError(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	keys, env := testKeys(t)
	checkType := uniqueType("big")
	fake := newFakeHTTPPlugin(t, keys, descriptor(checkType))

	_, runner, _ := loadHTTPForTest(t, httpManifest("big", fake.server.URL, env))
	r.NotNil(runner)

	const total = 64 * 1024 * 1024

	written := make(chan int, 1)

	fake.set(func(f *fakeHTTPPlugin) {
		f.execute = func(writer http.ResponseWriter, _ *http.Request) {
			chunk := bytes.Repeat([]byte("a"), 64*1024)
			sent := 0

			for sent < total {
				n, err := writer.Write(chunk)
				sent += n

				if err != nil {
					break
				}
			}

			written <- sent
		}
	})

	res := runPluginType(t, checkType, map[string]any{"host": "plugin.acme.com", "timeout": "10s"})
	r.Equal(checkerdef.StatusError, res.Status)
	r.Contains(res.Output["message"], "larger than 1 MiB")

	select {
	case sent := <-written:
		r.Less(sent, total, "the answer was not read past the cap")
	case <-time.After(10 * time.Second):
		r.Fail("the plugin handler never stopped writing")
	}
}

func TestHTTPPluginRedirectIsAnErrorAndNotFollowed(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	keys, env := testKeys(t)
	checkType := uniqueType("redirect")

	// The redirect target is itself a working plugin service.
	target := newFakeHTTPPlugin(t, keys, descriptor(uniqueType("target")))

	fake := newFakeHTTPPlugin(t, keys, descriptor(checkType))
	fake.set(func(f *fakeHTTPPlugin) {
		f.execute = func(writer http.ResponseWriter, req *http.Request) {
			http.Redirect(writer, req, target.server.URL+protocol.PathExecute, http.StatusFound)
		}
	})

	_, runner, _ := loadHTTPForTest(t, httpManifest("redirect", fake.server.URL, env))
	r.NotNil(runner)

	res := runPluginType(t, checkType, map[string]any{"host": "plugin.acme.com"})
	r.Equal(checkerdef.StatusError, res.Status)
	r.Contains(res.Output["message"], "302")
	r.Equal(0, target.callCount(protocol.PathExecute), "the redirect target gets no request")

	// Positive control: the same target gets the request when it is the
	// manifest URL.
	targetSet, targetRunner, _ := loadHTTPForTest(t, httpManifest("target", target.server.URL, env))
	r.NotNil(targetRunner)
	r.Len(targetSet.Types, 1)

	res = runPluginType(t, string(targetSet.Types[0]), map[string]any{"host": "plugin.acme.com"})
	r.Equal(checkerdef.StatusUp, res.Status, res.Output)
	r.Equal(1, target.callCount(protocol.PathExecute))
}

func TestHTTPPluginRequestsAreSigned(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	keys, env := testKeys(t, "2026-10")
	checkType := uniqueType("signed")
	fake := newFakeHTTPPlugin(t, keys, descriptor(checkType))

	_, runner, _ := loadHTTPForTest(t, httpManifest("signed", fake.server.URL, env))
	r.NotNil(runner)

	res := runPluginType(t, checkType, map[string]any{"host": "plugin.acme.com"})
	r.Equal(checkerdef.StatusUp, res.Status, res.Output)

	fake.mu.Lock()
	r.GreaterOrEqual(len(fake.keyIDs), 2)
	r.Empty(fake.verifyErrs, "every request passes servicesig.VerifyRequest")
	r.Equal([]string{"2026-10", "2026-10"}, fake.keyIDs)
	fake.mu.Unlock()

	// Rotation: the new key goes first in the set, the plugin knows both.
	rotated, rotatedEnv := testKeys(t, "2026-11", "2026-10")
	rotatedType := uniqueType("rotated")
	rotatedFake := newFakeHTTPPlugin(t, rotated, descriptor(rotatedType))

	_, rotatedRunner, _ := loadHTTPForTest(t, httpManifest("rotated", rotatedFake.server.URL, rotatedEnv))
	r.NotNil(rotatedRunner)

	res = runPluginType(t, rotatedType, map[string]any{"host": "plugin.acme.com"})
	r.Equal(checkerdef.StatusUp, res.Status, res.Output)

	rotatedFake.mu.Lock()
	defer rotatedFake.mu.Unlock()

	r.Empty(rotatedFake.verifyErrs)
	r.Equal([]string{"2026-11", "2026-11"}, rotatedFake.keyIDs)
}

func TestHTTPPluginSecretsReachThePluginOnly(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	keys, env := testKeys(t)
	checkType := uniqueType("secret")
	fake := newFakeHTTPPlugin(t, keys, descriptor(checkType))

	_, runner, logs := loadHTTPForTest(t, httpManifest("secret", fake.server.URL, env))
	r.NotNil(runner)

	const secret = "s3cr3t-acme-token"

	cases := []struct {
		name    string
		handler http.HandlerFunc
		want    checkerdef.Status
	}{
		{"500", func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusInternalServerError)
		}, checkerdef.StatusError},
		{"200 with a result", func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(writer,
				`{"status":"up","metrics":{"lines":2},"output":{"message":"finger answered","lines":"alice"}}`)
		}, checkerdef.StatusUp},
	}

	// The cases share one fake: run them in order.
	for _, tc := range cases {
		fake.set(func(f *fakeHTTPPlugin) { f.execute = tc.handler })

		res := runPluginType(t, checkType, map[string]any{"host": "plugin.acme.com", "token": secret})
		r.Equal(tc.want, res.Status, "%s: %v", tc.name, res.Output)

		fake.mu.Lock()
		r.Contains(strings.Join(fake.bodies, "\n"), secret, "%s: the secret reaches /execute", tc.name)
		fake.mu.Unlock()

		stored, err := json.Marshal(map[string]any{"output": res.Output, "metrics": res.Metrics})
		r.NoError(err)
		// Positive control: the stored result holds what the plugin returned.
		if tc.want == checkerdef.StatusUp {
			r.Contains(string(stored), "finger answered", tc.name)
		}

		r.NotContains(string(stored), secret, "%s: not in the stored result", tc.name)
		r.NotContains(logs.String(), secret, "%s: not in the logs", tc.name)
	}
}

func TestHTTPPluginUnreachableResultHidesTheURL(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	keys, env := testKeys(t)
	checkType := uniqueType("gone")
	fake := newFakeHTTPPlugin(t, keys, descriptor(checkType))

	_, runner, logs := loadHTTPForTest(t, httpManifest("gone", fake.server.URL, env))
	r.NotNil(runner)

	address := strings.TrimPrefix(fake.server.URL, "http://")
	fake.server.Close()

	res := runPluginType(t, checkType, map[string]any{"host": "plugin.acme.com"})
	r.Equal(checkerdef.StatusError, res.Status)

	stored, err := json.Marshal(res.Output)
	r.NoError(err)
	r.Contains(string(stored), ErrHTTPPluginNoAnswer.Error())
	r.NotContains(string(stored), address, "the plugin address is not in the stored result")
	r.Contains(logs.String(), address, "the worker log keeps the full error")
}

func TestHTTPPluginValidate(t *testing.T) {
	t.Parallel()

	keys, env := testKeys(t)
	validated := descriptor(uniqueType("validated"))
	validated.Validate = true
	notValidated := descriptor(uniqueType("notvalidated"))

	fake := newFakeHTTPPlugin(t, keys, validated, notValidated)

	_, runner, logs := loadHTTPForTest(t, httpManifest("validating", fake.server.URL, env))
	require.NotNil(t, runner)

	spec := func() *checkerdef.CheckSpec {
		return &checkerdef.CheckSpec{Config: map[string]any{"host": "plugin.acme.com"}}
	}

	r := require.New(t)

	// "validate": false never calls the endpoint.
	r.NoError(configregistry.ValidateSpec(checkerdef.CheckType(notValidated.Type), spec()))
	r.Equal(0, fake.callCount(protocol.PathValidate))

	// Positive control: the validating type calls it once.
	r.NoError(configregistry.ValidateSpec(checkerdef.CheckType(validated.Type), spec()))
	r.Equal(1, fake.callCount(protocol.PathValidate))

	// The schema runs first: a config it rejects never reaches the plugin.
	err := configregistry.ValidateSpec(checkerdef.CheckType(validated.Type),
		&checkerdef.CheckSpec{Config: map[string]any{}})
	r.NotNil(checkerdef.IsConfigError(err))
	r.Equal(1, fake.callCount(protocol.PathValidate))

	// Errors from the plugin are a validation error naming every field.
	fake.set(func(f *fakeHTTPPlugin) {
		f.validate = func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(writer,
				`{"errors":[{"field":"host","message":"is not a finger server"},{"field":"user","message":"too long"}]}`)
		}
	})

	configErr := checkerdef.IsConfigError(configregistry.ValidateSpec(checkerdef.CheckType(validated.Type), spec()))
	r.NotNil(configErr, "a ConfigError is answered as VALIDATION_ERROR")
	r.Equal("host", configErr.Parameter)
	r.Contains(configErr.Message, "is not a finger server")
	r.Contains(configErr.Message, "too long")

	// A failing or unreachable service saves on the schema alone.
	fake.set(func(f *fakeHTTPPlugin) {
		f.validate = func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusBadGateway)
		}
	})

	r.NoError(configregistry.ValidateSpec(checkerdef.CheckType(validated.Type), spec()))
	r.Contains(logs.String(), "could not validate a config")

	fake.server.Close()
	r.NoError(configregistry.ValidateSpec(checkerdef.CheckType(validated.Type), spec()))
}
