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
		"notification/notif_badge.html",
		"notification/notif_list.html",
		"notification/notif_read_response.html",
		"notification/message_badge.html",
		"notification/messages_preview.html",
		"notification/messages_center.html",
	)
}
