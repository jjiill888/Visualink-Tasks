package notes

import (
	"visualink/internal/platform/auth"

	"github.com/go-chi/chi/v5"
)

// Deps 是本模块 handler 的依赖束:自己的 Repo + 用户查询(auth)。
type Deps struct {
	Repo  *Repo
	Users *auth.Store
}

// Routes 注册云笔记路由(文库/编辑页/文档组/权限/历史/附件/协作);
// 挂在登录中间件之内。
func Routes(r chi.Router, d *Deps) {
	r.Get("/notes", NotesPage(d))
	r.Get("/notes/new", NewNote(d))
	r.Get("/notes/panel", NotesPanel(d)) // chi 静态段优先于 {id}
	// 回收站(仅创建者视角;恢复/彻底删除不走 loadNote——它只查未删除行)
	r.Get("/notes/trash", NotesTrash(d))
	r.Post("/notes/{id}/recover", RecoverNote(d))
	r.Delete("/notes/{id}/purge", PurgeNote(d))
	// 文档组(侧栏文件树的文件夹,纯组织结构;四端点都返回刷新后的面板片段)
	r.Post("/notes/groups", CreateNoteGroup(d))
	r.Put("/notes/groups/{gid}", RenameNoteGroup(d))
	r.Delete("/notes/groups/{gid}", DeleteNoteGroup(d))
	r.Put("/notes/{id}/group", SetNoteGroup(d))
	r.Get("/notes/{id}", NoteEditPage(d))
	r.Put("/notes/{id}", SaveNote(d))
	r.Delete("/notes/{id}", DeleteNote(d))
	r.Get("/notes/{id}/collab-token", CollabToken(d)) // y-sweet 房间 token
	// 权限管理(顶栏「权限」弹层,仅创建者)
	r.Get("/notes/{id}/permissions", NotePermsPanel(d))
	r.Put("/notes/{id}/visibility", SetNoteVisibility(d))
	r.Post("/notes/{id}/shares", AddNoteShare(d))
	r.Delete("/notes/{id}/shares/{uid}", RemoveNoteShare(d))
	r.Get("/notes/{id}/share-search", NoteShareSearch(d))
	r.Handle("/collab/*", CollabProxy()) // y-sweet websocket 反代(仅登录用户可达)
	r.Get("/notes/{id}/export", ExportNote(d)) // Markdown 原文下载
	r.Get("/notes/{id}/history", NoteHistory(d))
	r.Get("/notes/{id}/revisions/{rid}", NoteRevisionPage(d))
	r.Get("/notes/{id}/revisions/{rid}/diff", NoteRevisionDiff(d)) // 与上一版本的行级对比
	r.Post("/notes/{id}/restore/{rid}", RestoreNoteRevision(d))
	r.Post("/notes/{id}/attachments", UploadNoteAttachment(d))
	r.Get("/attachments/{id}/{name}", ServeNoteAttachment(d))
}
