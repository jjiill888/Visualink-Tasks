package im

import (
	"visualink/internal/notification"
	"visualink/internal/platform/auth"

	"github.com/go-chi/chi/v5"
)

// Deps 是本模块 handler 的依赖束:自己的 Repo + 用户查询(auth)+
// 通知读取(notification,IM 通知视图消费同一数据)。
type Deps struct {
	Repo   *Repo
	Users  *auth.Store
	Notifs *notification.Repo
}

// Routes 注册 IM(频道/私信/WebRTC 信令)路由;挂在登录中间件之内。
func Routes(r chi.Router, d *Deps) {
	r.Get("/im", IMHome(d))
	r.Get("/im/sidebar", IMSidebar(d))
	r.Get("/im/notifications", IMNotifications(d))
	r.Get("/im/channels/new", IMNewChannelForm(d))
	r.Post("/im/channels", CreateIMChannel(d))
	r.Get("/im/c/{id}", IMChannel(d))
	r.Get("/im/c/{id}/messages", GetIMMessages(d))
	r.Get("/im/c/{id}/messages/new", GetNewIMMessages(d))
	r.Post("/im/c/{id}/messages", SendIMMessage(d))
	r.Post("/im/c/{id}/join", JoinIMChannel(d))
	r.Delete("/im/c/{id}/join", LeaveIMChannel(d))
	// DM 路由——读写 direct_messages 表(同 Tasks 消息中心)
	r.Get("/im/dm/{userID}", IMDMView(d))
	r.Post("/im/dm/{userID}/messages", SendIMDM(d))
	r.Get("/im/dm/{userID}/messages/new", GetNewIMDMMessages(d))
	// WebRTC 信令中继
	r.Post("/im/call/signal", CallSignal(d))
}
