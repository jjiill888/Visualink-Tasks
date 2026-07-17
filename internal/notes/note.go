package notes

import (
	"crypto/rand"
	"encoding/hex"
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

	"visualink/internal/model"
	"visualink/internal/platform/auth"
	"visualink/internal/platform/web"

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

// loadNote 解析路径中的 {id}、加载笔记并做可读性检查（MyAccess 由 SQL 按
// 当前用户算好）。失败时已写好响应，返回 nil。
func loadNote(w http.ResponseWriter, r *http.Request, d *Deps) *model.Note {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "无效的笔记 ID", http.StatusBadRequest)
		return nil
	}
	n, err := d.Repo.GetNote(id, auth.UserFromContext(r).ID)
	if err != nil {
		http.Error(w, "服务器错误", http.StatusInternalServerError)
		return nil
	}
	if n == nil || !n.CanRead() {
		// 无权限的笔记对外表现为不存在，避免泄露存在性
		http.Error(w, "笔记不存在", http.StatusNotFound)
		return nil
	}
	return n
}

// loadNoteForEdit loadNote + 编辑权检查（受限笔记的 reader 只读）。
func loadNoteForEdit(w http.ResponseWriter, r *http.Request, d *Deps) *model.Note {
	n := loadNote(w, r, d)
	if n == nil {
		return nil
	}
	if !n.CanEdit() {
		http.Error(w, "你对这篇笔记只有阅读权限", http.StatusForbidden)
		return nil
	}
	return n
}

// loadNoteOwner loadNote + 仅创建者（权限管理端点用）。
func loadNoteOwner(w http.ResponseWriter, r *http.Request, d *Deps) *model.Note {
	n := loadNote(w, r, d)
	if n == nil {
		return nil
	}
	if n.MyAccess != "owner" {
		http.Error(w, "仅创建者可管理权限", http.StatusForbidden)
		return nil
	}
	return n
}

type notesListData struct {
	Notes         []*model.Note
	Query         string
	CurrentUserID int64
}

func notesList(w http.ResponseWriter, r *http.Request, d *Deps, fullPage bool) {
	u := auth.UserFromContext(r)
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	notes, err := d.Repo.ListNotes(u.ID, query)
	if err != nil {
		http.Error(w, "查询失败", http.StatusInternalServerError)
		return
	}
	data := &notesListData{Notes: notes, Query: query, CurrentUserID: u.ID}
	if fullPage {
		pd := auth.PageData(r, "notes")
		pd.Data = data
		renderStandalone(w, "notes.html", pd)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := partials.ExecuteTemplate(w, "notes_list_partial.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// NotesPage GET /notes — 列表页；HTMX 搜索请求只返回列表片段。
func NotesPage(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		notesList(w, r, d, r.Header.Get("HX-Request") != "true")
	}
}

// notesPanelSection 侧栏「文件」视图的一个可见性分区（公共/共享/私人），
// 分区内：文档组（文件夹）在前、未分组文档平铺在后——VSCode 目录习惯。
type notesPanelSection struct {
	Key    string // 折叠状态的 localStorage 键（public/shared/private）
	Title  string
	Groups []*notesPanelGroup
	Loose  []*model.Note
}

// notesPanelGroup 分区内的文档组节点。Own = 当前用户建的组（有＋/删操作）。
type notesPanelGroup struct {
	ID    int64
	Name  string
	Own   bool
	Notes []*model.Note
}

// notesPanelData 编辑页侧栏「文件」视图的数据，按可见性分三区：
// 公共文档 = 所有人的公开笔记；共享协作 = 自己可见的受限笔记（自己建的
// + 别人加自己进名单的）；私人文档 = 自己的私有笔记。
// 文档组是纯组织结构：笔记落在哪个区仍由 visibility 决定，同组笔记可见性
// 不同时文件夹会在多个区分别出现（各显示各区的笔记）。自己建的空组
// （或组内笔记当前都不可见）挂在公共区——新文档默认公开，会落回这里。
func notesPanelData(d *Deps, userID int64) (map[string]any, error) {
	notes, err := d.Repo.ListNotes(userID, "")
	if err != nil {
		return nil, err
	}
	sections := []*notesPanelSection{
		{Key: "public", Title: "公共文档"},
		{Key: "shared", Title: "共享协作"},
		{Key: "private", Title: "私人文档"},
	}
	secOf := func(n *model.Note) *notesPanelSection {
		switch n.Visibility {
		case model.NoteVisPrivate:
			return sections[2]
		case model.NoteVisRestricted:
			return sections[1]
		default:
			return sections[0]
		}
	}
	// 每区一张 groupID → 节点 的索引；ListNotes 按更新时间倒序，组的出现
	// 顺序即「组内最新文档」的顺序，组内文档亦保持该序
	dirIdx := map[*notesPanelSection]map[int64]*notesPanelGroup{}
	seen := map[int64]bool{} // 已在任一区出现过的组
	for _, n := range notes {
		sec := secOf(n)
		if n.GroupID == 0 {
			sec.Loose = append(sec.Loose, n)
			continue
		}
		idx := dirIdx[sec]
		if idx == nil {
			idx = map[int64]*notesPanelGroup{}
			dirIdx[sec] = idx
		}
		dir := idx[n.GroupID]
		if dir == nil {
			dir = &notesPanelGroup{ID: n.GroupID, Name: n.GroupName, Own: false}
			idx[n.GroupID] = dir
			sec.Groups = append(sec.Groups, dir)
			seen[n.GroupID] = true
		}
		dir.Notes = append(dir.Notes, n)
	}
	// 自己建的组补 Own 标记；没有可见笔记的组挂公共区（空组也要能看到/删掉）
	owned, err := d.Repo.ListNoteGroups(userID)
	if err != nil {
		return nil, err
	}
	for _, gr := range owned {
		found := false
		for _, sec := range sections {
			if dir := dirIdx[sec][gr.ID]; dir != nil {
				dir.Own = true
				found = true
			}
		}
		if !found {
			sections[0].Groups = append(sections[0].Groups,
				&notesPanelGroup{ID: gr.ID, Name: gr.Name, Own: true})
		}
	}
	return map[string]any{"Sections": sections}, nil
}

// writeNotesPanel 渲染侧栏文档列表片段（面板刷新与组增删/移动的共同出口）。
func writeNotesPanel(w http.ResponseWriter, d *Deps, userID int64) {
	data, err := notesPanelData(d, userID)
	if err != nil {
		http.Error(w, "查询失败", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := partials.ExecuteTemplate(w, "notes_panel_partial.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// NotesPanel GET /notes/panel — 侧栏文档列表片段。
// 编辑页首屏由 NoteEditPage 直出同一片段（跨境高 RTT 下不必等 fetch），
// 本端点只用于打开侧栏时的后台刷新。
func NotesPanel(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeNotesPanel(w, d, auth.UserFromContext(r).ID)
	}
}

// CreateNoteGroup POST /notes/groups — 新建文档组，返回刷新后的面板片段。
func CreateNoteGroup(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFromContext(r)
		name := strings.TrimSpace(r.FormValue("name"))
		if name == "" {
			http.Error(w, "请输入组名", http.StatusBadRequest)
			return
		}
		if len([]rune(name)) > 50 {
			http.Error(w, "组名最多 50 个字符", http.StatusBadRequest)
			return
		}
		if _, err := d.Repo.CreateNoteGroup(u.ID, name); err != nil {
			http.Error(w, "创建失败", http.StatusInternalServerError)
			return
		}
		writeNotesPanel(w, d, u.ID)
	}
}

// DeleteNoteGroup DELETE /notes/groups/{gid} — 删组（仅建组者），
// 组内文档回到未分组，不删除文档。返回刷新后的面板片段。
func DeleteNoteGroup(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFromContext(r)
		gid, err := strconv.ParseInt(chi.URLParam(r, "gid"), 10, 64)
		if err != nil {
			http.Error(w, "无效的组 ID", http.StatusBadRequest)
			return
		}
		if err := d.Repo.DeleteNoteGroup(gid, u.ID); err != nil {
			http.Error(w, "只有建组者可以删除这个组", http.StatusForbidden)
			return
		}
		writeNotesPanel(w, d, u.ID)
	}
}

// SetNoteGroup PUT /notes/{id}/group — 移动笔记归属组（group_id 空/0 = 移出）。
// 只有笔记创建者能移，且只能挂进自己建的组（SQL 双重属主校验）。
// 返回刷新后的面板片段。
func SetNoteGroup(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFromContext(r)
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "无效的笔记 ID", http.StatusBadRequest)
			return
		}
		var gid int64
		if v := strings.TrimSpace(r.FormValue("group_id")); v != "" {
			if gid, err = strconv.ParseInt(v, 10, 64); err != nil || gid < 0 {
				http.Error(w, "无效的组 ID", http.StatusBadRequest)
				return
			}
		}
		if err := d.Repo.SetNoteGroup(id, u.ID, gid); err != nil {
			http.Error(w, "只能把自己的文档移进自己建的组", http.StatusForbidden)
			return
		}
		writeNotesPanel(w, d, u.ID)
	}
}

// renderNoteEdit 渲染编辑页（NoteEditPage 与 NewNote 共用）。
// 侧栏文档列表随页面直出（查询失败不阻塞编辑，Panel 为 nil 时
// 模板显示占位，前端打开侧栏时再 fetch 补拉）。
func renderNoteEdit(w http.ResponseWriter, r *http.Request, d *Deps, n *model.Note) {
	panel, err := notesPanelData(d, auth.UserFromContext(r).ID)
	if err != nil {
		panel = nil
	}
	// 只读用户（受限笔记的 reader）不进协作也不签房间 token
	canEdit := n.CanEdit()
	collabToken := ""
	if canEdit {
		// 预签房间 token 随页直出（省一次前端 round-trip）；失败为空串，
		// 前端回退到 /notes/{id}/collab-token
		collabToken = InlineCollabToken(r, n.ID)
	}
	pd := auth.PageData(r, "notes")
	pd.Data = map[string]any{
		"Note":        n,
		"Panel":       panel,
		"Collab":      canEdit && CollabEnabled(),
		"ReadOnly":    !canEdit,
		"CollabToken": collabToken,
	}
	renderStandalone(w, "note_edit.html", pd)
}

// NewNote GET /notes/new — 创建空笔记后**直出**编辑页，不再 302。
// 跨境高 RTT 下 302+跟随 = 两个串行来回；直出省掉一跳，模板里的
// history.replaceState 把地址栏归一到 /notes/{id}（防刷新重复建）。
// 用 GET 是为了让「新建笔记」做成 target="_blank" 的普通链接。
func NewNote(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFromContext(r)
		// ?group=N：在文档组内新建（侧栏组行的「＋」）；不是自己的组则忽略入组
		var groupID int64
		if v := r.URL.Query().Get("group"); v != "" {
			if gid, err := strconv.ParseInt(v, 10, 64); err == nil && d.Repo.OwnsNoteGroup(gid, u.ID) {
				groupID = gid
			}
		}
		// ?vis=private/restricted：分区头「＋」新建的文档直接落在对应分区；
		// 非法/缺省一律公开（既有默认）
		vis := r.URL.Query().Get("vis")
		if vis != model.NoteVisPrivate && vis != model.NoteVisRestricted {
			vis = model.NoteVisPublic
		}
		id, err := d.Repo.CreateNote(u.ID, "无标题笔记", groupID, vis)
		if err != nil {
			http.Error(w, "创建失败", http.StatusInternalServerError)
			return
		}
		n, err := d.Repo.GetNote(id, u.ID)
		if err != nil || n == nil {
			// 建成了但读不回来（几乎不可能），退回跳转兜底
			http.Redirect(w, r, fmt.Sprintf("/notes/%d", id), http.StatusFound)
			return
		}
		renderNoteEdit(w, r, d, n)
	}
}

// NoteEditPage GET /notes/{id} — 编辑页（Milkdown 客户端渲染岛）。
// 独立标签页打开，不套 base.html 应用外壳（Typora 式极简风格）。
func NoteEditPage(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := loadNote(w, r, d)
		if n == nil {
			return
		}
		renderNoteEdit(w, r, d, n)
	}
}

type saveNoteReq struct {
	Title         string `json:"title"`
	ContentMD     string `json:"content_md"`
	BaseUpdatedAt string `json:"base_updated_at"`
}

// SaveNote PUT /notes/{id} — 编辑器自动保存（需编辑权）。请求/响应均为 JSON。
// base_updated_at 与库中不一致时返回 409（乐观锁）。
func SaveNote(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := loadNoteForEdit(w, r, d)
		if n == nil {
			return
		}
		u := auth.UserFromContext(r)

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

		newUpdatedAt, err := d.Repo.SaveNote(n.ID, u.ID, title, req.ContentMD, req.BaseUpdatedAt)
		if errors.Is(err, ErrNoteConflict) {
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
func DeleteNote(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := loadNote(w, r, d)
		if n == nil {
			return
		}
		u := auth.UserFromContext(r)
		if n.OwnerID != u.ID {
			http.Error(w, "仅创建者可删除笔记", http.StatusForbidden)
			return
		}
		if err := d.Repo.SoftDeleteNote(n.ID, u.ID); err != nil {
			http.Error(w, "删除失败", http.StatusInternalServerError)
			return
		}
		// 文库页 HTMX 请求返回列表片段；侧栏文件树 fetch 返回面板片段
		if r.Header.Get("HX-Request") == "true" {
			notesList(w, r, d, false)
			return
		}
		writeNotesPanel(w, d, u.ID)
	}
}

// NoteHistory GET /notes/{id}/history — 版本列表（HTMX 弹层内容）。
func NoteHistory(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := loadNote(w, r, d)
		if n == nil {
			return
		}
		revs, err := d.Repo.ListNoteRevisions(n.ID)
		if err != nil {
			http.Error(w, "查询失败", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		data := map[string]any{"Note": n, "Revisions": revs, "CanEdit": n.CanEdit()}
		if err := partials.ExecuteTemplate(w, "note_history.html", data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

// NoteRevisionPage GET /notes/{id}/revisions/{rid} — 历史版本只读渲染页。
func NoteRevisionPage(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := loadNote(w, r, d)
		if n == nil {
			return
		}
		rid, err := strconv.ParseInt(chi.URLParam(r, "rid"), 10, 64)
		if err != nil {
			http.Error(w, "无效的版本 ID", http.StatusBadRequest)
			return
		}
		rev, err := d.Repo.GetNoteRevision(n.ID, rid)
		if err != nil {
			http.Error(w, "服务器错误", http.StatusInternalServerError)
			return
		}
		if rev == nil {
			http.Error(w, "版本不存在", http.StatusNotFound)
			return
		}
		pd := auth.PageData(r, "notes")
		pd.Data = map[string]any{"Note": n, "Rev": rev}
		render(w, r, "note_revision.html", pd)
	}
}

// RestoreNoteRevision POST /notes/{id}/restore/{rid} — 恢复到某历史版本（需编辑权）。
func RestoreNoteRevision(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := loadNoteForEdit(w, r, d)
		if n == nil {
			return
		}
		u := auth.UserFromContext(r)
		rid, err := strconv.ParseInt(chi.URLParam(r, "rid"), 10, 64)
		if err != nil {
			http.Error(w, "无效的版本 ID", http.StatusBadRequest)
			return
		}
		if err := d.Repo.RestoreNoteRevision(n.ID, rid, u.ID); err != nil {
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
		web.Redirect(w, r, target)
	}
}

// UploadNoteAttachment POST /notes/{id}/attachments — 图片/文件上传（需编辑权）。
// 校验大小（≤20MB）与扩展名白名单，返回 JSON {url, filename, is_image}。
func UploadNoteAttachment(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := loadNoteForEdit(w, r, d)
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
		if err := d.Repo.CreateNoteAttachment(a); err != nil {
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
func ServeNoteAttachment(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := loadNote(w, r, d)
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

// randomSlug 生成 16 位十六进制随机文件名(与 platform/upload 同法,独立小工具不共享)。
func randomSlug() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// ── 回收站 ──────────────────────────────────────────────────────────────────

// trashData 回收站片段的数据。
type trashData struct {
	Notes []*TrashedNote
}

func renderTrash(w http.ResponseWriter, d *Deps, ownerID int64) {
	list, err := d.Repo.ListTrashedNotes(ownerID)
	if err != nil {
		http.Error(w, "查询失败", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := partials.ExecuteTemplate(w, "notes_trash_partial.html", &trashData{Notes: list}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// NotesTrash handles GET /notes/trash — 文库页回收站视图(HTMX 片段)。
func NotesTrash(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFromContext(r)
		renderTrash(w, d, u.ID)
	}
}

// RecoverNote handles POST /notes/{id}/recover — 恢复到原分区/原分组。
// 不走 loadNote:GetNote 只查未删除的,这里要操作的恰是已删除行。
func RecoverNote(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFromContext(r)
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "无效的笔记 ID", http.StatusBadRequest)
			return
		}
		if err := d.Repo.RecoverNote(id, u.ID); err != nil {
			http.Error(w, "恢复失败:笔记不在回收站,或不属于你", http.StatusForbidden)
			return
		}
		renderTrash(w, d, u.ID)
	}
}

// PurgeNote handles DELETE /notes/{id}/purge — 彻底删除(库行 CASCADE + 磁盘附件目录)。
func PurgeNote(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFromContext(r)
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "无效的笔记 ID", http.StatusBadRequest)
			return
		}
		if err := d.Repo.PurgeNote(id, u.ID); err != nil {
			http.Error(w, "删除失败:笔记不在回收站,或不属于你", http.StatusForbidden)
			return
		}
		// 库行已删,附件目录属于该笔记独占,直接整目录移除
		_ = os.RemoveAll(filepath.Join(NoteAttachRoot, strconv.FormatInt(id, 10)))
		renderTrash(w, d, u.ID)
	}
}
