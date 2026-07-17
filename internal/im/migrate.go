package im

import "database/sql"

// Migrate 建立 IM(频道/成员/消息)的表并播种 #general 大厅频道。
// 依赖 auth.Migrate 先建 users。
func Migrate(d *sql.DB) error {
	_, _ = d.Exec(`
	CREATE TABLE IF NOT EXISTS im_channels (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		name         TEXT NOT NULL UNIQUE,
		display_name TEXT NOT NULL DEFAULT '',
		description  TEXT NOT NULL DEFAULT '',
		type         TEXT NOT NULL DEFAULT 'public',
		created_by   INTEGER REFERENCES users(id),
		created_at   DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)

	_, _ = d.Exec(`
	CREATE TABLE IF NOT EXISTS im_channel_members (
		channel_id       INTEGER NOT NULL REFERENCES im_channels(id) ON DELETE CASCADE,
		user_id          INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		role             TEXT NOT NULL DEFAULT 'member',
		last_read_msg_id INTEGER NOT NULL DEFAULT 0,
		joined_at        DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (channel_id, user_id)
	)`)

	_, _ = d.Exec(`
	CREATE TABLE IF NOT EXISTS im_messages (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		channel_id  INTEGER NOT NULL REFERENCES im_channels(id) ON DELETE CASCADE,
		user_id     INTEGER NOT NULL REFERENCES users(id),
		content     TEXT NOT NULL,
		reply_to_id INTEGER REFERENCES im_messages(id),
		edited_at   DATETIME,
		deleted_at  DATETIME,
		created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)

	_, _ = d.Exec(`CREATE INDEX IF NOT EXISTS idx_im_messages_channel ON im_messages(channel_id, id DESC)`)
	_, _ = d.Exec(`CREATE INDEX IF NOT EXISTS idx_im_members_user ON im_channel_members(user_id)`)

	// 播种 #general 频道
	var generalExists int
	_ = d.QueryRow(`SELECT COUNT(*) FROM im_channels WHERE name='general'`).Scan(&generalExists)
	if generalExists == 0 {
		_, _ = d.Exec(`INSERT OR IGNORE INTO im_channels (name, display_name, description, type, created_by) VALUES ('general', '大厅', '团队公共频道', 'public', 0)`)
	}
	return nil
}
