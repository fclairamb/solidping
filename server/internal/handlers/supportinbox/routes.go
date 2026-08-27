package supportinbox

import (
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/middleware"
)

// RoutePrefix is where the support inbox lives under the API group.
const RoutePrefix = "/support"

// RegisterRoutes wires every support-inbox endpoint behind the SuperAdmin gate.
//
// The gate lives HERE, in one function, rather than inline in server.go's
// 1500-line route table. Two reasons, and the second is the important one:
//
//  1. Exactly one place decides who may read the instance's inbound human
//     messages. These endpoints expose every message anyone has ever sent us on
//     any channel, from senders who are frequently strangers.
//  2. A test can drive the REAL registration instead of a copy of it. A test
//     that rebuilt this group itself would keep passing after a refactor
//     dropped the middleware here — which is precisely the regression worth
//     catching, because the dash0 route is unlinked and nothing else would
//     notice. See TestSupportRoutesRequireSuperAdmin.
func RegisterRoutes(api *httpx.Group, authMW *middleware.AuthMiddleware, handler *Handler) {
	group := api.NewGroup(RoutePrefix).
		Use(authMW.RequireAuth).
		Use(authMW.RequireSuperAdmin)

	group.GET("/threads", handler.ListThreads)
	group.GET("/threads/:uid", handler.GetThread)
	group.PATCH("/threads/:uid", handler.UpdateThread)
	group.GET("/threads/:uid/messages", handler.ListMessages)
	group.POST("/threads/:uid/messages", handler.CreateMessage)
	group.POST("/threads/:uid/messages/:messageUid/resend", handler.ResendMessage)
}
