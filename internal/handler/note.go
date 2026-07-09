package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"featuretrack/internal/db"
	"featuretrack/internal/model"

	"github.com/go-chi/chi/v5"
)

// NoteAttachRoot 笔记附件的磁盘根目录，按 {note_id}/{随机名.扩展名} 组织。
// 可用环境变量 NOTES_ATTACH_DIR 覆盖（测试/部署用）。
var NoteAttachRoot = func() string {
	if v := os.Getenv("NOTES_ATTACH_DIR"); v != "" {
		return v
	}
	return "./data/attachments"
}()

const maxNoteAttachBytes = 20 << 20 // 附件单文件上限 20MB

// noteAttachExts 附件扩展名白名单。禁止 html/svg 等可在浏览器执行脚本的类型。
var noteAttachExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true,
	".pdf": true, ".txt": true, ".md": true, ".csv": true, ".zip": true,
	".doc": true, ".docx": true, ".xls": true, ".xlsx": true, ".ppt": true, ".pptx": true,
}

var noteImageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true,
}

// noteStoredNameRe 附件落盘文件名格式：16 位 hex 随机名 + 扩展名（由服务端生成）。
var noteStoredNameRe = regexp.MustCompile(`^[a-f0-9]{16}\.[a-z0-9]{1,8}$`)

// canAccessNote 权限规则：非私有笔记所有登录用户可查看/编辑；私有笔记仅 owner。
func canAccessNote(n *model.Note, u *model.User) bool {
	return !n.IsPrivate || n.OwnerID == u.ID
}

// loadNote 解析路径中的 {id}、加载笔记并做可见性检查。失败时已写好响应，返回 nil。
func loadNote(w http.ResponseWriter, r *http.Request, database *db.DB) *model.Note {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "无效的笔记 ID", http.StatusBadRequest)
		return nil
	}
	n, err := database.GetNote(id)
	if err != nil {
		http.Error(w, "服务器错误", http.StatusInternalServerError)
		return nil
	}
	if n == nil || !canAccessNote(n, UserFromContext(r)) {
		// 私有笔记对非 owner 表现为不存在，避免泄露存在性
		http.Error(w, "笔记不存在", http.StatusNotFound)
		return nil
	}
	return n
}

type notesListData struct {
	Notes         []*model.Note
	Query         string
	CurrentUserID int64
}

func notesList(w http.ResponseWriter, r *http.Request, database *db.DB, fullPage bool) {
	u := UserFromContext(r)
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	notes, err := database.ListNotes(u.ID, query)
	if err != nil {
		http.Error(w, "查询失败", http.StatusInternalServerError)
		return
	}
	data := &notesListData{Notes: notes, Query: query, CurrentUserID: u.ID}
	if fullPage {
		pd := pageData(r, "notes")
		pd.Data = data
		renderStandalone(w, "notes.html", pd)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := PartialTmpl.ExecuteTemplate(w, "notes_list_partial.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// NotesPage GET /notes — 列表页；HTMX 搜索请求只返回列表片段。
func NotesPage(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		notesList(w, r, database, r.Header.Get("HX-Request") != "true")
	}
}

// notesPanelData 编辑页侧栏「文件」视图的数据。
// 公共文档 = 所有人的非私有笔记；私人文档 = 自己的私有笔记（ListNotes 的
// 可见性规则本就如此，这里只按 IsPrivate 分组）。
func notesPanelData(database *db.DB, userID int64) (map[string]any, error) {
	notes, err := database.ListNotes(userID, "")
	if err != nil {
		return nil, err
	}
	var public, private []*model.Note
	for _, n := range notes {
		if n.IsPrivate {
			private = append(private, n)
		} else {
			public = append(public, n)
		}
	}
	return map[string]any{"Public": public, "Private": private}, nil
}

// NotesPanel GET /notes/panel — 侧栏文档列表片段。
// 编辑页首屏由 NoteEditPage 直出同一片段（跨境高 RTT 下不必等 fetch），
// 本端点只用于打开侧栏时的后台刷新。
func NotesPanel(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := notesPanelData(database, UserFromContext(r).ID)
		if err != nil {
			http.Error(w, "查询失败", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := PartialTmpl.ExecuteTemplate(w, "notes_panel_partial.html", data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

// NewNote GET /notes/new — 创建空笔记后 302 到编辑页。
// 用 GET 是为了让列表页的「新建笔记」做成 target="_blank" 的普通链接，
// 直接在新标签页里落到新笔记的编辑器。
func NewNote(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r)
		id, err := database.CreateNote(u.ID, "无标题笔记")
		if err != nil {
			http.Error(w, "创建失败", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/notes/%d", id), http.StatusFound)
	}
}

// NoteEditPage GET /notes/{id} — 编辑页（Milkdown 客户端渲染岛）。
// 独立标签页打开，不套 base.html 应用外壳（Typora 式极简风格）。
func NoteEditPage(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := loadNote(w, r, database)
		if n == nil {
			return
		}
		// 侧栏文档列表随页面直出（查询失败不阻塞编辑，Panel 为 nil 时
		// 模板显示占位，前端打开侧栏时再 fetch 补拉）
		panel, err := notesPanelData(database, UserFromContext(r).ID)
		if err != nil {
			panel = nil
		}
		pd := pageData(r, "notes")
		pd.Data = map[string]any{"Note": n, "Panel": panel}
		renderStandalone(w, "note_edit.html", pd)
	}
}

type saveNoteReq struct {
	Title         string `json:"title"`
	ContentMD     string `json:"content_md"`
	BaseUpdatedAt string `json:"base_updated_at"`
	IsPrivate     *bool  `json:"is_private"` // 仅 owner 的请求生效
}

// SaveNote PUT /notes/{id} — 编辑器自动保存。请求/响应均为 JSON。
// base_updated_at 与库中不一致时返回 409（乐观锁）。
func SaveNote(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := loadNote(w, r, database)
		if n == nil {
			return
		}
		u := UserFromContext(r)

		r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
		var req saveNoteReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "请求格式错误", http.StatusBadRequest)
			return
		}
		title := strings.TrimSpace(req.Title)
		if title == "" {
			title = "无标题笔记"
		}

		isPrivate := req.IsPrivate
		if n.OwnerID != u.ID {
			isPrivate = nil // 私有开关只有 owner 能改
		}
		newUpdatedAt, err := database.SaveNote(n.ID, u.ID, title, req.ContentMD, req.BaseUpdatedAt, isPrivate)
		if errors.Is(err, db.ErrNoteConflict) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "笔记已被他人修改"})
			return
		}
		if err != nil {
			http.Error(w, "保存失败", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]string{"updated_at": newUpdatedAt})
	}
}

// DeleteNote DELETE /notes/{id} — 软删除（仅 owner），HTMX 返回刷新后的列表片段。
func DeleteNote(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := loadNote(w, r, database)
		if n == nil {
			return
		}
		u := UserFromContext(r)
		if n.OwnerID != u.ID {
			http.Error(w, "仅创建者可删除笔记", http.StatusForbidden)
			return
		}
		if err := database.SoftDeleteNote(n.ID, u.ID); err != nil {
			http.Error(w, "删除失败", http.StatusInternalServerError)
			return
		}
		notesList(w, r, database, false)
	}
}

// NoteHistory GET /notes/{id}/history — 版本列表（HTMX 弹层内容）。
func NoteHistory(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := loadNote(w, r, database)
		if n == nil {
			return
		}
		revs, err := database.ListNoteRevisions(n.ID)
		if err != nil {
			http.Error(w, "查询失败", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		data := map[string]any{"Note": n, "Revisions": revs}
		if err := PartialTmpl.ExecuteTemplate(w, "note_history.html", data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

// NoteRevisionPage GET /notes/{id}/revisions/{rid} — 历史版本只读渲染页。
func NoteRevisionPage(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := loadNote(w, r, database)
		if n == nil {
			return
		}
		rid, err := strconv.ParseInt(chi.URLParam(r, "rid"), 10, 64)
		if err != nil {
			http.Error(w, "无效的版本 ID", http.StatusBadRequest)
			return
		}
		rev, err := database.GetNoteRevision(n.ID, rid)
		if err != nil {
			http.Error(w, "服务器错误", http.StatusInternalServerError)
			return
		}
		if rev == nil {
			http.Error(w, "版本不存在", http.StatusNotFound)
			return
		}
		pd := pageData(r, "notes")
		pd.Data = map[string]any{"Note": n, "Rev": rev}
		render(w, r, "note_revision.html", pd)
	}
}

// RestoreNoteRevision POST /notes/{id}/restore/{rid} — 恢复到某历史版本。
func RestoreNoteRevision(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := loadNote(w, r, database)
		if n == nil {
			return
		}
		u := UserFromContext(r)
		rid, err := strconv.ParseInt(chi.URLParam(r, "rid"), 10, 64)
		if err != nil {
			http.Error(w, "无效的版本 ID", http.StatusBadRequest)
			return
		}
		if err := database.RestoreNoteRevision(n.ID, rid, u.ID); err != nil {
			http.Error(w, "恢复失败："+err.Error(), http.StatusInternalServerError)
			return
		}
		target := fmt.Sprintf("/notes/%d", n.ID)
		if r.Header.Get("HX-Request") == "true" {
			// HTMX 发起（历史弹层内），让浏览器整页跳回编辑页加载恢复后的内容
			w.Header().Set("HX-Redirect", target)
			w.WriteHeader(http.StatusOK)
			return
		}
		redirect(w, r, target)
	}
}

// UploadNoteAttachment POST /notes/{id}/attachments — 图片/文件上传。
// 校验大小（≤20MB）与扩展名白名单，返回 JSON {url, filename, is_image}。
func UploadNoteAttachment(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := loadNote(w, r, database)
		if n == nil {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxNoteAttachBytes+1<<20)
		if err := r.ParseMultipartForm(maxNoteAttachBytes); err != nil {
			http.Error(w, "文件过大或上传失败（限 20MB）", http.StatusRequestEntityTooLarge)
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "未收到文件", http.StatusBadRequest)
			return
		}
		defer file.Close()

		if header.Size > maxNoteAttachBytes {
			http.Error(w, "文件过大（限 20MB）", http.StatusRequestEntityTooLarge)
			return
		}
		ext := strings.ToLower(filepath.Ext(header.Filename))
		if !noteAttachExts[ext] {
			http.Error(w, "不支持的文件类型", http.StatusUnsupportedMediaType)
			return
		}

		dir := filepath.Join(NoteAttachRoot, strconv.FormatInt(n.ID, 10))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			http.Error(w, "存储目录创建失败", http.StatusInternalServerError)
			return
		}
		storedName := randomSlug() + ext
		absPath := filepath.Join(dir, storedName)
		dst, err := os.Create(absPath)
		if err != nil {
			http.Error(w, "写入失败", http.StatusInternalServerError)
			return
		}
		size, err := io.Copy(dst, file)
		closeErr := dst.Close()
		if err != nil || closeErr != nil {
			_ = os.Remove(absPath)
			http.Error(w, "写入失败", http.StatusInternalServerError)
			return
		}

		a := &model.NoteAttachment{
			NoteID:     n.ID,
			Filename:   strings.TrimSpace(header.Filename),
			StoredPath: filepath.ToSlash(filepath.Join(strconv.FormatInt(n.ID, 10), storedName)),
			Size:       size,
		}
		if err := database.CreateNoteAttachment(a); err != nil {
			_ = os.Remove(absPath)
			http.Error(w, "数据库写入失败", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"url":      fmt.Sprintf("/attachments/%d/%s", n.ID, storedName),
			"filename": a.Filename,
			"is_image": noteImageExts[ext],
		})
	}
}

// ServeNoteAttachment GET /attachments/{id}/{name} — 附件静态服务（需登录，
// 且校验对所属笔记的可见性；私有笔记附件外人不可见）。
func ServeNoteAttachment(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := loadNote(w, r, database)
		if n == nil {
			return
		}
		name := chi.URLParam(r, "name")
		// 文件名由服务端生成的随机 hex 组成；不匹配即视为路径穿越尝试
		if !noteStoredNameRe.MatchString(name) {
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		abs := filepath.Join(NoteAttachRoot, strconv.FormatInt(n.ID, 10), name)

		ext := strings.ToLower(filepath.Ext(name))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
		switch ext {
		case ".jpg", ".jpeg":
			w.Header().Set("Content-Type", "image/jpeg")
		case ".png":
			w.Header().Set("Content-Type", "image/png")
		case ".gif":
			w.Header().Set("Content-Type", "image/gif")
		case ".webp":
			w.Header().Set("Content-Type", "image/webp")
		case ".pdf":
			w.Header().Set("Content-Type", "application/pdf")
		default:
			// 非图片/PDF 一律按下载处理，杜绝内容嗅探为可执行页面
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", "attachment")
		}
		http.ServeFile(w, r, abs)
	}
}
