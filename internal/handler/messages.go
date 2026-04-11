package handler

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"featuretrack/internal/db"
	"featuretrack/internal/hub"
	"featuretrack/internal/model"
)

type messageBadgeData struct {
	Count int
}

type messageCenterData struct {
	Search        string
	Contacts      []*model.MessageContact
	ActiveKind    string
	ActiveUserID  int64
	ActiveTitle   string
	ActiveSubline string
	ActiveRole    string
	Notifications []*model.Notification
	MessageItems  []messageItemView
	CurrentUserID int64
	EmptyState    string
}

func messageRoleLabel(role string) string {
	switch role {
	case "pm":
		return "产品经理"
	case "dev":
		return "开发工程师"
	case "admin":
		return "管理员"
	default:
		return role
	}
}

type messageItemView struct {
	*model.DirectMessage
	Own         bool
	ShowDivider bool
	DividerText string
	Compact     bool
	ShowTime    bool
}

func buildMessageItems(messages []*model.DirectMessage, currentUserID int64) []messageItemView {
	items := make([]messageItemView, len(messages))
	for i, msg := range messages {
		currentBucket := msg.CreatedAtLocal()
		showDivider := i == 0
		compact := false
		if i > 0 {
			prev := messages[i-1]
			prevBucket := prev.CreatedAtLocal()
			showDivider = currentBucket != prevBucket
			compact = !showDivider && prev.SenderID == msg.SenderID
		}

		showTime := true
		if i < len(messages)-1 {
			next := messages[i+1]
			nextBucket := next.CreatedAtLocal()
			if nextBucket == currentBucket && next.SenderID == msg.SenderID {
				showTime = false
			}
		}

		items[i] = messageItemView{
			DirectMessage: msg,
			Own:           msg.SenderID == currentUserID,
			ShowDivider:   showDivider,
			DividerText:   currentBucket,
			Compact:       compact,
			ShowTime:      showTime,
		}
	}
	return items
}

func messageCenterURL(kind string, userID int64, search string) string {
	var parts []string
	if kind != "" {
		parts = append(parts, "kind="+kind)
	}
	if userID > 0 {
		parts = append(parts, "id="+strconv.FormatInt(userID, 10))
	}
	if search != "" {
		parts = append(parts, "q="+search)
	}
	if len(parts) == 0 {
		return "/messages/center"
	}
	return "/messages/center?" + strings.Join(parts, "&")
}

func buildMessageCenterData(database *db.DB, u *model.User, kind string, targetUserID int64, search string) (messageCenterData, error) {
	search = strings.TrimSpace(search)
	if kind == "user" && targetUserID > 0 {
		if err := database.MarkDirectMessagesRead(u.ID, targetUserID); err != nil {
			return messageCenterData{}, err
		}
	}
	// 不再自动全部已读系统通知，单条已读由 notif_list.html 控制

	systemContact, err := database.BuildSystemContact(u.ID)
	if err != nil {
		return messageCenterData{}, err
	}
	contacts, err := database.ListMessageContacts(u.ID, search)
	if err != nil {
		return messageCenterData{}, err
	}

	if kind == "" {
		if search != "" && len(contacts) > 0 {
			kind = "user"
			targetUserID = contacts[0].UserID
			search = ""
			contacts, err = database.ListMessageContacts(u.ID, "")
			if err != nil {
				return messageCenterData{}, err
			}
		} else if systemContact.UnreadCount > 0 || !systemContact.LastAt.IsZero() {
			kind = "system"
		} else if len(contacts) > 0 {
			kind = "user"
			targetUserID = contacts[0].UserID
		} else {
			kind = "system"
		}
	}

	if kind == "user" && targetUserID > 0 {
		found := false
		for _, c := range contacts {
			if c.UserID == targetUserID {
				found = true
				break
			}
		}
		if !found {
			other, err := database.GetUserByID(targetUserID)
			if err != nil {
				return messageCenterData{}, err
			}
			if other != nil {
				contacts = append([]*model.MessageContact{{
					Kind:      "user",
					UserID:    other.ID,
					Title:     other.DisplayName,
					Secondary: "@" + other.Username,
					Preview:   "开始新对话",
					Empty:     true,
				}}, contacts...)
			}
		}
	}

	data := messageCenterData{
		Search:        search,
		ActiveKind:    kind,
		ActiveUserID:  targetUserID,
		CurrentUserID: u.ID,
	}

	allContacts := make([]*model.MessageContact, 0, len(contacts)+1)
	includeSystem := search == "" || strings.Contains("系统通知", search) || strings.Contains("@通知", search)
	if includeSystem || systemContact.UnreadCount > 0 || !systemContact.LastAt.IsZero() {
		systemCopy := *systemContact
		systemCopy.Active = kind == "system"
		allContacts = append(allContacts, &systemCopy)
	}
	for _, contact := range contacts {
		copy := *contact
		copy.Active = kind == "user" && copy.UserID == targetUserID
		allContacts = append(allContacts, &copy)
	}
	data.Contacts = allContacts

	if kind == "system" {
		data.ActiveTitle = systemContact.Title
		data.ActiveSubline = systemContact.Secondary
		notifications, err := database.ListRecentNotifications(u.ID, 200)
		if err != nil {
			return messageCenterData{}, err
		}
		sort.Slice(notifications, func(i, j int) bool {
			return notifications[i].CreatedAt.Before(notifications[j].CreatedAt)
		})
		data.Notifications = notifications
		if len(notifications) == 0 {
			data.EmptyState = "暂无系统通知"
		}
		// 打开系统通知列表时全部标已读（蓝条不受影响，仍需实际查看功能才消除）
		_ = database.MarkAllNotificationsRead(u.ID)
		return data, nil
	}

	other, err := database.GetUserByID(targetUserID)
	if err != nil {
		return messageCenterData{}, err
	}
	if other == nil {
		data.ActiveKind = "system"
		data.ActiveTitle = systemContact.Title
		data.ActiveSubline = systemContact.Secondary
		data.EmptyState = "未找到联系人"
		return data, nil
	}
	data.ActiveTitle = other.DisplayName
	data.ActiveSubline = "@" + other.Username
	data.ActiveRole = messageRoleLabel(other.Role)
	messages, err := database.ListDirectMessages(u.ID, other.ID, 200)
	if err != nil {
		return messageCenterData{}, err
	}
	data.MessageItems = buildMessageItems(messages, u.ID)
	if len(messages) == 0 {
		data.EmptyState = "现在开始发第一条消息吧"
	}
	return data, nil
}

func GetMessageBadge(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r)
		count, err := database.CountUnreadInbox(u.ID)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := PartialTmpl.ExecuteTemplate(w, "message_badge.html", messageBadgeData{Count: count}); err != nil {
			http.Error(w, err.Error(), 500)
		}
	}
}

func GetMessagePreview(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r)
		count, err := database.CountUnreadInbox(u.ID)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		systemContact, err := database.BuildSystemContact(u.ID)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		contacts, err := database.ListMessageContacts(u.ID, "")
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		items := make([]*model.MessageContact, 0, len(contacts)+1)
		if systemContact.UnreadCount > 0 || !systemContact.LastAt.IsZero() {
			items = append(items, systemContact)
		}
		items = append(items, contacts...)

		hasUnread := false
		for _, item := range items {
			if item.UnreadCount > 0 {
				hasUnread = true
				break
			}
		}
		filtered := make([]*model.MessageContact, 0, len(items))
		for _, item := range items {
			if hasUnread {
				if item.UnreadCount > 0 {
					filtered = append(filtered, item)
				}
				continue
			}
			if !item.LastAt.IsZero() || item.UnreadCount > 0 || item.Kind == "system" {
				filtered = append(filtered, item)
			}
		}
		sort.Slice(filtered, func(i, j int) bool {
			return filtered[i].LastAt.After(filtered[j].LastAt)
		})
		if len(filtered) > 6 {
			filtered = filtered[:6]
		}
		preview := model.MessagePreview{Count: count, Items: filtered}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := PartialTmpl.ExecuteTemplate(w, "messages_preview.html", preview); err != nil {
			http.Error(w, err.Error(), 500)
		}
	}
}

func GetMessageCenter(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r)
		search := strings.TrimSpace(r.URL.Query().Get("q"))
		kind := strings.TrimSpace(r.URL.Query().Get("kind"))
		targetUserID, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		data, err := buildMessageCenterData(database, u, kind, targetUserID, search)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// 系统通知视图已在 buildMessageCenterData 中标记全部已读，通过 HX-Trigger 刷新徽章
		if data.ActiveKind == "system" {
			w.Header().Set("HX-Trigger", "message-refresh")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := PartialTmpl.ExecuteTemplate(w, "messages_center.html", data); err != nil {
			http.Error(w, err.Error(), 500)
		}
	}
}

func SendMessage(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r)
		recipientID, err := strconv.ParseInt(r.FormValue("recipient_id"), 10, 64)
		if err != nil || recipientID <= 0 {
			http.Error(w, "invalid recipient", 400)
			return
		}
		if recipientID == u.ID {
			http.Error(w, "不能给自己发消息", 400)
			return
		}
		content := strings.TrimSpace(r.FormValue("content"))
		if content == "" {
			http.Error(w, "消息不能为空", 400)
			return
		}
		other, err := database.GetUserByID(recipientID)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if other == nil {
			http.Error(w, "联系人不存在", 404)
			return
		}
		if _, err := database.CreateDirectMessage(u.ID, recipientID, content); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		hub.Global.Broadcast(fmt.Sprintf("mailbox-updated:%d", recipientID))
		hub.Global.Broadcast(fmt.Sprintf("mailbox-updated:%d", u.ID))

		data, err := buildMessageCenterData(database, u, "user", recipientID, strings.TrimSpace(r.FormValue("q")))
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := PartialTmpl.ExecuteTemplate(w, "messages_center.html", data); err != nil {
			http.Error(w, err.Error(), 500)
		}
	}
}
