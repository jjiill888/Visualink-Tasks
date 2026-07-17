package im

import (
	"html/template"

	"visualink/internal/platform/web"
)

// imTmpl:IM 独立窗口的模板集(布局+侧栏+频道+通知视图+消息片段)。
var imTmpl *template.Template

// InitTemplates 解析本模块的模板集;main 启动时调用。
func InitTemplates() {
	imTmpl = web.ParseSet(
		"im_layout.html",
		"im_sidebar.html",
		"im_channel.html",
		"im_notif_view.html",
		"im_message_list.html",
		"im_message_page.html",
		"im_new_channel.html",
	)
}
