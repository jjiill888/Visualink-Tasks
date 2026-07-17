package notification

import "database/sql"

// Migrate 建立通知与私信的表。依赖 auth/tasks 先建 users/features/comments
// (notifications 外键指向它们)。
func Migrate(d *sql.DB) error {
	_, err := d.Exec(`
	CREATE TABLE IF NOT EXISTS notifications (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id       INTEGER NOT NULL REFERENCES users(id),
		feature_id    INTEGER NOT NULL REFERENCES features(id) ON DELETE CASCADE,
		comment_id    INTEGER REFERENCES comments(id) ON DELETE CASCADE,
		from_user     TEXT NOT NULL DEFAULT '',
		feature_title TEXT NOT NULL DEFAULT '',
		message       TEXT NOT NULL DEFAULT '',
		is_read       INTEGER NOT NULL DEFAULT 0,
		created_at    DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	`)
	if err != nil {
		return err
	}
	if err := migrateNotifications(d); err != nil {
		return err
	}

	_, _ = d.Exec(`
	CREATE TABLE IF NOT EXISTS direct_messages (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		sender_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		recipient_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		content      TEXT NOT NULL,
		is_read      INTEGER NOT NULL DEFAULT 0,
		created_at   DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	_, _ = d.Exec(`CREATE INDEX IF NOT EXISTS idx_direct_messages_pair ON direct_messages(sender_id, recipient_id, created_at DESC)`)
	_, _ = d.Exec(`CREATE INDEX IF NOT EXISTS idx_direct_messages_recipient_read ON direct_messages(recipient_id, is_read, created_at DESC)`)
	return nil
}
