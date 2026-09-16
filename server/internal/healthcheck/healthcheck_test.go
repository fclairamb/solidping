package healthcheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestURLFromListen(t *testing.T) {
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
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
	}))
	defer srv.Close()

	err := Check(context.Background(), srv.URL+healthPath, 1*time.Millisecond)
	if err == nil {
		t.Fatal("Check() = nil, want timeout error")
	}
}
