package handler

// 云笔记权限管理：编辑页顶栏「权限」弹层的一组 HTMX 端点，全部仅创建者可用。
//
//	GET    /notes/{id}/permissions      弹层整体片段（可见性三档 + 名单 + 搜索框）
//	PUT    /notes/{id}/visibility       切换可见性档位
//	POST   /notes/{id}/shares           添加成员 / 改角色（upsert；添加自动切「受限」）
//	DELETE /notes/{id}/shares/{uid}     移除成员
//	GET    /notes/{id}/share-search?q=  搜索可添加的用户（排除创建者与已在名单内的）
//
// 除 share-search 返回候选列表小片段外，其余端点统一返回刷新后的弹层整体片段，
// 前端用 outerHTML swap 原地替换，按钮标签由模板里的 htmx:afterSwap 钩子同步。

import (
	"net/http"
	"strconv"
	"strings"

	"featuretrack/internal/db"
	"featuretrack/internal/model"

	"github.com/go-chi/chi/v5"
)

// renderNotePerms 渲染权限弹层整体片段（各写端点成功后复用）。
func renderNotePerms(w http.ResponseWriter, database *db.DB, n *model.Note) {
	shares, err := database.ListNoteShares(n.ID)
	if err != nil {
		http.Error(w, "查询失败", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := map[string]any{"Note": n, "Shares": shares}
	if err := PartialTmpl.ExecuteTemplate(w, "note_perms_partial.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// NotePermsPanel GET /notes/{id}/permissions
func NotePermsPanel(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := loadNoteOwner(w, r, database)
		if n == nil {
			return
		}
		renderNotePerms(w, database, n)
	}
}

// SetNoteVisibility PUT /notes/{id}/visibility — 表单字段 visibility。
func SetNoteVisibility(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := loadNoteOwner(w, r, database)
		if n == nil {
			return
		}
		v := r.FormValue("visibility")
		if v != model.NoteVisPublic && v != model.NoteVisRestricted && v != model.NoteVisPrivate {
			http.Error(w, "无效的可见性档位", http.StatusBadRequest)
			return
		}
		if err := database.SetNoteVisibility(n.ID, v); err != nil {
			http.Error(w, "保存失败", http.StatusInternalServerError)
			return
		}
		n.Visibility = v
		renderNotePerms(w, database, n)
	}
}

// AddNoteShare POST /notes/{id}/shares — 表单字段 user_id、role。
// 已在名单内则改角色（名单行的角色下拉切换也走这里）。
func AddNoteShare(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := loadNoteOwner(w, r, database)
		if n == nil {
			return
		}
		uid, err := strconv.ParseInt(r.FormValue("user_id"), 10, 64)
		if err != nil {
			http.Error(w, "无效的用户 ID", http.StatusBadRequest)
			return
		}
		role := r.FormValue("role")
		if role != "editor" && role != "reader" {
			http.Error(w, "无效的角色", http.StatusBadRequest)
			return
		}
		if uid == n.OwnerID {
			http.Error(w, "创建者始终拥有全部权限，无需添加", http.StatusBadRequest)
			return
		}
		target, err := database.GetUserByID(uid)
		if err != nil || target == nil {
			http.Error(w, "用户不存在", http.StatusBadRequest)
			return
		}
		if err := database.UpsertNoteShare(n.ID, uid, role); err != nil {
			http.Error(w, "保存失败", http.StatusInternalServerError)
			return
		}
		n.Visibility = model.NoteVisRestricted // UpsertNoteShare 已在库里切档
		renderNotePerms(w, database, n)
	}
}

// RemoveNoteShare DELETE /notes/{id}/shares/{uid}
func RemoveNoteShare(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := loadNoteOwner(w, r, database)
		if n == nil {
			return
		}
		uid, err := strconv.ParseInt(chi.URLParam(r, "uid"), 10, 64)
		if err != nil {
			http.Error(w, "无效的用户 ID", http.StatusBadRequest)
			return
		}
		if err := database.RemoveNoteShare(n.ID, uid); err != nil {
			http.Error(w, "移除失败", http.StatusInternalServerError)
			return
		}
		renderNotePerms(w, database, n)
	}
}

// NoteShareSearch GET /notes/{id}/share-search?q= — 候选用户小片段。
func NoteShareSearch(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := loadNoteOwner(w, r, database)
		if n == nil {
			return
		}
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		var users []*model.User
		if q != "" {
			var err error
			users, err = database.SearchShareCandidates(n.ID, n.OwnerID, q)
			if err != nil {
				http.Error(w, "查询失败", http.StatusInternalServerError)
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		data := map[string]any{"Note": n, "Users": users, "Query": q}
		if err := PartialTmpl.ExecuteTemplate(w, "note_share_results", data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}
