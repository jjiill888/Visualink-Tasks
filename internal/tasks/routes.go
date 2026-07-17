package tasks

import (
	"visualink/internal/db"

	"github.com/go-chi/chi/v5"
)

// Routes 注册协作系统(功能看板/讨论组/偏好)的路由;挂在登录中间件之内。
func Routes(r chi.Router, database *db.DB) {
	r.Get("/dashboard", Dashboard(database))
	r.Get("/stats", GetStats(database))

	r.Get("/features/mine", Mine(database))
	r.Get("/features/new", FeatureForm(database))
	r.Get("/features/submit", FeatureSubmitPage(database))
	r.Get("/features", ListFeatures(database))
	r.Post("/features", CreateFeature(database))
	r.Get("/features/{id}", FeatureDetail(database))
	r.Get("/features/{id}/edit", DraftEditForm(database))
	r.Post("/features/{id}/edit", UpdateDraft(database))
	r.Get("/features/{id}/modify", ModifyContentForm(database))
	r.Post("/features/{id}/modify", UpdateFeatureContent(database))
	r.Get("/features/{id}/row", GetFeatureRow(database))
	r.Get("/features/{id}/comments", GetComments(database))
	r.Delete("/features/{id}", RetractFeature(database))
	r.Patch("/features/{id}/status", UpdateStatus(database))
	r.Post("/features/{id}/archive", ArchiveFeature(database))
	r.Post("/features/{id}/comments", AddComment(database))
	r.Delete("/features/{id}/comments/{commentID}", DeleteComment(database))
	r.Post("/features/{id}/watch", WatchFeature(database))
	r.Delete("/features/{id}/watch", UnwatchFeature(database))

	r.Get("/preferences", Preferences(database))

	r.Get("/groups", ListGroups(database))
	r.Post("/groups", CreateGroup(database))
	r.Get("/groups/{id}", GroupDetail(database))
	r.Post("/groups/{id}/join", JoinGroup(database))
	r.Delete("/groups/{id}/join", LeaveGroup(database))
	r.Post("/groups/{id}/watch", WatchGroup(database))
	r.Delete("/groups/{id}/watch", UnwatchGroup(database))
	r.Post("/groups/{id}/members", AddGroupMember(database))
	r.Delete("/groups/{id}/members/{uid}", RemoveGroupMember(database))
}
