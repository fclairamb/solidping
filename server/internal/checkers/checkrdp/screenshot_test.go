package checkrdp

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkbrowser"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

func executeWithOutcome(
	ctx context.Context, t *testing.T, cfg *RDPConfig, outcome *authRunOutcome,
) *checkerdef.Result {
	t.Helper()

	checker := &RDPChecker{
		preDialedConn: func(context.Context, *RDPConfig) (net.Conn, error) {
			client, server := net.Pipe()
			t.Cleanup(func() { _ = server.Close() })

			return client, nil
		},
		authSession: func(context.Context, *RDPConfig, net.Conn) (*authRunOutcome, error) {
			return outcome, nil
		},
	}

	result, err := checker.Execute(ctx, cfg)
	require.NoError(t, err)

	return result
}

func TestScreenshotDiagnostics(t *testing.T) {
	t.Parallel()

	base := func() *RDPConfig {
		return &RDPConfig{Host: "rdp.acme.com", Username: "alice", Password: "secret"}
	}
	shot := []byte("\x89PNG-fake")

	tests := []struct {
		name      string
		ctx       context.Context //nolint:containedctx // test table
		cfg       func() *RDPConfig
		outcome   *authRunOutcome
		wantImage bool
		wantError string
	}{
		{
			name: "opted in keeps the capture", ctx: context.Background(),
			cfg:     func() *RDPConfig { c := base(); c.Screenshot = true; return c },
			outcome: &authRunOutcome{screenshot: shot}, wantImage: true,
		},
		{
			name: "not opted in and not forced takes nothing", ctx: context.Background(),
			cfg: base, outcome: &authRunOutcome{screenshot: shot},
		},
		{
			name: "forced capture keeps it without opt-in", ctx: checkerdef.WithForcedCapture(context.Background()),
			cfg: base, outcome: &authRunOutcome{screenshot: shot}, wantImage: true,
		},
		{
			name: "capture error is reported, verdict untouched", ctx: checkerdef.WithForcedCapture(context.Background()),
			cfg: base, outcome: &authRunOutcome{shotError: "no frame"}, wantError: "no frame",
		},
		{
			name: "empty capture is reported", ctx: checkerdef.WithForcedCapture(context.Background()),
			cfg: base, outcome: &authRunOutcome{}, wantError: "the capture returned an empty image",
		},
		{
			name: "over-cap capture is dropped", ctx: checkerdef.WithForcedCapture(context.Background()),
			cfg:       base,
			outcome:   &authRunOutcome{screenshot: make([]byte, checkbrowser.MaxScreenshotBytes+1)},
			wantError: checkbrowser.OverCapMessage(checkbrowser.MaxScreenshotBytes + 1),
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			result := executeWithOutcome(testCase.ctx, t, testCase.cfg(), testCase.outcome)
			require.Equal(t, checkerdef.StatusUp, result.Status)

			if !testCase.wantImage && testCase.wantError == "" {
				require.Nil(t, result.Diagnostics)

				return
			}

			require.NotNil(t, result.Diagnostics)
			require.Equal(t, testCase.wantError, result.Diagnostics.ScreenshotError)

			if testCase.wantImage {
				require.Equal(t, shot, result.Diagnostics.Screenshot.Image)
				require.Equal(t, checkerdef.ImageFormatPNG, result.Diagnostics.Screenshot.Format)
			} else {
				require.Nil(t, result.Diagnostics.Screenshot)
			}
		})
	}
}
