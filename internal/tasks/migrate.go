package tasks

import "database/sql"

// Migrate 建立协作系统(讨论组/功能/评论/事件/订阅/关注)的表。
// 依赖 auth.Migrate 先建 users。
func Migrate(d *sql.DB) error {
	_, err := d.Exec(`
	CREATE TABLE IF NOT EXISTS groups (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		title       TEXT NOT NULL,
		description TEXT DEFAULT '',
		created_by  INTEGER REFERENCES users(id),
		created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS features (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		group_id    INTEGER REFERENCES groups(id),
		title       TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		priority    TEXT NOT NULL DEFAULT 'medium',
		status      TEXT NOT NULL DEFAULT 'pending',
		created_by  INTEGER REFERENCES users(id),
		assigned_to INTEGER REFERENCES users(id),
		created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS comments (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		feature_id INTEGER REFERENCES features(id),
		user_id    INTEGER REFERENCES users(id),
		content    TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS feature_events (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		feature_id  INTEGER NOT NULL REFERENCES features(id) ON DELETE CASCADE,
		operator_id INTEGER NOT NULL REFERENCES users(id),
		action      TEXT NOT NULL,
		old_value   TEXT NOT NULL DEFAULT '',
		new_value   TEXT NOT NULL DEFAULT '',
		created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	`)
	if err != nil {
		return err
	}
	// 老库补列(列已存在时报错,按项目惯例忽略)
	_, _ = d.Exec(`ALTER TABLE comments ADD COLUMN is_deleted INTEGER NOT NULL DEFAULT 0`)

	_, _ = d.Exec(`
	CREATE TABLE IF NOT EXISTS user_group_subscriptions (
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		group_id   INTEGER NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
		type       TEXT NOT NULL DEFAULT 'member',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (user_id, group_id)
	)`)

	_, _ = d.Exec(`
	CREATE TABLE IF NOT EXISTS user_feature_watches (
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		feature_id INTEGER NOT NULL REFERENCES features(id) ON DELETE CASCADE,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (user_id, feature_id)
	)`)

	_, _ = d.Exec(`
	CREATE TABLE IF NOT EXISTS feature_comment_reads (
		user_id              INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		feature_id           INTEGER NOT NULL REFERENCES features(id) ON DELETE CASCADE,
		last_seen_comment_id INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (user_id, feature_id)
	)`)

	return nil
}
