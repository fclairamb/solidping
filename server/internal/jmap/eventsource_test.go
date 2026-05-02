package jmap_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/jmap"
)

func TestExpandEventSourceURL(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, raw, types, want string
	}{
		{
			name:  "all placeholders substituted",
			raw:   "https://api.example.com/jmap/event/?types={types}&closeafter={closeafter}&ping={ping}",
			types: "Email",
			want:  "https://api.example.com/jmap/event/?types=Email&closeafter=no&ping=300",
		},
		{
			name:  "empty types becomes wildcard",
			raw:   "https://api.example.com/jmap/event/?types={types}&closeafter={closeafter}&ping={ping}",
			types: "",
			want:  "https://api.example.com/jmap/event/?types=%2A&closeafter=no&ping=300",
		},
		{
			name:  "no placeholders falls back to query append",
			raw:   "https://api.example.com/events",
			types: "Email",
			want:  "https://api.example.com/events?types=Email",
		},
		{
			name:  "no placeholders preserves existing query",
			raw:   "https://api.example.com/events?token=x",
			types: "Email",
			want:  "https://api.example.com/events?token=x&types=Email",
		},
		{
			name:  "comma-separated types are escaped",
			raw:   "https://api.example.com/jmap/event/?types={types}",
			types: "Email,Mailbox",
			want:  "https://api.example.com/jmap/event/?types=Email%2CMailbox",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			r.Equal(tc.want, jmap.ExpandEventSourceURLForTest(tc.raw, tc.types))
		})
	}
}

func TestListenEventSourceDispatchesStateEvents(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/.well-known/jmap":
			w.Header().Set("Content-Type", "application/json")
			payload := `{"capabilities":{` +
				`"urn:ietf:params:jmap:core":{},` +
				`"urn:ietf:params:jmap:mail":{}},` +
				`"accounts":{"a":{"name":"x","isPersonal":true}},` +
				`"primaryAccounts":{"urn:ietf:params:jmap:mail":"a"},` +
				`"apiUrl":"http://` + req.Host + `/jmap",` +
				`"eventSourceUrl":"http://` + req.Host + `/events?types={types}&closeafter={closeafter}&ping={ping}",` +
				`"state":"s"}`
			_, _ = w.Write([]byte(payload))
		case "/events":
			if strings.ContainsAny(req.URL.RawQuery, "{}") {
				t.Errorf("server received un-expanded URI template: %q", req.URL.RawQuery)
				w.WriteHeader(http.StatusBadRequest)

				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)

			// emit one ping (keepalive) and two state events, then close
			_, _ = w.Write([]byte(": keepalive\n\n"))
			if flusher != nil {
				flusher.Flush()
			}

			_, _ = w.Write([]byte("event: state\ndata: {\"changed\":{}}\n\n"))
			if flusher != nil {
				flusher.Flush()
			}

			_, _ = w.Write([]byte("event: state\ndata: {\"changed\":{\"a\":{\"Email\":\"s2\"}}}\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		default:
			w.WriteHeader(http.StatusNotFound)
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

	var (
		stateCount atomic.Int32
		dataSeen   = make(chan string, 4)
	)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	go func() {
		_ = c.ListenEventSourceWithReconnect(ctx, "state", func(ev jmap.EventSourceEvent) error {
			if ev.Type == "state" {
				stateCount.Add(1)

				select {
				case dataSeen <- ev.Data:
				default:
				}
			}

			return nil
		})
	}()

	// First state event has no real change; second one does.
	for i := 0; i < 2; i++ {
		select {
		case <-dataSeen:
		case <-ctx.Done():
			t.Fatalf("timed out waiting for state events (saw %d)", stateCount.Load())
		}
	}

	r.GreaterOrEqual(stateCount.Load(), int32(2))
}

func TestListenEventSourceReconnects(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var connectCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/.well-known/jmap":
			w.Header().Set("Content-Type", "application/json")
			payload := `{"capabilities":{` +
				`"urn:ietf:params:jmap:core":{},` +
				`"urn:ietf:params:jmap:mail":{}},` +
				`"accounts":{"a":{"name":"x","isPersonal":true}},` +
				`"primaryAccounts":{"urn:ietf:params:jmap:mail":"a"},` +
				`"apiUrl":"http://` + req.Host + `/jmap",` +
				`"eventSourceUrl":"http://` + req.Host + `/events?types={types}&closeafter={closeafter}&ping={ping}",` +
				`"state":"s"}`
			_, _ = w.Write([]byte(payload))
		case "/events":
			if strings.ContainsAny(req.URL.RawQuery, "{}") {
				t.Errorf("server received un-expanded URI template: %q", req.URL.RawQuery)
				w.WriteHeader(http.StatusBadRequest)

				return
			}
			n := connectCount.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			// On first attempt, drop after one event. On second, send another then keep open briefly.
			body := "event: state\ndata: {\"changed\":{\"a\":{\"Email\":\"s" +
				strings.Repeat("x", int(n)) + "\"}}}\n\n"
			_, _ = w.Write([]byte(body))

			if flusher != nil {
				flusher.Flush()
			}
		default:
			w.WriteHeader(http.StatusNotFound)
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

	var stateCount atomic.Int32

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = c.ListenEventSourceWithReconnect(ctx, "state", func(ev jmap.EventSourceEvent) error {
			if ev.Type == "state" {
				if stateCount.Add(1) >= 2 {
					cancel()
				}
			}

			return nil
		})
	}()

	<-done
	r.GreaterOrEqual(stateCount.Load(), int32(2), "expected at least 2 state events across 2 connects")
	r.GreaterOrEqual(connectCount.Load(), int32(2), "expected reconnect")
}
