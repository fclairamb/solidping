package jmap_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/jmap"
)

// methodFixtureServer is a tiny fake JMAP backend used by the methods tests.
// It serves a session document and returns canned method responses based on
// the first method name in the request.
type methodFixtureServer struct {
	t          *testing.T
	srv        *httptest.Server
	sessionURL string
	responses  map[string]string // method name -> JSON body for the [name, args, "c0"] tuple
	captured   []byte
}

func newMethodFixtureServer(t *testing.T, responses map[string]string) *methodFixtureServer {
	t.Helper()

	f := &methodFixtureServer{t: t, responses: responses}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/.well-known/jmap":
			w.Header().Set("Content-Type", "application/json")
			payload := strings.ReplaceAll(`{
				"capabilities":{"urn:ietf:params:jmap:core":{},"urn:ietf:params:jmap:mail":{}},
				"accounts":{"acc-1":{"name":"x","isPersonal":true}},
				"primaryAccounts":{"urn:ietf:params:jmap:mail":"acc-1"},
				"apiUrl":"%s/jmap","downloadUrl":"%s/dl","uploadUrl":"%s/ul",
				"eventSourceUrl":"%s/events","state":"s"}`, "%s", f.srv.URL)
			_, _ = w.Write([]byte(payload))
		case "/jmap":
			body, _ := io.ReadAll(req.Body)
			f.captured = body

			var env struct {
				MethodCalls []struct {
					Name string `json:"-"`
				} `json:"methodCalls"`
			}
			// JSON tuple parsing is awkward; fall back to inspecting the raw bytes.
			method := ""
			for k := range f.responses {
				if strings.Contains(string(body), `"`+k+`"`) {
					method = k

					break
				}
			}
			_ = env
			body2, ok := f.responses[method]
			if !ok {
				w.WriteHeader(http.StatusNotFound)

				return
			}

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"methodResponses":[` + body2 + `],"sessionState":"s"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	f.sessionURL = f.srv.URL + "/.well-known/jmap"

	return f
}

func (f *methodFixtureServer) close() { f.srv.Close() }

func (f *methodFixtureServer) client(t *testing.T) *jmap.Client {
	t.Helper()

	c := jmap.NewClient(&jmap.Config{
		SessionURL: f.sessionURL,
		Username:   "u",
		Password:   "p",
	})
	_, err := c.DiscoverSession(context.Background())
	require.NoError(t, err)

	return c
}

func TestMailboxQuery(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	srv := newMethodFixtureServer(t, map[string]string{
		"Mailbox/query": `["Mailbox/query",{"accountId":"acc-1","ids":["m1","m2","m3"]},"c0"]`,
	})
	defer srv.close()

	ids, err := srv.client(t).MailboxQuery(context.Background(), "acc-1")
	r.NoError(err)
	r.Equal([]string{"m1", "m2", "m3"}, ids)
}

func TestFindMailboxByName(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	srv := newMethodFixtureServer(t, map[string]string{
		"Mailbox/get": `["Mailbox/get",{"list":[{"id":"m1","name":"Inbox"},{"id":"m2","name":"Processed"}]},"c0"]`,
	})
	defer srv.close()

	mb, err := srv.client(t).FindMailboxByName(context.Background(), "acc-1", "Processed")
	r.NoError(err)
	r.NotNil(mb)
	r.Equal("m2", mb.ID)

	_, err = srv.client(t).FindMailboxByName(context.Background(), "acc-1", "NoSuch")
	r.ErrorIs(err, jmap.ErrMailboxNotFound)
}

func TestFindMailboxByRole(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	resp := `["Mailbox/get",{"list":[` +
		`{"id":"m1","name":"Inbox","role":"inbox"},` +
		`{"id":"m9","name":"Trash","role":"trash"}` +
		`]},"c0"]`
	srv := newMethodFixtureServer(t, map[string]string{
		"Mailbox/get": resp,
	})
	defer srv.close()

	mb, err := srv.client(t).FindMailboxByRole(context.Background(), "acc-1", jmap.MailboxRoleTrash)
	r.NoError(err)
	r.NotNil(mb)
	r.Equal("m9", mb.ID)
}

func TestFindOrCreateMailboxCreatesWhenMissing(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	calls := 0
	getResp := `["Mailbox/get",{"list":[]},"c0"]`
	setResp := `["Mailbox/set",{"created":{"new":{"id":"m-new"}}},"c0"]`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/.well-known/jmap":
			w.Header().Set("Content-Type", "application/json")
			payload := `{"capabilities":{` +
				`"urn:ietf:params:jmap:core":{},` +
				`"urn:ietf:params:jmap:mail":{}},` +
				`"accounts":{"a":{"name":"x","isPersonal":true}},` +
				`"primaryAccounts":{"urn:ietf:params:jmap:mail":"a"},` +
				`"apiUrl":"http://` + req.Host + `/jmap","state":"s"}`
			_, _ = w.Write([]byte(payload))
		case "/jmap":
			body, _ := io.ReadAll(req.Body)
			calls++
			w.Header().Set("Content-Type", "application/json")

			if strings.Contains(string(body), "Mailbox/get") {
				_, _ = w.Write([]byte(`{"methodResponses":[` + getResp + `],"sessionState":"s"}`))

				return
			}

			_, _ = w.Write([]byte(`{"methodResponses":[` + setResp + `],"sessionState":"s"}`))
		}
	}))
	defer srv.Close()

	c := jmap.NewClient(&jmap.Config{
		SessionURL: srv.URL + "/.well-known/jmap",
		Username:   "u",
		Password:   "p",
	})
	_, err := c.DiscoverSession(context.Background())
	r.NoError(err)

	mb, err := c.FindOrCreateMailbox(context.Background(), "a", "Processed")
	r.NoError(err)
	r.NotNil(mb)
	r.Equal("m-new", mb.ID)
	r.Equal("Processed", mb.Name)
	r.Equal(2, calls) // get + set
}

func TestEmailQueryEncodesFilter(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	srv := newMethodFixtureServer(t, map[string]string{
		"Email/query": `["Email/query",{"ids":["e1","e2"]},"c0"]`,
	})
	defer srv.close()

	ids, err := srv.client(t).EmailQuery(context.Background(), "acc-1", jmap.EmailQueryFilter{
		InMailbox: "m1",
		Before:    "2026-01-01T00:00:00Z",
	})
	r.NoError(err)
	r.Equal([]string{"e1", "e2"}, ids)

	var env struct {
		MethodCalls []json.RawMessage `json:"methodCalls"`
	}
	r.NoError(json.Unmarshal(srv.captured, &env))
	r.Contains(string(env.MethodCalls[0]), `"inMailbox":"m1"`)
	r.Contains(string(env.MethodCalls[0]), `"before":"2026-01-01T00:00:00Z"`)
}

func TestEmailGetSkipsWhenNoIDs(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	c := jmap.NewClient(&jmap.Config{Username: "u", Password: "p"})
	emails, err := c.EmailGet(context.Background(), "acc", nil, nil)
	r.NoError(err)
	r.Empty(emails)
}

func TestEmailSetMailboxBuildsPatch(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	srv := newMethodFixtureServer(t, map[string]string{
		"Email/set": `["Email/set",{"updated":{"e1":null}},"c0"]`,
	})
	defer srv.close()

	c := srv.client(t)
	r.NoError(c.EmailSetMailbox(context.Background(), "acc-1", []string{"e1"}, "m-from", "m-to"))

	body := string(srv.captured)
	r.Contains(body, `"mailboxIds/m-to":true`)
	r.Contains(body, `"mailboxIds/m-from":null`)
}

func TestEmailDestroy(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	srv := newMethodFixtureServer(t, map[string]string{
		"Email/set": `["Email/set",{"destroyed":["e1","e2"]},"c0"]`,
	})
	defer srv.close()

	r.NoError(srv.client(t).EmailDestroy(context.Background(), "acc-1", []string{"e1", "e2"}))
	r.Contains(string(srv.captured), `"destroy":["e1","e2"]`)
}
