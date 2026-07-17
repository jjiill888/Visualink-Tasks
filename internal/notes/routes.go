package notes

import (
	"visualink/internal/db"

	"github.com/go-chi/chi/v5"
)

// Routes 注册云笔记路由(文库/编辑页/文档组/权限/历史/附件/协作);
// 挂在登录中间件之内。
func Routes(r chi.Router, database *db.DB) {
	r.Get("/notes", NotesPage(database))
	r.Get("/notes/new", NewNote(database))
	r.Get("/notes/panel", NotesPanel(database)) // chi 静态段优先于 {id}
	// 文档组(侧栏文件树的文件夹,纯组织结构;三端点都返回刷新后的面板片段)
	r.Post("/notes/groups", CreateNoteGroup(database))
	r.Delete("/notes/groups/{gid}", DeleteNoteGroup(database))
	r.Put("/notes/{id}/group", SetNoteGroup(database))
	r.Get("/notes/{id}", NoteEditPage(database))
	r.Put("/notes/{id}", SaveNote(database))
	r.Delete("/notes/{id}", DeleteNote(database))
	r.Get("/notes/{id}/collab-token", CollabToken(database)) // y-sweet 房间 token
	// 权限管理(顶栏「权限」弹层,仅创建者)
	r.Get("/notes/{id}/permissions", NotePermsPanel(database))
	r.Put("/notes/{id}/visibility", SetNoteVisibility(database))
	r.Post("/notes/{id}/shares", AddNoteShare(database))
	r.Delete("/notes/{id}/shares/{uid}", RemoveNoteShare(database))
	r.Get("/notes/{id}/share-search", NoteShareSearch(database))
	r.Handle("/collab/*", CollabProxy()) // y-sweet websocket 反代(仅登录用户可达)
	r.Get("/notes/{id}/history", NoteHistory(database))
	r.Get("/notes/{id}/revisions/{rid}", NoteRevisionPage(database))
	r.Post("/notes/{id}/restore/{rid}", RestoreNoteRevision(database))
	r.Post("/notes/{id}/attachments", UploadNoteAttachment(database))
	r.Get("/attachments/{id}/{name}", ServeNoteAttachment(database))
}
