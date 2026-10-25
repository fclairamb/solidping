package integration

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/files"
	"github.com/fclairamb/solidping/server/internal/handlers/files/signedurl"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage"
)

func getStatus(t *testing.T, url string) (int, []byte) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return resp.StatusCode, body
}

func TestPublicFileGet_SignedURL(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	tmpDir := t.TempDir()
	ts := NewTestServerWithConfig(t, func(c *config.Config) { c.FileStorage.LocalRoot = tmpDir })
	ctx := t.Context()

	content := []byte("public body")
	fileSvc := files.NewService(ts.Server.DBService(), &config.Config{
		FileStorage: config.FileStorageConfig{LocalRoot: tmpDir},
	})
	seeded, err := fileSvc.CreateFile(ctx, uuid.MustParse(cliCovOrgUID), filestorage.GroupTypeReports,
		"pub.txt", "text/plain", nil, bytes.NewReader(content), int64(len(content)))
	r.NoError(err)

	secret := []byte("test-secret-key-for-integration-tests")
	fileUID := uuid.MustParse(seeded.UID)
	base := ts.HTTPServer.URL + "/pub/files/"

	exp, sig := signedurl.Sign(secret, fileUID, time.Hour)
	expStr := strconv.FormatInt(exp, 10)

	status, body := getStatus(t, base+seeded.UID+"?exp="+expStr+"&sig="+sig)
	r.Equal(http.StatusOK, status)
	r.Equal(content, body)

	status, _ = getStatus(t, base+"not-a-uuid?exp="+expStr+"&sig="+sig)
	r.Equal(http.StatusNotFound, status)

	status, _ = getStatus(t, base+seeded.UID+"?exp=abc&sig="+sig)
	r.Equal(http.StatusForbidden, status)

	status, _ = getStatus(t, base+seeded.UID+"?exp="+expStr+"&sig=deadbeef")
	r.Equal(http.StatusForbidden, status)

	pastExp, pastSig := signedurl.Sign(secret, fileUID, -time.Hour)
	status, _ = getStatus(t, base+seeded.UID+"?exp="+strconv.FormatInt(pastExp, 10)+"&sig="+pastSig)
	r.Equal(http.StatusGone, status)

	unknown := uuid.New()
	uExp, uSig := signedurl.Sign(secret, unknown, time.Hour)
	status, _ = getStatus(t, base+unknown.String()+"?exp="+strconv.FormatInt(uExp, 10)+"&sig="+uSig)
	r.Equal(http.StatusNotFound, status)
}

func postReport(t *testing.T, ts *TestServer, fields map[string]string, screenshot []byte) int {
	t.Helper()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	for k, v := range fields {
		require.NoError(t, mw.WriteField(k, v))
	}

	if screenshot != nil {
		part, err := mw.CreateFormFile("screenshot", "shot.png")
		require.NoError(t, err)
		_, err = part.Write(screenshot)
		require.NoError(t, err)
	}

	require.NoError(t, mw.Close())

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		ts.HTTPServer.URL+"/api/mgmt/report", &buf)
	require.NoError(t, err)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer not-a-valid-token")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	return resp.StatusCode
}

func TestSubmitReport_Endpoint(t *testing.T) {
	t.Parallel()

	disabled := NewTestServer(t)
	require.Equal(t, http.StatusNotFound, postReport(t, disabled, map[string]string{"url": "https://acme.com"}, nil))

	ts := NewTestServerWithConfig(t, func(c *config.Config) {
		c.App.EnableBugReport = true
		c.FileStorage.LocalRoot = t.TempDir()
	})

	require.Equal(t, http.StatusBadRequest, postReport(t, ts, map[string]string{"comment": "x"}, nil))
	require.Equal(t, http.StatusNotFound,
		postReport(t, ts, map[string]string{"url": "https://acme.com", "org": "nope"}, nil))
	require.Equal(t, http.StatusCreated, postReport(t, ts, map[string]string{
		"url": "https://acme.com", "org": TestOrgSlug, "comment": "broken", "context": `{"a":1}`,
	}, []byte("png-bytes")))
}
