package jmap_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/jmap"
)

func TestConfigApplyDefaults(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := jmap.Config{}
	cfg.ApplyDefaults()

	r.Equal(jmap.DefaultMailboxName, cfg.MailboxName)
	r.Equal(jmap.DefaultProcessedMailboxName, cfg.ProcessedMailboxName)
	r.Equal(jmap.DefaultPollIntervalSeconds, cfg.PollIntervalSeconds)
	r.Equal(jmap.DefaultProcessedRetentionDays, cfg.ProcessedRetentionDays)
	r.Equal(jmap.DefaultFailedRetentionDays, cfg.FailedRetentionDays)
}

func TestConfigApplyDefaultsPreservesExplicit(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := jmap.Config{
		MailboxName:            "Custom",
		PollIntervalSeconds:    10,
		ProcessedRetentionDays: 90,
		FailedRetentionDays:    14,
	}
	cfg.ApplyDefaults()

	r.Equal("Custom", cfg.MailboxName)
	r.Equal(10, cfg.PollIntervalSeconds)
	r.Equal(90, cfg.ProcessedRetentionDays)
	r.Equal(14, cfg.FailedRetentionDays)
}

func TestMethodCallMarshalRoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	original := jmap.MethodCall{
		Name:     "Mailbox/query",
		Args:     map[string]any{"accountId": "u1"},
		ClientID: "c0",
	}

	data, err := json.Marshal(original)
	r.NoError(err)
	r.Equal(`["Mailbox/query",{"accountId":"u1"},"c0"]`, string(data))

	var roundTripped jmap.MethodCall

	r.NoError(json.Unmarshal(data, &roundTripped))
	r.Equal(original.Name, roundTripped.Name)
	r.Equal(original.ClientID, roundTripped.ClientID)
	r.Equal("u1", roundTripped.Args["accountId"])
}

func TestMethodResponseUnmarshal(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	raw := []byte(`["Mailbox/query",{"accountId":"u1","ids":["m1","m2"]},"c0"]`)

	var resp jmap.MethodResponse

	r.NoError(json.Unmarshal(raw, &resp))
	r.Equal("Mailbox/query", resp.Name)
	r.Equal("c0", resp.ClientID)

	var args struct {
		AccountID string   `json:"accountId"`
		IDs       []string `json:"ids"`
	}
	r.NoError(json.Unmarshal(resp.Args, &args))
	r.Equal("u1", args.AccountID)
	r.Equal([]string{"m1", "m2"}, args.IDs)
}

const sampleSessionPayload = `{
  "capabilities": {"urn:ietf:params:jmap:core": {}, "urn:ietf:params:jmap:mail": {}},
  "accounts": {"acc-1": {"name": "Inbox", "isPersonal": true}},
  "primaryAccounts": {"urn:ietf:params:jmap:mail": "acc-1"},
  "apiUrl": "%s/jmap",
  "downloadUrl": "%s/download",
  "uploadUrl": "%s/upload",
  "eventSourceUrl": "%s/events",
  "state": "abc"
}`

func TestDiscoverSession(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var srvURL string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		user, _, _ := req.BasicAuth()
		if user != "alice" {
			w.WriteHeader(http.StatusUnauthorized)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(strings.ReplaceAll(sampleSessionPayload, "%s", srvURL)))
	}))
	defer srv.Close()

	srvURL = srv.URL

	client := jmap.NewClient(&jmap.Config{
		SessionURL: srv.URL + "/.well-known/jmap",
		Username:   "alice",
		Password:   "secret",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	session, err := client.DiscoverSession(ctx)
	r.NoError(err)
	r.NotNil(session)
	r.Equal("acc-1", client.AccountID())
	r.Equal(srv.URL+"/events", client.EventSourceURL())
}

func TestDiscoverSessionRejectsMissingMailAccount(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"capabilities":{},"accounts":{},"primaryAccounts":{},"apiUrl":"x","state":"a"}`))
	}))
	defer srv.Close()

	client := jmap.NewClient(&jmap.Config{
		SessionURL: srv.URL,
		Username:   "alice",
		Password:   "secret",
	})

	_, err := client.DiscoverSession(context.Background())
	r.ErrorIs(err, jmap.ErrNoMailAccount)
}

func TestDiscoverSessionAppliesRewriteBase(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
		  "capabilities":{"urn:ietf:params:jmap:core":{},"urn:ietf:params:jmap:mail":{}},
		  "accounts":{"a":{"name":"x","isPersonal":true}},
		  "primaryAccounts":{"urn:ietf:params:jmap:mail":"a"},
		  "apiUrl":"http://internal.example/jmap",
		  "eventSourceUrl":"http://internal.example/events",
		  "downloadUrl":"http://internal.example/dl",
		  "uploadUrl":"http://internal.example/ul",
		  "state":"s"}`))
	}))
	defer srv.Close()

	client := jmap.NewClient(&jmap.Config{
		SessionURL:     srv.URL,
		Username:       "alice",
		Password:       "secret",
		RewriteBaseURL: "https://public.example",
	})

	_, err := client.DiscoverSession(context.Background())
	r.NoError(err)
	r.Equal("https://public.example/events", client.EventSourceURL())
}

func TestCallSendsEnvelopeAndParsesResponse(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var capturedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/.well-known/jmap":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(strings.ReplaceAll(sampleSessionPayload, "%s",
				"http://"+req.Host)))
		case "/jmap":
			body, _ := io.ReadAll(req.Body)
			capturedBody = body
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"methodResponses":[["Mailbox/query",{"ids":["m1"]},"c0"]],"sessionState":"abc"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := jmap.NewClient(&jmap.Config{
		SessionURL: srv.URL + "/.well-known/jmap",
		Username:   "alice",
		Password:   "secret",
	})

	_, err := client.DiscoverSession(context.Background())
	r.NoError(err)

	resp, err := client.Call(context.Background(), []jmap.MethodCall{
		{Name: "Mailbox/query", Args: map[string]any{"accountId": "acc-1"}, ClientID: "c0"},
	})
	r.NoError(err)
	r.Len(resp.MethodResponses, 1)
	r.Equal("Mailbox/query", resp.MethodResponses[0].Name)
	r.Contains(string(capturedBody), `"using":["urn:ietf:params:jmap:core","urn:ietf:params:jmap:mail"]`)
}
