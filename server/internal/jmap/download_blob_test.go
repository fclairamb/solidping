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
