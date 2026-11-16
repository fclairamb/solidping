package importers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/checks/importers"
)

const pingdomTestToken = "pd_super_secret_token_value"

// pingdomServer is a fake Pingdom API 3.1 serving the fixtures.
type pingdomServer struct {
	*httptest.Server
	mu    sync.Mutex
	auths []string
	paths []string
}

func newPingdomServer(t *testing.T, status int) *pingdomServer {
	t.Helper()

	srv := &pingdomServer{}

	var details []struct {
		Check json.RawMessage `json:"check"`
	}

	require.NoError(t, json.Unmarshal(readFixture(t, "pingdom-check-details.json"), &details))

	byID := map[string]json.RawMessage{}

	for _, d := range details {
		var c struct {
			ID int `json:"id"`
		}

		require.NoError(t, json.Unmarshal(d.Check, &c))

		byID[strconv.Itoa(c.ID)] = d.Check
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/3.1/checks", func(w http.ResponseWriter, r *http.Request) {
		srv.record(r)

		if status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"echo ` + r.Header.Get("Authorization") + `"}`))

			return
		}

		_, _ = w.Write(readFixture(t, "pingdom-checks.json"))
	})
	mux.HandleFunc("/api/3.1/checks/", func(w http.ResponseWriter, r *http.Request) {
		srv.record(r)

		check, ok := byID[strings.TrimPrefix(r.URL.Path, "/api/3.1/checks/")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)

			return
		}

		_, _ = w.Write([]byte(`{"check":` + string(check) + `}`))
	})

	srv.Server = httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv
}

func (s *pingdomServer) record(r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.auths = append(s.auths, r.Header.Get("Authorization"))
	s.paths = append(s.paths, r.URL.RequestURI())
}

func pingdomTokenBody(t *testing.T) []byte {
	t.Helper()

	body, err := json.Marshal(map[string]string{"token": pingdomTestToken})
	require.NoError(t, err)

	return body
}

func TestPingdomConverterGoldenFromPastedDetails(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	conv := importers.NewPingdomConverter(importers.PingdomOptions{})
	r.Equal("pingdom", conv.Source())

	result, err := conv.Convert(readFixture(t, "pingdom-check-details.json"))
	r.NoError(err)
	r.Len(result.Document.Checks, 10, "the transaction check is skipped")

	assertGolden(t, "pingdom", result)
}

func TestPingdomConverterPastedListUsesListFields(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	conv := importers.NewPingdomConverter(importers.PingdomOptions{})
	result, err := conv.Convert(readFixture(t, "pingdom-checks.json"))
	r.NoError(err)
	r.Len(result.Document.Checks, 10)
}

func TestPingdomConverterFetchSendsBearerAndMatchesPaste(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := newPingdomServer(t, 0)
	conv := importers.NewPingdomConverter(importers.PingdomOptions{BaseURL: srv.URL})

	fetched, err := conv.Convert(pingdomTokenBody(t))
	r.NoError(err)

	pasted, err := conv.Convert(readFixture(t, "pingdom-check-details.json"))
	r.NoError(err)

	fetchedJSON, err := json.Marshal(fetched.Document.Checks)
	r.NoError(err)

	pastedJSON, err := json.Marshal(pasted.Document.Checks)
	r.NoError(err)

	r.JSONEq(string(pastedJSON), string(fetchedJSON))
	r.Contains(srv.paths[0], "include_tags=true")
	r.Len(srv.paths, 12, "one list call plus one details call per check")

	for _, auth := range srv.auths {
		r.Equal("Bearer "+pingdomTestToken, auth)
	}

	r.NotContains(string(fetchedJSON), pingdomTestToken)

	warnings, err := json.Marshal(fetched.Warnings)
	r.NoError(err)
	r.NotContains(string(warnings), pingdomTestToken)
}

func TestPingdomConverterUnauthorizedAndRateLimit(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := newPingdomServer(t, http.StatusUnauthorized)
	conv := importers.NewPingdomConverter(importers.PingdomOptions{BaseURL: srv.URL})

	_, err := conv.Convert(pingdomTokenBody(t))
	r.ErrorIs(err, importers.ErrPingdomUnauthorized)
	r.NotContains(err.Error(), pingdomTestToken)

	limited := newPingdomServer(t, http.StatusTooManyRequests)
	conv = importers.NewPingdomConverter(importers.PingdomOptions{BaseURL: limited.URL})

	_, err = conv.Convert(pingdomTokenBody(t))
	r.ErrorIs(err, importers.ErrPingdomRateLimited)

	failing := newPingdomServer(t, http.StatusInternalServerError)
	conv = importers.NewPingdomConverter(importers.PingdomOptions{BaseURL: failing.URL})

	_, err = conv.Convert(pingdomTokenBody(t))
	r.ErrorIs(err, importers.ErrPingdomAPI)
}

func TestPingdomConverterRequiresTokenOrPayload(t *testing.T) {
	t.Parallel()

	conv := importers.NewPingdomConverter(importers.PingdomOptions{})
	_, err := conv.Convert([]byte(`{"token":""}`))
	require.ErrorIs(t, err, importers.ErrPingdomTokenRequired)
}

func TestConvertEndpointPingdomIsIdempotentAndManaged(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := newPingdomServer(t, 0)
	harness := newConvertHarnessWithPingdom(t, "", srv.URL)

	// runSourceRoundTrip covers dry-run, apply and the idempotent re-apply.
	result := runSourceRoundTrip(t, harness, "pingdom", pingdomTokenBody(t))
	r.Equal("pingdom", result.Manifest)
	r.Equal(10, result.Created)
}

func TestConvertEndpointPingdomBadTokenIsAValidationError(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := newPingdomServer(t, http.StatusUnauthorized)
	harness := newConvertHarnessWithPingdom(t, "", srv.URL)

	rec := harness.post(t, "pingdom", true, pingdomTokenBody(t))
	r.Equal(http.StatusBadRequest, rec.Code)
	r.Contains(rec.Body.String(), "VALIDATION_ERROR")
	r.NotContains(rec.Body.String(), pingdomTestToken)
	r.Equal(0, harness.countChecks(t))
}
