package im

import (
	"visualink/internal/db"

	"github.com/go-chi/chi/v5"
)

// Routes 注册 IM(频道/私信/WebRTC 信令)路由;挂在登录中间件之内。
func Routes(r chi.Router, database *db.DB) {
	r.Get("/im", IMHome(database))
	r.Get("/im/sidebar", IMSidebar(database))
	r.Get("/im/notifications", IMNotifications(database))
	r.Get("/im/channels/new", IMNewChannelForm(database))
	r.Post("/im/channels", CreateIMChannel(database))
	r.Get("/im/c/{id}", IMChannel(database))
	r.Get("/im/c/{id}/messages", GetIMMessages(database))
	r.Get("/im/c/{id}/messages/new", GetNewIMMessages(database))
	r.Post("/im/c/{id}/messages", SendIMMessage(database))
	r.Post("/im/c/{id}/join", JoinIMChannel(database))
	r.Delete("/im/c/{id}/join", LeaveIMChannel(database))
	// DM 路由——读写 direct_messages 表(同 Tasks 消息中心)
	r.Get("/im/dm/{userID}", IMDMView(database))
	r.Post("/im/dm/{userID}/messages", SendIMDM(database))
	r.Get("/im/dm/{userID}/messages/new", GetNewIMDMMessages(database))
	// WebRTC 信令中继
	r.Post("/im/call/signal", CallSignal(database))
}
