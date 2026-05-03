package checkwebsocket

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// GetSampleConfigs returns sample WebSocket check configurations.
func (c *WebSocketChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "WebSocket: echo.websocket.org",
			Slug:   "ws-websocket",
			Period: 5 * time.Minute,
			Config: (&WebSocketConfig{
				URL:    "wss://echo.websocket.org",
				Send:   "hello",
				Expect: "hello",
			}).GetConfig(),
		},
	}
}
