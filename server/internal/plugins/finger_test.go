package plugins

import (
	"io"
	"net"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/plugins/examples/finger"
)

// The reference x-finger plugin end to end: its handler on httptest, loaded
// through an http manifest, run against a fake Finger server. It is the only
// test that registers x-finger.
func TestFingerPluginEndToEnd(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	keys, env := testKeys(t)

	service := httptest.NewServer(finger.NewHandler(keys, nil))
	t.Cleanup(service.Close)

	countBefore := checkerdef.AdvertisedCheckTypeCount()

	set, runner, _ := loadHTTPForTest(t, httpManifest("finger", service.URL, env))
	r.NotNil(runner)
	r.Empty(set.Errors)
	r.Equal([]checkerdef.CheckType{finger.CheckType}, set.Types)
	r.Equal(countBefore, checkerdef.AdvertisedCheckTypeCount(), "plugin types are never in the tally")

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	r.NoError(err)
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}

		defer func() { _ = conn.Close() }()

		buf := make([]byte, 64)
		_, _ = conn.Read(buf)
		_, _ = io.WriteString(conn, "Login: alice\r\n")
	}()

	addr, ok := listener.Addr().(*net.TCPAddr)
	r.True(ok)

	res := runPluginType(t, finger.CheckType, map[string]any{
		"host": "127.0.0.1", "port": float64(addr.Port), "user": "alice", "timeout": "5s",
	})
	r.Equal(checkerdef.StatusUp, res.Status, res.Output)
	r.Equal("Login: alice", res.Output["message"])
	r.InDelta(1, res.Metrics["lines"], 0)
}
