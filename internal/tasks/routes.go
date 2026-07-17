package tasks

import (
	"visualink/internal/notification"
	"visualink/internal/platform/auth"
	"visualink/internal/platform/upload"

	"github.com/go-chi/chi/v5"
)

// Deps 是本模块 handler 的依赖束:自己的 Repo + 跨域注入
// (用户查询走 auth、通知创建走 notification、附件走 upload)。
type Deps struct {
	Repo   *Repo
	Users  *auth.Store
	Notifs *notification.Repo
	Files  *upload.Store
}

// Routes 注册协作系统(功能看板/讨论组/偏好)的路由;挂在登录中间件之内。
func Routes(r chi.Router, d *Deps) {
	r.Get("/dashboard", Dashboard(d))
	r.Get("/stats", GetStats(d))

	r.Get("/features/mine", Mine(d))
	r.Get("/features/new", FeatureForm(d))
	r.Get("/features/submit", FeatureSubmitPage(d))
	r.Get("/features", ListFeatures(d))
	r.Post("/features", CreateFeature(d))
	r.Get("/features/{id}", FeatureDetail(d))
	r.Get("/features/{id}/edit", DraftEditForm(d))
	r.Post("/features/{id}/edit", UpdateDraft(d))
	r.Get("/features/{id}/modify", ModifyContentForm(d))
	r.Post("/features/{id}/modify", UpdateFeatureContent(d))
	r.Get("/features/{id}/row", GetFeatureRow(d))
	r.Get("/features/{id}/comments", GetComments(d))
	r.Delete("/features/{id}", RetractFeature(d))
	r.Patch("/features/{id}/status", UpdateStatus(d))
	r.Post("/features/{id}/archive", ArchiveFeature(d))
	r.Post("/features/{id}/comments", AddComment(d))
	r.Delete("/features/{id}/comments/{commentID}", DeleteComment(d))
	r.Post("/features/{id}/watch", WatchFeature(d))
	r.Delete("/features/{id}/watch", UnwatchFeature(d))

	r.Get("/preferences", Preferences(d))

	r.Get("/groups", ListGroups(d))
	r.Post("/groups", CreateGroup(d))
	r.Get("/groups/{id}", GroupDetail(d))
	r.Post("/groups/{id}/join", JoinGroup(d))
	r.Delete("/groups/{id}/join", LeaveGroup(d))
	r.Post("/groups/{id}/watch", WatchGroup(d))
	r.Delete("/groups/{id}/watch", UnwatchGroup(d))
	r.Post("/groups/{id}/members", AddGroupMember(d))
	r.Delete("/groups/{id}/members/{uid}", RemoveGroupMember(d))
}
