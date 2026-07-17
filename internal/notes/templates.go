package notes

import (
	"html/template"
	"net/http"

	"visualink/internal/model"
	"visualink/internal/platform/web"
)

// pages:笔记模块的整页模板集。note_edit/notes 是不套 base.html 的独立页
// (Typora 式编辑器/极简文库),note_revision 仍带应用外壳。
var pages map[string]*template.Template

// partials:侧栏面板/文库列表/历史弹层/权限弹层片段。
var partials *template.Template

// InitTemplates 解析本模块的全部模板集;main 启动时调用。
func InitTemplates() {
	// 键=裸文件名(handler 与模板入口名都按原名),路径带模块目录
	pages = map[string]*template.Template{
		// 编辑页独立标签页,侧栏「文件」视图片段随页直出(/notes/panel 复用同一片段)
		"note_edit.html": web.ParseSet("notes/note_edit.html", "notes/notes_panel_partial.html"),
		// 文库页内嵌列表片段(搜索时 HTMX 单独刷新)
		"notes.html":         web.ParseSet("notes/notes.html", "notes/notes_list_partial.html"),
		"note_revision.html": web.ParseSet("shared/base.html", "notes/note_revision.html"),
		"note_diff.html":     web.ParseSet("shared/base.html", "notes/note_diff.html"),
	}
	partials = web.ParseSet(
		"notes/notes_list_partial.html",
		"notes/notes_trash_partial.html",
		"notes/notes_panel_partial.html",
		"notes/note_history.html",
		"notes/note_perms_partial.html",
	)
}

func render(w http.ResponseWriter, _ *http.Request, name string, data *model.PageData) {
	web.RenderPage(w, pages, name, data)
}

func renderStandalone(w http.ResponseWriter, name string, data *model.PageData) {
	web.RenderStandalone(w, pages, name, data)
}
