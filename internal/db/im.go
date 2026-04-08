package db

import (
	"database/sql"
	"fmt"
	"time"

	"featuretrack/internal/model"
)

// ── Channel queries ─────────────────────────────────────────────────────────

// GetIMChannel returns a channel by ID without membership context.
func (d *DB) GetIMChannel(id int64) (*model.IMChannel, error) {
	row := d.QueryRow(`
		SELECT id, name, display_name, description, type, created_by, created_at
		FROM im_channels WHERE id = ?`, id)
	return scanIMChannel(row)
}

func (d *DB) getIMChannelByName(name string) (*model.IMChannel, error) {
	row := d.QueryRow(`
		SELECT id, name, display_name, description, type, created_by, created_at
		FROM im_channels WHERE name = ?`, name)
	ch, err := scanIMChannel(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return ch, err
}

func scanIMChannel(row *sql.Row) (*model.IMChannel, error) {
	var ch model.IMChannel
	err := row.Scan(&ch.ID, &ch.Name, &ch.DisplayName, &ch.Description,
		&ch.Type, &ch.CreatedBy, &ch.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &ch, nil
}

// ListUserIMChannels returns the group channels and DM channels the user belongs to,
// enriched with unread count and last message preview.
func (d *DB) ListUserIMChannels(userID int64) (myChannels []*model.IMChannel, myDMs []*model.IMChannel, err error) {
	rows, err := d.Query(`
		SELECT
			c.id, c.name,
			CASE c.type
				WHEN 'direct' THEN (
					SELECT u2.display_name FROM im_channel_members cm2
					JOIN users u2 ON u2.id = cm2.user_id
					WHERE cm2.channel_id = c.id AND cm2.user_id != ?
					LIMIT 1
				)
				ELSE COALESCE(NULLIF(c.display_name,''), c.name)
			END AS display_name,
			c.description, c.type, c.created_by, c.created_at,
			cm.role, cm.last_read_msg_id,
			COALESCE((
				SELECT COUNT(*) FROM im_messages im
				WHERE im.channel_id = c.id AND im.id > cm.last_read_msg_id AND im.deleted_at IS NULL
			), 0) AS unread_count,
			COALESCE((
				SELECT im.content FROM im_messages im
				WHERE im.channel_id = c.id AND im.deleted_at IS NULL
				ORDER BY im.id DESC LIMIT 1
			), '') AS last_msg,
			COALESCE((
				SELECT im.created_at FROM im_messages im
				WHERE im.channel_id = c.id AND im.deleted_at IS NULL
				ORDER BY im.id DESC LIMIT 1
			), '') AS last_msg_at
		FROM im_channels c
		JOIN im_channel_members cm ON cm.channel_id = c.id AND cm.user_id = ?
		ORDER BY c.type ASC, unread_count DESC, c.name ASC
	`, userID, userID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var ch model.IMChannel
		var lastMsgAtStr string
		var lastReadMsgID int64
		err := rows.Scan(
			&ch.ID, &ch.Name, &ch.DisplayName, &ch.Description,
			&ch.Type, &ch.CreatedBy, &ch.CreatedAt,
			&ch.MyRole, &lastReadMsgID,
			&ch.UnreadCount, &ch.LastMsg, &lastMsgAtStr,
		)
		if err != nil {
			return nil, nil, err
		}
		ch.IsMember = true
		if lastMsgAtStr != "" {
			ch.LastMsgAt, _ = time.Parse("2006-01-02 15:04:05", lastMsgAtStr)
		}
		if ch.Type == "direct" {
			myDMs = append(myDMs, &ch)
		} else {
			myChannels = append(myChannels, &ch)
		}
	}
	return myChannels, myDMs, rows.Err()
}

// GetOrCreateDMChannel finds or creates a 1-to-1 DM channel between two users.
func (d *DB) GetOrCreateDMChannel(userA, userB int64) (*model.IMChannel, error) {
	min, max := userA, userB
	if min > max {
		min, max = max, min
	}
	name := fmt.Sprintf("dm:%d_%d", min, max)

	ch, err := d.getIMChannelByName(name)
	if err != nil {
		return nil, err
	}
	if ch != nil {
		return ch, nil
	}

	// Create new DM channel
	tx, err := d.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`INSERT INTO im_channels (name, type, created_by) VALUES (?, 'direct', ?)`,
		name, userA,
	)
	if err != nil {
		return nil, err
	}
	channelID, _ := res.LastInsertId()

	_, err = tx.Exec(
		`INSERT OR IGNORE INTO im_channel_members (channel_id, user_id, role) VALUES (?, ?, 'member'), (?, ?, 'member')`,
		channelID, userA, channelID, userB,
	)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return d.GetIMChannel(channelID)
}

// CreateIMChannel creates a new public/private channel and adds the creator as owner.
func (d *DB) CreateIMChannel(name, displayName, description, chType string, createdBy int64) (*model.IMChannel, error) {
	tx, err := d.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`INSERT INTO im_channels (name, display_name, description, type, created_by) VALUES (?, ?, ?, ?, ?)`,
		name, displayName, description, chType, createdBy,
	)
	if err != nil {
		return nil, err
	}
	channelID, _ := res.LastInsertId()

	_, err = tx.Exec(
		`INSERT INTO im_channel_members (channel_id, user_id, role) VALUES (?, ?, 'owner')`,
		channelID, createdBy,
	)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return d.GetIMChannel(channelID)
}

// IsIMChannelMember returns true if the user is a member of the channel.
func (d *DB) IsIMChannelMember(channelID, userID int64) (bool, error) {
	var count int
	err := d.QueryRow(
		`SELECT COUNT(*) FROM im_channel_members WHERE channel_id=? AND user_id=?`,
		channelID, userID,
	).Scan(&count)
	return count > 0, err
}

// JoinIMChannel adds a user to a channel (idempotent).
func (d *DB) JoinIMChannel(channelID, userID int64) error {
	_, err := d.Exec(
		`INSERT OR IGNORE INTO im_channel_members (channel_id, user_id, role) VALUES (?, ?, 'member')`,
		channelID, userID,
	)
	return err
}

// LeaveIMChannel removes a user from a channel.
func (d *DB) LeaveIMChannel(channelID, userID int64) error {
	_, err := d.Exec(
		`DELETE FROM im_channel_members WHERE channel_id=? AND user_id=?`,
		channelID, userID,
	)
	return err
}

// ListIMChannelMembers returns all members of a channel.
func (d *DB) ListIMChannelMembers(channelID int64) ([]*model.IMChannelMember, error) {
	rows, err := d.Query(`
		SELECT cm.channel_id, cm.user_id, cm.role, cm.last_read_msg_id, cm.joined_at,
		       u.display_name, u.username
		FROM im_channel_members cm
		JOIN users u ON u.id = cm.user_id
		WHERE cm.channel_id = ?
		ORDER BY cm.role DESC, u.display_name ASC
	`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var members []*model.IMChannelMember
	for rows.Next() {
		var m model.IMChannelMember
		if err := rows.Scan(&m.ChannelID, &m.UserID, &m.Role, &m.LastReadMsgID, &m.JoinedAt,
			&m.DisplayName, &m.Username); err != nil {
			return nil, err
		}
		members = append(members, &m)
	}
	return members, rows.Err()
}

// GetIMChannelByNamePublic returns a public channel by its internal name, or nil.
func (d *DB) GetIMChannelByNamePublic(name string) (*model.IMChannel, error) {
	row := d.QueryRow(`
		SELECT id, name, display_name, description, type, created_by, created_at
		FROM im_channels WHERE name = ? AND type = 'public'`, name)
	ch, err := scanIMChannel(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return ch, err
}

// ListPublicIMChannels returns all public channels (for discovery).
func (d *DB) ListPublicIMChannels(userID int64) ([]*model.IMChannel, error) {
	rows, err := d.Query(`
		SELECT c.id, c.name, COALESCE(NULLIF(c.display_name,''), c.name) as display_name,
		       c.description, c.type, c.created_by, c.created_at,
		       EXISTS(SELECT 1 FROM im_channel_members WHERE channel_id=c.id AND user_id=?) AS is_member
		FROM im_channels c
		WHERE c.type = 'public'
		ORDER BY c.name ASC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var channels []*model.IMChannel
	for rows.Next() {
		var ch model.IMChannel
		if err := rows.Scan(&ch.ID, &ch.Name, &ch.DisplayName, &ch.Description,
			&ch.Type, &ch.CreatedBy, &ch.CreatedAt, &ch.IsMember); err != nil {
			return nil, err
		}
		channels = append(channels, &ch)
	}
	return channels, rows.Err()
}

// ── Message queries ─────────────────────────────────────────────────────────

const imMessageSelect = `
	SELECT m.id, m.channel_id, m.user_id, m.content, m.reply_to_id,
	       m.edited_at, m.deleted_at, m.created_at,
	       u.username, u.display_name
	FROM im_messages m
	JOIN users u ON u.id = m.user_id`

func scanIMMessages(rows *sql.Rows) ([]*model.IMMessage, error) {
	defer rows.Close()
	var msgs []*model.IMMessage
	for rows.Next() {
		var m model.IMMessage
		var editedAt, deletedAt sql.NullTime
		var replyToID sql.NullInt64
		if err := rows.Scan(&m.ID, &m.ChannelID, &m.UserID, &m.Content, &replyToID,
			&editedAt, &deletedAt, &m.CreatedAt, &m.UserName, &m.UserDisplay); err != nil {
			return nil, err
		}
		if replyToID.Valid {
			v := replyToID.Int64
			m.ReplyToID = &v
		}
		if editedAt.Valid {
			t := editedAt.Time
			m.EditedAt = &t
		}
		if deletedAt.Valid {
			t := deletedAt.Time
			m.DeletedAt = &t
		}
		msgs = append(msgs, &m)
	}
	return msgs, rows.Err()
}

// ListIMMessages returns up to `limit` messages before `beforeID` (cursor pagination).
// If beforeID == 0, returns the most recent messages.
// Results are returned oldest-first (ascending order for display).
func (d *DB) ListIMMessages(channelID, beforeID int64, limit int) ([]*model.IMMessage, error) {
	var rows *sql.Rows
	var err error
	if beforeID <= 0 {
		rows, err = d.Query(imMessageSelect+`
			WHERE m.channel_id = ? AND m.deleted_at IS NULL
			ORDER BY m.id DESC LIMIT ?`, channelID, limit)
	} else {
		rows, err = d.Query(imMessageSelect+`
			WHERE m.channel_id = ? AND m.id < ? AND m.deleted_at IS NULL
			ORDER BY m.id DESC LIMIT ?`, channelID, beforeID, limit)
	}
	if err != nil {
		return nil, err
	}
	msgs, err := scanIMMessages(rows)
	if err != nil {
		return nil, err
	}
	// Reverse to ascending order (oldest first for display)
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

// ListNewIMMessages returns messages newer than afterID (for SSE-triggered refresh).
func (d *DB) ListNewIMMessages(channelID, afterID int64) ([]*model.IMMessage, error) {
	rows, err := d.Query(imMessageSelect+`
		WHERE m.channel_id = ? AND m.id > ? AND m.deleted_at IS NULL
		ORDER BY m.id ASC LIMIT 100`, channelID, afterID)
	if err != nil {
		return nil, err
	}
	return scanIMMessages(rows)
}

// CreateIMMessage inserts a new message and returns its ID.
func (d *DB) CreateIMMessage(channelID, userID int64, content string) (int64, error) {
	res, err := d.Exec(
		`INSERT INTO im_messages (channel_id, user_id, content) VALUES (?, ?, ?)`,
		channelID, userID, content,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DeleteIMMessage soft-deletes a message (only owner can delete).
func (d *DB) DeleteIMMessage(msgID, userID int64) error {
	_, err := d.Exec(
		`UPDATE im_messages SET deleted_at=CURRENT_TIMESTAMP WHERE id=? AND user_id=?`,
		msgID, userID,
	)
	return err
}

// EditIMMessage updates message content (only owner can edit).
func (d *DB) EditIMMessage(msgID, userID int64, content string) error {
	_, err := d.Exec(
		`UPDATE im_messages SET content=?, edited_at=CURRENT_TIMESTAMP WHERE id=? AND user_id=? AND deleted_at IS NULL`,
		content, msgID, userID,
	)
	return err
}

// UpdateIMLastRead updates the user's last-read message ID in a channel.
func (d *DB) UpdateIMLastRead(channelID, userID, msgID int64) error {
	_, err := d.Exec(
		`UPDATE im_channel_members SET last_read_msg_id=MAX(last_read_msg_id, ?) WHERE channel_id=? AND user_id=?`,
		msgID, channelID, userID,
	)
	return err
}

// MaxIMMessageID returns the highest message ID in a channel (0 if empty).
func (d *DB) MaxIMMessageID(channelID int64) (int64, error) {
	var id sql.NullInt64
	err := d.QueryRow(
		`SELECT MAX(id) FROM im_messages WHERE channel_id=? AND deleted_at IS NULL`,
		channelID,
	).Scan(&id)
	return id.Int64, err
}

// CountIMUnreadTotal returns total unread count across all IM channels for a user.
func (d *DB) CountIMUnreadTotal(userID int64) (int, error) {
	var count int
	err := d.QueryRow(`
		SELECT COALESCE(SUM(
			(SELECT COUNT(*) FROM im_messages im
			 WHERE im.channel_id = cm.channel_id AND im.id > cm.last_read_msg_id AND im.deleted_at IS NULL)
		), 0)
		FROM im_channel_members cm WHERE cm.user_id = ?
	`, userID).Scan(&count)
	return count, err
}

// GetIMChannelForUser returns a channel enriched with the current user's membership data.
func (d *DB) GetIMChannelForUser(channelID, userID int64) (*model.IMChannel, error) {
	row := d.QueryRow(`
		SELECT c.id, c.name,
			CASE c.type
				WHEN 'direct' THEN (
					SELECT u2.display_name FROM im_channel_members cm2
					JOIN users u2 ON u2.id = cm2.user_id
					WHERE cm2.channel_id = c.id AND cm2.user_id != ?
					LIMIT 1
				)
				ELSE COALESCE(NULLIF(c.display_name,''), c.name)
			END AS display_name,
			c.description, c.type, c.created_by, c.created_at,
			COALESCE(cm.role, '') AS role,
			COALESCE((
				SELECT COUNT(*) FROM im_messages im
				WHERE im.channel_id = c.id AND im.id > COALESCE(cm.last_read_msg_id,0) AND im.deleted_at IS NULL
			), 0) AS unread_count
		FROM im_channels c
		LEFT JOIN im_channel_members cm ON cm.channel_id = c.id AND cm.user_id = ?
		WHERE c.id = ?
	`, userID, userID, channelID)

	var ch model.IMChannel
	err := row.Scan(&ch.ID, &ch.Name, &ch.DisplayName, &ch.Description,
		&ch.Type, &ch.CreatedBy, &ch.CreatedAt, &ch.MyRole, &ch.UnreadCount)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ch.IsMember = ch.MyRole != ""
	return &ch, nil
}
