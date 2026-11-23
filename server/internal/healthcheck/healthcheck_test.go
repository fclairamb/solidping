package healthcheck

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestURLFromListen(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		":4000":          "http://127.0.0.1:4000" + healthPath,
		"0.0.0.0:4000":   "http://127.0.0.1:4000" + healthPath,
		"localhost:4000": "http://127.0.0.1:4000" + healthPath,
		"127.0.0.1:8080": "http://127.0.0.1:8080" + healthPath,
	}

	for listen, want := range cases {
		if got := URLFromListen(listen); got != want {
			t.Errorf("URLFromListen(%q) = %q, want %q", listen, got, want)
		}
	}
}

func TestCheck_OK(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != healthPath {
			t.Errorf("unexpected path %q", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := Check(context.Background(), srv.URL+healthPath, DefaultTimeout); err != nil {
		t.Fatalf("Check() = %v, want nil", err)
	}
}

func TestCheck_ServiceUnavailable(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	err := Check(context.Background(), srv.URL+healthPath, DefaultTimeout)
	if err == nil {
		t.Fatal("Check() = nil, want error for 503")
	}

	if !strings.Contains(err.Error(), "503") {
		t.Errorf("Check() error = %v, want it to mention 503", err)
	}
}

func TestCheck_ConnectionRefused(t *testing.T) {
	t.Parallel()

	// Nothing listens here — a closed httptest server's URL is a reliable
	// "connection refused" target.
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	url := srv.URL + healthPath
	srv.Close()

	if err := Check(context.Background(), url, DefaultTimeout); err == nil {
		t.Fatal("Check() = nil, want error for connection refused")
	}
}

func TestCheck_Timeout(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
	}))
	defer srv.Close()

	err := Check(context.Background(), srv.URL+healthPath, 1*time.Millisecond)
	if err == nil {
		t.Fatal("Check() = nil, want timeout error")
	}
}

func TestLoopbackAddr(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		":4000":        "127.0.0.1:4000",
		"0.0.0.0:8080": "127.0.0.1:8080",
		"4000":         "127.0.0.1:4000",
	}

	for listen, want := range cases {
		if got := LoopbackAddr(listen); got != want {
			t.Errorf("LoopbackAddr(%q) = %q, want %q", listen, got, want)
		}
	}
}

// TestServe drives the agent-mode health listener through the same Check the
// `solidping healthcheck` subcommand runs (issue #995).
func TestServe(t *testing.T) {
	t.Parallel()

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	var healthy atomic.Bool

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() { done <- Serve(ctx, listener, healthy.Load) }()

	base := "http://" + listener.Addr().String()

	if err := Check(ctx, base+healthPath, DefaultTimeout); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("Check() while disconnected = %v, want a 503 error", err)
	}

	healthy.Store(true)

	if err := Check(ctx, base+healthPath, DefaultTimeout); err != nil {
		t.Fatalf("Check() while connected = %v, want nil", err)
	}

	if err := Check(ctx, base+"/api/v1/orgs", DefaultTimeout); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("Check() on another path = %v, want a 404 error", err)
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve() = %v, want nil after cancel", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve() did not return after ctx cancel")
	}
}
