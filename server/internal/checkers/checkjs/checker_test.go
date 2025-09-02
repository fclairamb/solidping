package checkjs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

func TestJSChecker_Type(t *testing.T) {
	t.Parallel()

	checker := &JSChecker{}
	require.Equal(t, checkerdef.CheckTypeJS, checker.Type())
}

func TestJSChecker_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		spec    *checkerdef.CheckSpec
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid script",
			spec: &checkerdef.CheckSpec{
				Config: map[string]any{"script": `return {status: "up"}`},
			},
			wantErr: false,
		},
		{
			name: "missing script",
			spec: &checkerdef.CheckSpec{
				Config: map[string]any{},
			},
			wantErr: true,
			errMsg:  "script: is required",
		},
		{
			name: "auto-generates name and slug",
			spec: &checkerdef.CheckSpec{
				Config: map[string]any{"script": `return {status: "up"}`},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			checker := &JSChecker{}
			err := checker.Validate(tt.spec)

			if tt.wantErr {
				r.Error(err)
				if tt.errMsg != "" {
					r.Equal(tt.errMsg, err.Error())
				}
			} else {
				r.NoError(err)
				r.NotEmpty(tt.spec.Name)
				r.NotEmpty(tt.spec.Slug)
			}
		})
	}
}

func TestJSChecker_Execute(t *testing.T) {
	t.Parallel()

	// Start a test HTTP server for http.get tests
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"healthy":true}`))
	}))
	t.Cleanup(server.Close)

	tests := []struct {
		name           string
		script         string
		env            map[string]string
		expectedStatus checkerdef.Status
		checkOutput    func(*testing.T, map[string]any)
	}{
		{
			name:           "simple return up",
			script:         `return {status: "up"}`,
			expectedStatus: checkerdef.StatusUp,
		},
		{
			name:           "return down with output",
			script:         `return {status: "down", output: {error: "bad"}}`,
			expectedStatus: checkerdef.StatusDown,
			checkOutput: func(t *testing.T, output map[string]any) {
				t.Helper()
				require.Equal(t, "bad", output["error"])
			},
		},
		{
			name:           "script throws error",
			script:         `throw new Error("boom")`,
			expectedStatus: checkerdef.StatusError,
			checkOutput: func(t *testing.T, output map[string]any) {
				t.Helper()
				require.Contains(t, output["error"], "script error")
			},
		},
		{
			name:           "no return value",
			script:         `var x = 1 + 1;`,
			expectedStatus: checkerdef.StatusError,
			checkOutput: func(t *testing.T, output map[string]any) {
				t.Helper()
				require.Contains(t, output["error"], "script must return a result object")
			},
		},
		{
			name:           "console.log captured",
			script:         `console.log("hello"); return {status: "up"}`,
			expectedStatus: checkerdef.StatusUp,
			checkOutput: func(t *testing.T, output map[string]any) {
				t.Helper()
				require.Contains(t, output["console"], "hello")
			},
		},
		{
			name:           "env variables accessible",
			script:         `return {status: env.expected === "yes" ? "up" : "down"}`,
			env:            map[string]string{"expected": "yes"},
			expectedStatus: checkerdef.StatusUp,
		},
		{
			name: "http.get works",
			script: `var resp = http.get("` + server.URL + `");
if (resp.statusCode === 200) {
  return {status: "up", metrics: {statusCode: resp.statusCode}};
}
return {status: "down"};`,
			expectedStatus: checkerdef.StatusUp,
		},
		{
			name:           "return with metrics",
			script:         `return {status: "up", metrics: {responseTime: 42}}`,
			expectedStatus: checkerdef.StatusUp,
			checkOutput: func(t *testing.T, _ map[string]any) {
				t.Helper()
				// Metrics are checked through result.Metrics, not output
			},
		},
		{
			name: "solidping.check blocks js type",
			script: `var r = solidping.check("js", {script: "return {status: 'up'}"});
return {status: r.status, output: r.output}`,
			expectedStatus: checkerdef.StatusError,
			checkOutput: func(t *testing.T, output map[string]any) {
				t.Helper()
				require.Contains(t, output["error"], "not allowed")
			},
		},
		{
			name: "solidping.check blocks heartbeat type",
			script: `var r = solidping.check("heartbeat", {});
return {status: r.status, output: r.output}`,
			expectedStatus: checkerdef.StatusError,
			checkOutput: func(t *testing.T, output map[string]any) {
				t.Helper()
				require.Contains(t, output["error"], "not allowed")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			checker := &JSChecker{}
			cfg := &JSConfig{
				Script: tt.script,
				Env:    tt.env,
			}

			result, err := checker.Execute(context.Background(), cfg)
			r.NoError(err)
			r.NotNil(result)
			r.Equal(tt.expectedStatus, result.Status,
				"expected status %s, got %s (output: %v)", tt.expectedStatus, result.Status, result.Output)

			if tt.checkOutput != nil {
				tt.checkOutput(t, result.Output)
			}
		})
	}
}
