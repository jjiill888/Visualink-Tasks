package tasks

import (
	"html/template"
	"net/http"

	"visualink/internal/model"
	"visualink/internal/platform/web"
)

// pages:每个整页一个隔离模板集(防 {{define "content"}} 跨页冲突)。
var pages map[string]*template.Template

// partials:本模块 HTMX 片段集(功能行/评论/统计/组/偏好)。
var partials *template.Template

// InitTemplates 解析本模块的全部模板集;main 启动时调用。
func InitTemplates() {
	pages = map[string]*template.Template{}
	// 需要功能行与讨论组片段的页面(键=裸文件名,handler 按原名查;路径带模块目录)
	for _, page := range []string{"dashboard.html", "mine.html", "group_detail.html"} {
		pages[page] = web.ParseSet(
			"shared/base.html",
			"tasks/feature_row.html",
			"tasks/group_action_btn.html",
			"tasks/group_members_partial.html",
			"tasks/feature_watch_btn.html",
			"tasks/"+page,
		)
	}
	for _, page := range []string{"groups.html", "submit_standalone.html"} {
		pages[page] = web.ParseSet("shared/base.html", "tasks/"+page)
	}
	partials = web.ParseSet(
		"tasks/feature_row.html",
		"tasks/features_partial.html",
		"tasks/comments_partial.html",
		"tasks/feature_detail.html",
		"tasks/feature_watch_btn.html",
		"tasks/stats_partial.html",
		"tasks/submit_form_modal.html",
		"tasks/feature_draft_edit.html",
		"tasks/feature_modify.html",
		"tasks/group_action_btn.html",
		"tasks/group_members_partial.html",
		"tasks/preferences_partial.html",
	)
}

func render(w http.ResponseWriter, _ *http.Request, name string, data *model.PageData) {
	web.RenderPage(w, pages, name, data)
}
