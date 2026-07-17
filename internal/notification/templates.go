package notification

import (
	"html/template"

	"visualink/internal/platform/web"
)

// partials:通知角标/下拉列表/已读回执 + 消息中心(角标/预览/中心/发送)。
var partials *template.Template

// InitTemplates 解析本模块的片段模板集;main 启动时调用。
func InitTemplates() {
	partials = web.ParseSet(
		"notif_badge.html",
		"notif_list.html",
		"notif_read_response.html",
		"message_badge.html",
		"messages_preview.html",
		"messages_center.html",
	)
}
