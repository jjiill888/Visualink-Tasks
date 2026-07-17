package notification

import (
	"visualink/internal/platform/auth"

	"github.com/go-chi/chi/v5"
)

// Deps 是本模块 handler 的依赖束:自己的 Repo + 用户查询(auth)。
type Deps struct {
	Repo  *Repo
	Users *auth.Store
}

// Routes 注册通知与站内消息路由;挂在登录中间件之内。
// (将来 IM 重做时消息中心并入 IM 更名「通知中心」,本模块是承接点。)
func Routes(r chi.Router, d *Deps) {
	r.Get("/notifications/count", GetNotificationBadge(d))
	r.Get("/notifications", GetNotificationList(d))
	r.Post("/notifications/read", MarkNotificationsRead(d))
	r.Post("/notifications/read-all", MarkAllNotificationsRead(d))

	r.Get("/messages/count", GetMessageBadge(d))
	r.Get("/messages/preview", GetMessagePreview(d))
	r.Get("/messages/center", GetMessageCenter(d))
	r.Post("/messages/send", SendMessage(d))
}
