package jmap_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/jmap"
)

func newBlobServer(t *testing.T, blob func(w http.ResponseWriter, r *http.Request)) *jmap.Client {
	t.Helper()

	var srv *httptest.Server

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/jmap", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"capabilities":    map[string]any{},
			"primaryAccounts": map[string]string{jmap.CapabilityMail: "acc 1"},
			"apiUrl":          srv.URL + "/api",
			"downloadUrl":     srv.URL + "/dl/{accountId}/{blobId}/{name}?accept={type}",
		})
	})
	mux.HandleFunc("/dl/", blob)

	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := jmap.NewClient(&jmap.Config{SessionURL: srv.URL + "/.well-known/jmap", Username: "alice"})
	_, err := client.DiscoverSession(context.Background())
	require.NoError(t, err)

	return client
}

func TestDownloadBlob_TemplatesURL(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var gotPath, gotQuery string

	client := newBlobServer(t, func(w http.ResponseWriter, req *http.Request) {
		gotPath = req.URL.EscapedPath()
		gotQuery = req.URL.RawQuery

		_, _ = w.Write([]byte("raw message"))
	})

	body, err := client.DownloadBlob(context.Background(), "acc 1", "blob/9")
	r.NoError(err)
	r.Equal("raw message", string(body))
	r.Equal("/dl/acc%201/blob%2F9/message.eml", gotPath)
	r.Equal("accept=message%2Frfc822", gotQuery)
}

func TestDownloadBlob_SizeCap(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	chunk := bytes.Repeat([]byte("x"), 1<<20)
	client := newBlobServer(t, func(w http.ResponseWriter, _ *http.Request) {
		for range 12 {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	})

	_, err := client.DownloadBlob(context.Background(), "a", "b")
	r.ErrorIs(err, jmap.ErrResponseTooLarge)
}

func TestDownloadBlob_BadStatusAndNoSession(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	client := newBlobServer(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	})

	_, err := client.DownloadBlob(context.Background(), "a", "b")
	r.ErrorIs(err, jmap.ErrUnexpectedStatus)

	bare := jmap.NewClient(&jmap.Config{})
	_, err = bare.DownloadBlob(context.Background(), "a", "b")
	r.ErrorIs(err, jmap.ErrNoDownloadURL)
}

func sessionClient(t *testing.T, downloadURL func(base string) string) (*jmap.Client, *httptest.Server) {
	t.Helper()

	var srv *httptest.Server

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/jmap", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"capabilities":    map[string]any{},
			"primaryAccounts": map[string]string{jmap.CapabilityMail: "acc"},
			"apiUrl":          srv.URL + "/api",
			"downloadUrl":     downloadURL(srv.URL),
		})
	})
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, _ *http.Request) {
		// Promise more bytes than are sent, then hang up: a truncated body.
		w.Header().Set("Content-Length", "1000")
		_, _ = w.Write([]byte("short"))
	})

	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := jmap.NewClient(&jmap.Config{SessionURL: srv.URL + "/.well-known/jmap", Username: "alice"})
	_, err := client.DiscoverSession(context.Background())
	require.NoError(t, err)

	return client, srv
}

func TestDownloadBlob_InvalidTemplateURL(t *testing.T) {
	t.Parallel()

	client, _ := sessionClient(t, func(base string) string { return base + "/dl/%zz/{blobId}" })

	_, err := client.DownloadBlob(context.Background(), "a", "b")
	require.ErrorContains(t, err, "build request")
}

func TestDownloadBlob_TruncatedBody(t *testing.T) {
	t.Parallel()

	client, _ := sessionClient(t, func(base string) string { return base + "/dl/{accountId}/{blobId}" })

	_, err := client.DownloadBlob(context.Background(), "a", "b")
	require.ErrorContains(t, err, "read blob")
}

func TestDownloadBlob_TransportError(t *testing.T) {
	t.Parallel()

	client, srv := sessionClient(t, func(base string) string { return base + "/dl/{accountId}/{blobId}" })
	srv.Close()

	_, err := client.DownloadBlob(context.Background(), "a", "b")
	require.ErrorContains(t, err, "download blob")
}
