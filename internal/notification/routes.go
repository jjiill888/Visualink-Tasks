package notification

import (
	"visualink/internal/db"

	"github.com/go-chi/chi/v5"
)

// Routes 注册通知与站内消息路由;挂在登录中间件之内。
// (将来 IM 重做时消息中心并入 IM 更名「通知中心」,本模块是承接点。)
func Routes(r chi.Router, database *db.DB) {
	r.Get("/notifications/count", GetNotificationBadge(database))
	r.Get("/notifications", GetNotificationList(database))
	r.Post("/notifications/read", MarkNotificationsRead(database))
	r.Post("/notifications/read-all", MarkAllNotificationsRead(database))

	r.Get("/messages/count", GetMessageBadge(database))
	r.Get("/messages/preview", GetMessagePreview(database))
	r.Get("/messages/center", GetMessageCenter(database))
	r.Post("/messages/send", SendMessage(database))
}
