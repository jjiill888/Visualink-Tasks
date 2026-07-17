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
	// 需要功能行与讨论组片段的页面
	for _, page := range []string{"dashboard.html", "mine.html", "group_detail.html"} {
		pages[page] = web.ParseSet(
			"base.html",
			"feature_row.html",
			"group_action_btn.html",
			"group_members_partial.html",
			"feature_watch_btn.html",
			page,
		)
	}
	for _, page := range []string{"groups.html", "submit_standalone.html"} {
		pages[page] = web.ParseSet("base.html", page)
	}
	partials = web.ParseSet(
		"feature_row.html",
		"features_partial.html",
		"comments_partial.html",
		"feature_detail.html",
		"feature_watch_btn.html",
		"stats_partial.html",
		"submit_form_modal.html",
		"feature_draft_edit.html",
		"feature_modify.html",
		"group_action_btn.html",
		"group_members_partial.html",
		"preferences_partial.html",
	)
}

func render(w http.ResponseWriter, _ *http.Request, name string, data *model.PageData) {
	web.RenderPage(w, pages, name, data)
}
