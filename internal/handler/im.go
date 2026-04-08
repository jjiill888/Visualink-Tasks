package handler

import (
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"featuretrack/internal/db"
	"featuretrack/internal/hub"
	"featuretrack/internal/model"

	"github.com/go-chi/chi/v5"
)

const imPageSize = 40

var channelNameRe = regexp.MustCompile(`^[\p{L}\p{N}_-]{1,32}$`)

// ── View model ──────────────────────────────────────────────────────────────

type imMsgView struct {
	*model.IMMessage
	Own       bool
	ShowDate  bool
	DateLabel string
	Compact   bool
}

type imPageData struct {
	CurrentUser   *model.User
	MyChannels    []*model.IMChannel  // group channels (public/private)
	MyDMs         []*model.IMChannel  // DM contacts from direct_messages
	ActiveChannel *model.IMChannel
	Messages      []imMsgView
	// Notification mode
	Notifications    []*model.Notification
	NotifUnreadCount int
	// DM context
	DMPartnerID int64
	// Pagination
	HasMore     bool
	OldestMsgID int64
	NewestMsgID int64
}

func buildIMMessageViews(msgs []*model.IMMessage, currentUserID int64) []imMsgView {
	views := make([]imMsgView, len(msgs))
	const compactWindow = 10 * time.Minute
	for i, msg := range msgs {
		v := imMsgView{
			IMMessage: msg,
			Own:       msg.UserID == currentUserID,
		}
		if i == 0 {
			v.ShowDate = true
			v.DateLabel = msg.DateLabel()
		} else {
			prev := msgs[i-1]
			if prev.CreatedAt.Format("2006-01-02") != msg.CreatedAt.Format("2006-01-02") {
				v.ShowDate = true
				v.DateLabel = msg.DateLabel()
			} else if prev.UserID == msg.UserID && msg.CreatedAt.Sub(prev.CreatedAt) < compactWindow {
				v.Compact = true
			}
		}
		views[i] = v
	}
	return views
}

// dmToIMMessage converts a DirectMessage into an IMMessage for unified rendering.
func dmToIMMessage(dm *model.DirectMessage) *model.IMMessage {
	return &model.IMMessage{
		ID:          dm.ID,
		UserID:      dm.SenderID,
		Content:     dm.Content,
		CreatedAt:   dm.CreatedAt,
		UserDisplay: dm.SenderName,
		UserName:    dm.SenderName,
	}
}

// loadIMSidebar builds the sidebar data:
// - Group channels from im_channel_members
// - DM contacts from direct_messages (same source as message center)
func loadIMSidebar(database *db.DB, userID int64) (myChannels []*model.IMChannel, myDMs []*model.IMChannel, notifUnread int, err error) {
	// Group channels only (type != 'direct')
	myChannels, _, err = database.ListUserIMChannels(userID)
	if err != nil {
		return
	}

	// DM contacts from direct_messages table (reuse existing data)
	contacts, err := database.ListMessageContacts(userID, "")
	if err != nil {
		return
	}
	for _, c := range contacts {
		if c.Kind != "user" {
			continue
		}
		myDMs = append(myDMs, &model.IMChannel{
			ID:          c.UserID, // partner's userID, used in /im/dm/{ID}
			DisplayName: c.Title,
			Type:        "direct",
			UnreadCount: c.UnreadCount,
			LastMsg:     c.Preview,
			LastMsgAt:   c.LastAt,
		})
	}

	// Notification unread count
	notifUnread, err = database.CountUnreadNotifications(userID)
	return
}

func renderIMFull(w http.ResponseWriter, data *imPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := IMTmpl.ExecuteTemplate(w, "im_layout.html", data); err != nil {
		http.Error(w, err.Error(), 500)
	}
}

func renderIMPartial(w http.ResponseWriter, tmplName string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := IMTmpl.ExecuteTemplate(w, tmplName, data); err != nil {
		http.Error(w, err.Error(), 500)
	}
}

// ── Handlers ────────────────────────────────────────────────────────────────

// IMHome handles GET /im — redirects to first group channel, first DM, or notifications.
func IMHome(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r)

		myChannels, myDMs, _, err := loadIMSidebar(database, u.ID)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		// Prefer first group channel
		if len(myChannels) > 0 {
			http.Redirect(w, r, fmt.Sprintf("/im/c/%d", myChannels[0].ID), http.StatusSeeOther)
			return
		}
		// Else first DM
		if len(myDMs) > 0 {
			http.Redirect(w, r, fmt.Sprintf("/im/dm/%d", myDMs[0].ID), http.StatusSeeOther)
			return
		}
		// Try auto-join #general
		general, err := database.GetIMChannelByNamePublic("general")
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if general != nil {
			if err := database.JoinIMChannel(general.ID, u.ID); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			http.Redirect(w, r, fmt.Sprintf("/im/c/%d", general.ID), http.StatusSeeOther)
			return
		}
		// No channels — show empty shell
		data := &imPageData{CurrentUser: u}
		renderIMFull(w, data)
	}
}

// IMChannel handles GET /im/c/{id} — group channel view.
func IMChannel(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r)
		channelID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid channel", 400)
			return
		}

		ch, err := database.GetIMChannelForUser(channelID, u.ID)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if ch == nil {
			http.NotFound(w, r)
			return
		}

		// Auto-join public channels on first visit
		if !ch.IsMember && ch.IsPublic() {
			if err := database.JoinIMChannel(channelID, u.ID); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			ch.IsMember = true
		} else if !ch.IsMember {
			http.Error(w, "无权访问此频道", 403)
			return
		}

		msgs, hasMore, err := loadChannelMessages(database, channelID, 0)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if len(msgs) > 0 {
			_ = database.UpdateIMLastRead(channelID, u.ID, msgs[len(msgs)-1].ID)
		}

		myChannels, myDMs, notifUnread, err := loadIMSidebar(database, u.ID)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		views := buildIMMessageViews(msgs, u.ID)
		var oldestID, newestID int64
		if len(msgs) > 0 {
			oldestID = msgs[0].ID
			newestID = msgs[len(msgs)-1].ID
		}

		data := &imPageData{
			CurrentUser:      u,
			MyChannels:       myChannels,
			MyDMs:            myDMs,
			ActiveChannel:    ch,
			Messages:         views,
			HasMore:          hasMore,
			OldestMsgID:      oldestID,
			NewestMsgID:      newestID,
			NotifUnreadCount: notifUnread,
		}
		renderIMFull(w, data)
	}
}

func loadChannelMessages(database *db.DB, channelID, beforeID int64) ([]*model.IMMessage, bool, error) {
	msgs, err := database.ListIMMessages(channelID, beforeID, imPageSize+1)
	if err != nil {
		return nil, false, err
	}
	hasMore := len(msgs) > imPageSize
	if hasMore {
		msgs = msgs[1:]
	}
	return msgs, hasMore, nil
}

// GetIMMessages handles GET /im/c/{id}/messages?before={id} — cursor pagination.
func GetIMMessages(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r)
		channelID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		beforeID, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)

		ok, err := database.IsIMChannelMember(channelID, u.ID)
		if err != nil || !ok {
			http.Error(w, "forbidden", 403)
			return
		}

		msgs, hasMore, err := loadChannelMessages(database, channelID, beforeID)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		var oldestID int64
		if len(msgs) > 0 {
			oldestID = msgs[0].ID
		}
		views := buildIMMessageViews(msgs, u.ID)

		renderIMPartial(w, "im_message_page.html", &imPageData{
			CurrentUser:   u,
			ActiveChannel: &model.IMChannel{ID: channelID},
			Messages:      views,
			HasMore:       hasMore,
			OldestMsgID:   oldestID,
		})
	}
}

// GetNewIMMessages handles GET /im/c/{id}/messages/new?after={id} — SSE-triggered.
func GetNewIMMessages(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r)
		channelID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		afterID, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)

		ok, err := database.IsIMChannelMember(channelID, u.ID)
		if err != nil || !ok {
			http.Error(w, "forbidden", 403)
			return
		}

		msgs, err := database.ListNewIMMessages(channelID, afterID)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if len(msgs) > 0 {
			_ = database.UpdateIMLastRead(channelID, u.ID, msgs[len(msgs)-1].ID)
		}

		renderIMPartial(w, "im_message_list.html", &imPageData{
			CurrentUser: u,
			Messages:    buildIMMessageViews(msgs, u.ID),
		})
	}
}

// SendIMMessage handles POST /im/c/{id}/messages — group channel send.
func SendIMMessage(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r)
		channelID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid channel", 400)
			return
		}
		ok, err := database.IsIMChannelMember(channelID, u.ID)
		if err != nil || !ok {
			http.Error(w, "forbidden", 403)
			return
		}
		content := strings.TrimSpace(r.FormValue("content"))
		if content == "" {
			http.Error(w, "消息不能为空", 400)
			return
		}
		if len([]rune(content)) > 4000 {
			http.Error(w, "消息过长", 400)
			return
		}
		msgID, err := database.CreateIMMessage(channelID, u.ID, content)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		_ = database.UpdateIMLastRead(channelID, u.ID, msgID)
		hub.Global.Broadcast(fmt.Sprintf("im-msg:%d", channelID))
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
	}
}

// ── DM handlers (read/write direct_messages table) ──────────────────────────

// IMDMView handles GET /im/dm/{userID} — open DM conversation using direct_messages.
func IMDMView(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r)
		partnerID, err := strconv.ParseInt(chi.URLParam(r, "userID"), 10, 64)
		if err != nil || partnerID == u.ID {
			http.Error(w, "invalid user", 400)
			return
		}

		partner, err := database.GetUserByID(partnerID)
		if err != nil || partner == nil {
			http.Error(w, "用户不存在", 404)
			return
		}

		// Mark as read
		_ = database.MarkDirectMessagesRead(u.ID, partnerID)

		// Load messages from direct_messages table
		dms, err := database.ListDirectMessages(u.ID, partnerID, imPageSize)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		// Convert to IMMessage for unified rendering
		msgs := make([]*model.IMMessage, len(dms))
		for i, dm := range dms {
			msgs[i] = dmToIMMessage(dm)
		}
		views := buildIMMessageViews(msgs, u.ID)

		var newestID int64
		if len(msgs) > 0 {
			newestID = msgs[len(msgs)-1].ID
		}

		myChannels, myDMs, notifUnread, err := loadIMSidebar(database, u.ID)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		// Virtual channel object for DM
		activeCh := &model.IMChannel{
			ID:          partnerID,
			DisplayName: partner.DisplayName,
			Type:        "direct",
		}

		data := &imPageData{
			CurrentUser:      u,
			MyChannels:       myChannels,
			MyDMs:            myDMs,
			ActiveChannel:    activeCh,
			Messages:         views,
			DMPartnerID:      partnerID,
			NewestMsgID:      newestID,
			NotifUnreadCount: notifUnread,
			// DMs always show all messages (no pagination for now)
			HasMore: false,
		}
		renderIMFull(w, data)
	}
}

// SendIMDM handles POST /im/dm/{userID}/messages — writes to direct_messages.
func SendIMDM(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r)
		partnerID, err := strconv.ParseInt(chi.URLParam(r, "userID"), 10, 64)
		if err != nil || partnerID == u.ID {
			http.Error(w, "invalid user", 400)
			return
		}
		content := strings.TrimSpace(r.FormValue("content"))
		if content == "" {
			http.Error(w, "消息不能为空", 400)
			return
		}
		if len([]rune(content)) > 4000 {
			http.Error(w, "消息过长", 400)
			return
		}

		if _, err := database.CreateDirectMessage(u.ID, partnerID, content); err != nil {
			log.Printf("SendIMDM: CreateDirectMessage senderID=%d partnerID=%d err=%v", u.ID, partnerID, err)
			http.Error(w, err.Error(), 500)
			return
		}

		// Reuse existing mailbox-updated event (same as Tasks message center)
		hub.Global.Broadcast(fmt.Sprintf("mailbox-updated:%d", partnerID))
		hub.Global.Broadcast(fmt.Sprintf("mailbox-updated:%d", u.ID))
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
	}
}

// GetNewIMDMMessages handles GET /im/dm/{userID}/messages/new?after={id}.
func GetNewIMDMMessages(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r)
		partnerID, _ := strconv.ParseInt(chi.URLParam(r, "userID"), 10, 64)
		afterID, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)

		dms, err := database.ListNewDirectMessages(u.ID, partnerID, afterID)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		_ = database.MarkDirectMessagesRead(u.ID, partnerID)

		msgs := make([]*model.IMMessage, len(dms))
		for i, dm := range dms {
			msgs[i] = dmToIMMessage(dm)
		}

		renderIMPartial(w, "im_message_list.html", &imPageData{
			CurrentUser: u,
			Messages:    buildIMMessageViews(msgs, u.ID),
		})
	}
}

// ── Notifications handler ────────────────────────────────────────────────────

// IMNotifications handles GET /im/notifications — system notifications view.
func IMNotifications(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r)

		notifs, err := database.ListRecentNotifications(u.ID, 200)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		sort.Slice(notifs, func(i, j int) bool {
			return notifs[i].CreatedAt.Before(notifs[j].CreatedAt)
		})

		myChannels, myDMs, _, err := loadIMSidebar(database, u.ID)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		data := &imPageData{
			CurrentUser: u,
			MyChannels:  myChannels,
			MyDMs:       myDMs,
			ActiveChannel: &model.IMChannel{
				ID:          0,
				DisplayName: "系统通知",
				Type:        "system",
			},
			Notifications:    notifs,
			NotifUnreadCount: 0, // just opened, reset
		}
		renderIMFull(w, data)
	}
}

// ── Channel management ───────────────────────────────────────────────────────

// CreateIMChannel handles POST /im/channels.
func CreateIMChannel(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r)
		name := strings.TrimSpace(strings.ToLower(r.FormValue("name")))
		displayName := strings.TrimSpace(r.FormValue("display_name"))
		description := strings.TrimSpace(r.FormValue("description"))
		chType := r.FormValue("type")

		if !channelNameRe.MatchString(name) {
			http.Error(w, "频道名只能包含字母、数字、下划线和连字符，长度1-32", 400)
			return
		}
		if chType != "public" && chType != "private" {
			chType = "public"
		}
		if displayName == "" {
			displayName = name
		}

		ch, err := database.CreateIMChannel(name, displayName, description, chType, u.ID)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				http.Error(w, "频道名已存在", 409)
				return
			}
			http.Error(w, err.Error(), 500)
			return
		}

		hub.Global.Broadcast("im-channel-created")
		http.Redirect(w, r, fmt.Sprintf("/im/c/%d", ch.ID), http.StatusSeeOther)
	}
}

// JoinIMChannel handles POST /im/c/{id}/join.
func JoinIMChannel(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r)
		channelID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid channel", 400)
			return
		}
		ch, err := database.GetIMChannel(channelID)
		if err != nil || ch == nil {
			http.Error(w, "频道不存在", 404)
			return
		}
		if ch.Type != "public" {
			http.Error(w, "无法加入私有频道", 403)
			return
		}
		if err := database.JoinIMChannel(channelID, u.ID); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		hub.Global.Broadcast("im-channel-created")
		http.Redirect(w, r, fmt.Sprintf("/im/c/%d", ch.ID), http.StatusSeeOther)
	}
}

// LeaveIMChannel handles DELETE /im/c/{id}/join.
func LeaveIMChannel(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r)
		channelID, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err := database.LeaveIMChannel(channelID, u.ID); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		http.Redirect(w, r, "/im", http.StatusSeeOther)
	}
}

// IMSidebar handles GET /im/sidebar — partial for HTMX refresh.
func IMSidebar(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r)
		activeChannelID, _ := strconv.ParseInt(r.URL.Query().Get("active"), 10, 64)
		activeType := r.URL.Query().Get("type") // "direct", "system", or ""

		myChannels, myDMs, notifUnread, err := loadIMSidebar(database, u.ID)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		var activeCh *model.IMChannel
		if activeChannelID > 0 || activeType != "" {
			activeCh = &model.IMChannel{ID: activeChannelID, Type: activeType}
		}

		renderIMPartial(w, "im_sidebar.html", &imPageData{
			CurrentUser:      u,
			MyChannels:       myChannels,
			MyDMs:            myDMs,
			ActiveChannel:    activeCh,
			NotifUnreadCount: notifUnread,
		})
	}
}

// IMNewChannelForm handles GET /im/channels/new.
func IMNewChannelForm(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r)
		myChannels, myDMs, notifUnread, _ := loadIMSidebar(database, u.ID)
		renderIMFull(w, &imPageData{
			CurrentUser:      u,
			MyChannels:       myChannels,
			MyDMs:            myDMs,
			NotifUnreadCount: notifUnread,
			ActiveChannel:    &model.IMChannel{ID: -1, Type: "new"},
		})
	}
}
