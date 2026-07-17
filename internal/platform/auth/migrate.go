package auth

import "database/sql"

// Migrate 建立用户与会话表。必须最先执行——全系统的外键都指向 users。
func Migrate(d *sql.DB) error {
	_, err := d.Exec(`
	CREATE TABLE IF NOT EXISTS users (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		username     TEXT UNIQUE NOT NULL,
		display_name TEXT NOT NULL DEFAULT '',
		email        TEXT UNIQUE NOT NULL,
		password     TEXT NOT NULL,
		role         TEXT NOT NULL DEFAULT 'pm',
		created_at   DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS sessions (
		token      TEXT PRIMARY KEY,
		user_id    INTEGER REFERENCES users(id),
		expires_at DATETIME NOT NULL
	);
	`)
	if err != nil {
		return err
	}
	// 老库补列(列已存在时报错,按项目惯例忽略)并回填
	_, _ = d.Exec(`ALTER TABLE users ADD COLUMN display_name TEXT NOT NULL DEFAULT ''`)
	_, _ = d.Exec(`UPDATE users SET display_name = username WHERE display_name = ''`)
	return nil
}
