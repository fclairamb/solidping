package plugins

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fclairamb/solidping/server/internal/plugins/protocol"
)

// helperEnv selects what the re-executed test binary does instead of running
// the tests: the helper-process pattern, so the exec and nagios tests need no
// external interpreter.
const helperEnv = "SP_PLUGIN_TEST_HELPER"

func TestMain(m *testing.M) {
	if mode := os.Getenv(helperEnv); mode != "" {
		os.Exit(runHelper(mode))
	}

	os.Exit(m.Run())
}

// helperEnvFor is the environment of a helper process in the given mode.
func helperEnvFor(dir, mode string) []string {
	return append(baseEnv(dir), helperEnv+"="+mode)
}

//nolint:forbidigo // the helper process talks over stdout on purpose
func runHelper(mode string) int {
	switch {
	case strings.HasPrefix(mode, "nagios-exit-"):
		code := 0
		_, _ = fmt.Sscanf(strings.TrimPrefix(mode, "nagios-exit-"), "%d", &code)
		fmt.Printf("STATUS %d - args=%s | time=0.25s;1;2;0;10 'disk used'=42%%;80;90 bad=abc\n",
			code, strings.Join(os.Args[1:], ","))
		fmt.Println("second line | extra=7")

		return code
	case mode == "nagios-hang":
		// Fork a child that also hangs, to prove the whole group is killed.
		child := exec.Command(os.Args[0]) //nolint:noctx // re-exec of the test binary
		child.Env = append(os.Environ()[:0:0], helperEnv+"=sleep")
		_ = child.Start()

		if child.Process != nil {
			_ = os.WriteFile(os.Getenv("SP_PLUGIN_TEST_MARKER"), []byte(strconv.Itoa(child.Process.Pid)), 0o600)
		}

		time.Sleep(time.Minute)

		return 0
	case mode == "sleep":
		time.Sleep(time.Minute)

		return 0
	default:
		return runExecHelper(mode)
	}
}

// runExecHelper is a JSON-RPC exec plugin. Modes: "exec" answers,
// "exec-crash-once" exits on the first request it ever gets, "exec-slow"
// never answers execute.
//
//nolint:forbidigo // the helper process talks over stdout on purpose
func runExecHelper(mode string) int {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}

		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			return 3
		}

		fmt.Fprintln(os.Stderr, "got", req.Method)

		switch mode {
		case "exec-crash-once":
			// Crash on the first request of the first process only.
			marker := os.Getenv("SP_PLUGIN_TEST_MARKER")
			if _, err := os.Stat(marker); err != nil {
				_ = os.WriteFile(marker, []byte("crashed"), 0o600)

				return 2
			}
		case "exec-slow":
			if req.Method == protocol.MethodExecute {
				time.Sleep(time.Minute)
			}
		}

		var result any

		switch req.Method {
		case protocol.MethodExecute:
			var params protocol.ExecuteParams
			_ = json.Unmarshal(req.Params, &params)
			result = protocol.ExecuteResult{
				Status:  "up",
				Output:  map[string]any{"message": "hello " + fmt.Sprint(params.Config["host"]), "type": params.Type},
				Metrics: map[string]any{"pid": float64(os.Getpid())},
			}
		case protocol.MethodValidate:
			var params protocol.ValidateParams
			_ = json.Unmarshal(req.Params, &params)

			res := protocol.ValidateResult{}
			if params.Config["host"] == "forbidden.acme.com" {
				res.Errors = []protocol.ValidationError{{Field: "host", Message: "is not allowed"}}
			}

			result = res
		}

		raw, err := json.Marshal(result)
		if err != nil {
			return 4
		}

		line, err := json.Marshal(protocol.Response{JSONRPC: protocol.JSONRPCVersion, ID: req.ID, Result: raw})
		if err != nil {
			return 4
		}
		fmt.Println(string(line))
	}

	return 0
}
