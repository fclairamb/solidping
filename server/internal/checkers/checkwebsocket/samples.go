package checkwebsocket

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// echoMessage is the default send/expect payload used by the sample config and unit tests.
const echoMessage = "hello"

// GetSampleConfigs returns sample WebSocket check configurations.
func (c *WebSocketChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "WebSocket: echo.websocket.org",
			Slug:   "ws-websocket",
			Period: 5 * time.Minute,
			Config: (&WebSocketConfig{
				URL:    "wss://echo.websocket.org",
				Send:   echoMessage,
				Expect: echoMessage,
			}).GetConfig(),
		},
	}
}
