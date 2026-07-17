// 通知/私信/联系人的数据访问(原 internal/db 的对应段)。
package notification

import (
	"database/sql"
	"strings"
	"time"

	"visualink/internal/model"
)

// Repo 持有通知与私信表的数据访问。
type Repo struct {
	*sql.DB
}

func NewRepo(db *sql.DB) *Repo { return &Repo{db} }

// MarkNotificationReadByID 标记单条通知为已读
func (d *Repo) MarkNotificationReadByID(userID, notifID int64) error {
	_, err := d.Exec(
		"UPDATE notifications SET is_read=1 WHERE user_id=? AND id=?",
		userID, notifID,
	)
	return err
}

func migrateNotifications(d *sql.DB) error {
	type notificationColumn struct {
		name    string
		notNull bool
	}

	rows, err := d.Query(`PRAGMA table_info(notifications)`)
	if err != nil {
		return err
	}
	defer rows.Close()

	columns := map[string]notificationColumn{}
	for rows.Next() {
		var cid int
		var name, dataType string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		columns[name] = notificationColumn{name: name, notNull: notNull == 1}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	commentIDColumn, hasCommentID := columns["comment_id"]
	_, hasMessage := columns["message"]
	if hasCommentID && commentIDColumn.notNull {
		return rebuildNotificationsTable(d, hasMessage)
	}
	if !hasMessage {
		_, err := d.Exec(`ALTER TABLE notifications ADD COLUMN message TEXT NOT NULL DEFAULT ''`)
		if err != nil {
			return err
		}
	}
	return nil
}

func rebuildNotificationsTable(d *sql.DB, hasMessage bool) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`ALTER TABLE notifications RENAME TO notifications_legacy`); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		CREATE TABLE notifications (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id       INTEGER NOT NULL REFERENCES users(id),
			feature_id    INTEGER NOT NULL REFERENCES features(id) ON DELETE CASCADE,
			comment_id    INTEGER REFERENCES comments(id) ON DELETE CASCADE,
			from_user     TEXT NOT NULL DEFAULT '',
			feature_title TEXT NOT NULL DEFAULT '',
			message       TEXT NOT NULL DEFAULT '',
			is_read       INTEGER NOT NULL DEFAULT 0,
			created_at    DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return err
	}

	messageExpr := `''`
	if hasMessage {
		messageExpr = `COALESCE(message, '')`
	}
	copyQuery := `
		INSERT INTO notifications (id, user_id, feature_id, comment_id, from_user, feature_title, message, is_read, created_at)
		SELECT id, user_id, feature_id, NULLIF(comment_id, 0), COALESCE(from_user, ''), COALESCE(feature_title, ''), ` + messageExpr + `, is_read, created_at
		FROM notifications_legacy
	`
	if _, err := tx.Exec(copyQuery); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE notifications_legacy`); err != nil {
		return err
	}
	return tx.Commit()
}

// ── Notifications ───────────────────────────────────────────────────────────

func (d *Repo) CreateNotification(n *model.Notification) error {
	message := n.Message
	if message == "" {
		message = n.PreviewText()
	}
	_, err := d.Exec(
		`INSERT INTO notifications (user_id, feature_id, comment_id, from_user, feature_title, message) VALUES (?,?,?,?,?,?)`,
		n.UserID, n.FeatureID, nullInt64(n.CommentID), n.FromUser, n.FeatureTitle, message,
	)
	return err
}

func nullInt64(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

func (d *Repo) ListUnreadNotifications(userID int64) ([]*model.Notification, error) {
	rows, err := d.Query(`
		SELECT id, user_id, feature_id, comment_id, from_user, feature_title, message, is_read, created_at
		FROM notifications
		WHERE user_id=? AND is_read=0
		ORDER BY created_at DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*model.Notification
	for rows.Next() {
		n := &model.Notification{}
		var commentID sql.NullInt64
		if err := rows.Scan(&n.ID, &n.UserID, &n.FeatureID, &commentID, &n.FromUser, &n.FeatureTitle, &n.Message, &n.IsRead, &n.CreatedAt); err != nil {
			return nil, err
		}
		if commentID.Valid {
			n.CommentID = commentID.Int64
		}
		list = append(list, n)
	}
	return list, rows.Err()
}

func (d *Repo) MarkNotificationsReadByFeature(userID, featureID int64) error {
	_, err := d.Exec(
		`UPDATE notifications SET is_read=1 WHERE user_id=? AND feature_id=?`,
		userID, featureID,
	)
	return err
}

func (d *Repo) MarkAllNotificationsRead(userID int64) error {
	_, err := d.Exec(`UPDATE notifications SET is_read=1 WHERE user_id=?`, userID)
	return err
}

func (d *Repo) CountUnreadNotifications(userID int64) (int, error) {
	var count int
	err := d.QueryRow(`SELECT COUNT(*) FROM notifications WHERE user_id=? AND is_read=0`, userID).Scan(&count)
	return count, err
}

func (d *Repo) ListRecentNotifications(userID int64, limit int) ([]*model.Notification, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := d.Query(`
		SELECT id, user_id, feature_id, comment_id, from_user, feature_title, message, is_read, created_at
		FROM notifications
		WHERE user_id=?
		ORDER BY created_at DESC
		LIMIT ?
	`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*model.Notification
	for rows.Next() {
		n := &model.Notification{}
		var commentID sql.NullInt64
		if err := rows.Scan(&n.ID, &n.UserID, &n.FeatureID, &commentID, &n.FromUser, &n.FeatureTitle, &n.Message, &n.IsRead, &n.CreatedAt); err != nil {
			return nil, err
		}
		if commentID.Valid {
			n.CommentID = commentID.Int64
		}
		list = append(list, n)
	}
	return list, rows.Err()
}

func (d *Repo) CreateDirectMessage(senderID, recipientID int64, content string) (*model.DirectMessage, error) {
	res, err := d.Exec(
		`INSERT INTO direct_messages (sender_id, recipient_id, content) VALUES (?,?,?)`,
		senderID, recipientID, content,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	msg := &model.DirectMessage{ID: id, SenderID: senderID, RecipientID: recipientID, Content: content}
	_ = d.QueryRow(`SELECT created_at FROM direct_messages WHERE id=?`, id).Scan(&msg.CreatedAt)
	return msg, nil
}

func (d *Repo) CountUnreadDirectMessages(userID int64) (int, error) {
	var count int
	err := d.QueryRow(`SELECT COUNT(*) FROM direct_messages WHERE recipient_id=? AND is_read=0`, userID).Scan(&count)
	return count, err
}

func (d *Repo) CountUnreadInbox(userID int64) (int, error) {
	notifs, err := d.CountUnreadNotifications(userID)
	if err != nil {
		return 0, err
	}
	dms, err := d.CountUnreadDirectMessages(userID)
	if err != nil {
		return 0, err
	}
	return notifs + dms, nil
}

func compactPreview(text string) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if len([]rune(text)) <= 36 {
		return text
	}
	runes := []rune(text)
	return string(runes[:36]) + "…"
}

func (d *Repo) ListMessageContacts(userID int64, search string) ([]*model.MessageContact, error) {
	contacts := make([]*model.MessageContact, 0)
	seen := map[int64]*model.MessageContact{}
	rows, err := d.Query(`
		SELECT
			CASE WHEN dm.sender_id=? THEN dm.recipient_id ELSE dm.sender_id END AS partner_id,
			COALESCE(NULLIF(u.display_name,''), u.username) AS display_name,
			u.username,
			dm.sender_id,
			dm.recipient_id,
			dm.content,
			dm.created_at,
			dm.is_read
		FROM direct_messages dm
		JOIN users u ON u.id = CASE WHEN dm.sender_id=? THEN dm.recipient_id ELSE dm.sender_id END
		WHERE dm.sender_id=? OR dm.recipient_id=?
		ORDER BY dm.created_at DESC
	`, userID, userID, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var partnerID, senderID, recipientID int64
		var displayName, username, content string
		var createdAt time.Time
		var isRead bool
		if err := rows.Scan(&partnerID, &displayName, &username, &senderID, &recipientID, &content, &createdAt, &isRead); err != nil {
			return nil, err
		}
		contact, ok := seen[partnerID]
		if !ok {
			contact = &model.MessageContact{
				Kind:      "user",
				UserID:    partnerID,
				Title:     displayName,
				Secondary: "@" + username,
				Preview:   compactPreview(content),
				LastAt:    createdAt,
			}
			seen[partnerID] = contact
			contacts = append(contacts, contact)
		}
		if recipientID == userID && !isRead {
			contact.UnreadCount++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	query := strings.ToLower(strings.TrimSpace(search))
	filtered := make([]*model.MessageContact, 0, len(contacts))
	for _, contact := range contacts {
		if query == "" || strings.Contains(strings.ToLower(contact.Title), query) || strings.Contains(strings.ToLower(contact.Secondary), query) {
			filtered = append(filtered, contact)
		}
	}

	if query != "" {
		like := "%" + query + "%"
		rows, err := d.Query(`
			SELECT id, COALESCE(NULLIF(display_name,''), username), username
			FROM users
			WHERE id != ? AND (LOWER(display_name) LIKE ? OR LOWER(username) LIKE ?)
			ORDER BY display_name ASC
			LIMIT 12
		`, userID, like, like)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var displayName, username string
			if err := rows.Scan(&id, &displayName, &username); err != nil {
				return nil, err
			}
			if _, ok := seen[id]; ok {
				continue
			}
			filtered = append(filtered, &model.MessageContact{
				Kind:      "user",
				UserID:    id,
				Title:     displayName,
				Secondary: "@" + username,
				Preview:   "开始新对话",
				Empty:     true,
			})
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	return filtered, nil
}

func (d *Repo) BuildSystemContact(userID int64) (*model.MessageContact, error) {
	unreadCount, err := d.CountUnreadNotifications(userID)
	if err != nil {
		return nil, err
	}
	recent, err := d.ListRecentNotifications(userID, 1)
	if err != nil {
		return nil, err
	}
	contact := &model.MessageContact{
		Kind:        "system",
		Title:       "系统通知",
		Secondary:   "功能状态与 @ 通知",
		UnreadCount: unreadCount,
		Preview:     "暂无系统通知",
	}
	if len(recent) > 0 {
		contact.Preview = recent[0].PreviewText()
		contact.LastAt = recent[0].CreatedAt
	}
	return contact, nil
}

func (d *Repo) ListDirectMessages(userID, partnerID int64, limit int) ([]*model.DirectMessage, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.Query(`
		SELECT dm.id, dm.sender_id, dm.recipient_id, dm.content, dm.is_read, dm.created_at,
		       COALESCE(NULLIF(s.display_name,''), s.username),
		       COALESCE(NULLIF(r.display_name,''), r.username)
		FROM direct_messages dm
		JOIN users s ON s.id = dm.sender_id
		JOIN users r ON r.id = dm.recipient_id
		WHERE (dm.sender_id=? AND dm.recipient_id=?) OR (dm.sender_id=? AND dm.recipient_id=?)
		ORDER BY dm.created_at ASC
		LIMIT ?
	`, userID, partnerID, partnerID, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*model.DirectMessage
	for rows.Next() {
		m := &model.DirectMessage{}
		if err := rows.Scan(&m.ID, &m.SenderID, &m.RecipientID, &m.Content, &m.IsRead, &m.CreatedAt, &m.SenderName, &m.RecipientName); err != nil {
			return nil, err
		}
		list = append(list, m)
	}
	return list, rows.Err()
}

// ListNewDirectMessages returns DMs between two users with id > afterID (for SSE refresh).
func (d *Repo) ListNewDirectMessages(userID, partnerID, afterID int64) ([]*model.DirectMessage, error) {
	rows, err := d.Query(`
		SELECT dm.id, dm.sender_id, dm.recipient_id, dm.content, dm.is_read, dm.created_at,
		       COALESCE(NULLIF(s.display_name,''), s.username),
		       COALESCE(NULLIF(r.display_name,''), r.username)
		FROM direct_messages dm
		JOIN users s ON s.id = dm.sender_id
		JOIN users r ON r.id = dm.recipient_id
		WHERE ((dm.sender_id=? AND dm.recipient_id=?) OR (dm.sender_id=? AND dm.recipient_id=?))
		  AND dm.id > ?
		ORDER BY dm.created_at ASC
		LIMIT 100
	`, userID, partnerID, partnerID, userID, afterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*model.DirectMessage
	for rows.Next() {
		m := &model.DirectMessage{}
		if err := rows.Scan(&m.ID, &m.SenderID, &m.RecipientID, &m.Content, &m.IsRead, &m.CreatedAt, &m.SenderName, &m.RecipientName); err != nil {
			return nil, err
		}
		list = append(list, m)
	}
	return list, rows.Err()
}

func (d *Repo) MarkDirectMessagesRead(userID, partnerID int64) error {
	_, err := d.Exec(`UPDATE direct_messages SET is_read=1 WHERE recipient_id=? AND sender_id=? AND is_read=0`, userID, partnerID)
	return err
}
