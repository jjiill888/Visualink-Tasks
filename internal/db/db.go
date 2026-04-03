package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"featuretrack/internal/model"

	_ "modernc.org/sqlite"
)

// MarkNotificationReadByID 标记单条通知为已读
func (d *DB) MarkNotificationReadByID(userID, notifID int64) error {
	_, err := d.Exec(
		"UPDATE notifications SET is_read=1 WHERE user_id=? AND id=?",
		userID, notifID,
	)
	return err
}

type DB struct {
	*sql.DB
}

func Open(path string) (*DB, error) {
	sqldb, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	sqldb.SetMaxOpenConns(1) // SQLite is single-writer
	d := &DB{sqldb}
	if err := d.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return d, nil
}

func (d *DB) migrate() error {
	_, err := d.Exec(`
		PRAGMA journal_mode=WAL;
		PRAGMA foreign_keys=ON;
		PRAGMA synchronous=NORMAL;
		PRAGMA cache_size=-8000;
		PRAGMA busy_timeout=5000;
		PRAGMA temp_store=MEMORY;
	`)
	if err != nil {
		return err
	}
	_, err = d.Exec(`
	CREATE TABLE IF NOT EXISTS users (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		username     TEXT UNIQUE NOT NULL,
		display_name TEXT NOT NULL DEFAULT '',
		email        TEXT UNIQUE NOT NULL,
		password     TEXT NOT NULL,
		role         TEXT NOT NULL DEFAULT 'pm',
		created_at   DATETIME DEFAULT CURRENT_TIMESTAMP
	);

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

	CREATE TABLE IF NOT EXISTS sessions (
		token      TEXT PRIMARY KEY,
		user_id    INTEGER REFERENCES users(id),
		expires_at DATETIME NOT NULL
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
	if err := d.migrateNotifications(); err != nil {
		return err
	}
	// For existing databases without display_name: add column and backfill.
	// ALTER TABLE fails if column already exists — error intentionally ignored.
	_, _ = d.Exec(`ALTER TABLE users ADD COLUMN display_name TEXT NOT NULL DEFAULT ''`)
	_, _ = d.Exec(`UPDATE users SET display_name = username WHERE display_name = ''`)

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

func (d *DB) migrateNotifications() error {
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
		return d.rebuildNotificationsTable(hasMessage)
	}
	if !hasMessage {
		_, err := d.Exec(`ALTER TABLE notifications ADD COLUMN message TEXT NOT NULL DEFAULT ''`)
		if err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) rebuildNotificationsTable(hasMessage bool) error {
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

// ── Users ──────────────────────────────────────────────────────────────────

func (d *DB) CreateUser(u *model.User) error {
	if u.DisplayName == "" {
		u.DisplayName = u.Username
	}
	res, err := d.Exec(
		`INSERT INTO users (username, display_name, email, password, role) VALUES (?,?,?,?,?)`,
		u.Username, u.DisplayName, u.Email, u.Password, u.Role,
	)
	if err != nil {
		return err
	}
	u.ID, _ = res.LastInsertId()
	return nil
}

func (d *DB) GetUserByUsername(username string) (*model.User, error) {
	u := &model.User{}
	err := d.QueryRow(
		`SELECT id, username, display_name, email, password, role, created_at FROM users WHERE username=?`,
		username,
	).Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.Password, &u.Role, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return u, err
}

func (d *DB) GetUserByID(id int64) (*model.User, error) {
	u := &model.User{}
	err := d.QueryRow(
		`SELECT id, username, display_name, email, password, role, created_at FROM users WHERE id=?`, id,
	).Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.Password, &u.Role, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return u, err
}

// UsernameDisplayMap returns a username→display_name map for the given usernames.
// Used to resolve @mention handles to display names at render time.
func (d *DB) UsernameDisplayMap(usernames []string) (map[string]string, error) {
	if len(usernames) == 0 {
		return map[string]string{}, nil
	}
	placeholders := make([]string, len(usernames))
	args := make([]any, len(usernames))
	for i, u := range usernames {
		placeholders[i] = "?"
		args[i] = u
	}
	q := `SELECT username, COALESCE(NULLIF(display_name,''), username) FROM users WHERE username IN (` +
		strings.Join(placeholders, ",") + `)`
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var username, dn string
		if err := rows.Scan(&username, &dn); err != nil {
			return nil, err
		}
		m[username] = dn
	}
	return m, rows.Err()
}

// DisplayNameUsernameMap returns a display_name→username map for the given display names.
// Used to resolve @display_name mentions to usernames for notification lookup.
func (d *DB) DisplayNameUsernameMap(displayNames []string) (map[string]string, error) {
	if len(displayNames) == 0 {
		return map[string]string{}, nil
	}
	placeholders := make([]string, len(displayNames))
	args := make([]any, len(displayNames))
	for i, dn := range displayNames {
		placeholders[i] = "?"
		args[i] = dn
	}
	q := `SELECT COALESCE(NULLIF(display_name,''), username), username FROM users WHERE display_name IN (` +
		strings.Join(placeholders, ",") + `)`
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var dn, username string
		if err := rows.Scan(&dn, &username); err != nil {
			return nil, err
		}
		m[dn] = username
	}
	return m, rows.Err()
}

func (d *DB) GetUserByDisplayName(displayName string) (*model.User, error) {
	u := &model.User{}
	err := d.QueryRow(
		`SELECT id, username, display_name, email, password, role, created_at FROM users WHERE display_name=?`,
		displayName,
	).Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.Password, &u.Role, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return u, err
}

// MentionDisplayMap returns token→display_name for tokens matching either username or display_name.
// Used at render time so both @handle and @displayname show as @displayname.
func (d *DB) MentionDisplayMap(tokens []string) (map[string]string, error) {
	if len(tokens) == 0 {
		return map[string]string{}, nil
	}
	ph := make([]string, len(tokens))
	args := make([]any, len(tokens)*2)
	for i, t := range tokens {
		ph[i] = "?"
		args[i] = t
		args[len(tokens)+i] = t
	}
	inClause := strings.Join(ph, ",")
	q := `SELECT username, COALESCE(NULLIF(display_name,''), username) FROM users WHERE username IN (` + inClause + `)
	      UNION
	      SELECT display_name, COALESCE(NULLIF(display_name,''), username) FROM users WHERE display_name IN (` + inClause + `)`
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var token, dn string
		if err := rows.Scan(&token, &dn); err != nil {
			return nil, err
		}
		m[token] = dn
	}
	return m, rows.Err()
}

func (d *DB) UsernameExists(username string) (bool, error) {
	var n int
	err := d.QueryRow(`SELECT COUNT(*) FROM users WHERE username=?`, username).Scan(&n)
	return n > 0, err
}

func (d *DB) EmailExists(email string) (bool, error) {
	var n int
	err := d.QueryRow(`SELECT COUNT(*) FROM users WHERE email=?`, email).Scan(&n)
	return n > 0, err
}

// ── Sessions ───────────────────────────────────────────────────────────────

func (d *DB) CreateSession(token string, userID int64, expires time.Time) error {
	_, err := d.Exec(
		`INSERT INTO sessions (token, user_id, expires_at) VALUES (?,?,?)`,
		token, userID, expires,
	)
	return err
}

func (d *DB) GetSession(token string) (int64, error) {
	var userID int64
	var expiresAt time.Time
	err := d.QueryRow(
		`SELECT user_id, expires_at FROM sessions WHERE token=?`, token,
	).Scan(&userID, &expiresAt)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if time.Now().After(expiresAt) {
		_ = d.DeleteSession(token)
		return 0, nil
	}
	return userID, nil
}

func (d *DB) DeleteSession(token string) error {
	_, err := d.Exec(`DELETE FROM sessions WHERE token=?`, token)
	return err
}

// ── Features ──────────────────────────────────────────────────────────────

const featureCols = `
	f.id, f.group_id, f.title, f.description, f.priority, f.status,
	f.created_by, f.assigned_to, f.created_at, f.updated_at,
	COALESCE(NULLIF(u.display_name,''), u.username), u.role, COALESCE(g.title,'')
`

func scanFeature(row interface {
	Scan(...any) error
}) (*model.Feature, error) {
	f := &model.Feature{}
	err := row.Scan(
		&f.ID, &f.GroupID, &f.Title, &f.Description, &f.Priority, &f.Status,
		&f.CreatedBy, &f.AssignedTo, &f.CreatedAt, &f.UpdatedAt,
		&f.CreatorName, &f.CreatorRole, &f.GroupTitle,
	)
	return f, err
}

// search: 标题/描述模糊搜索
// groupID, assigneeID, creatorID: 精确筛选
// currentUserID: 用于让草稿对创建者可见（传 0 则所有草稿都不可见）
func (d *DB) ListFeatures(currentUserID int64, priority, status, search string, groupID, assigneeID, creatorID *int64) ([]*model.Feature, error) {
	q := `SELECT ` + featureCols + `
	       FROM features f
	       JOIN users u ON u.id = f.created_by
	       LEFT JOIN groups g ON g.id = f.group_id
	       WHERE 1=1`
	args := []any{}
	if priority != "" && priority != "all" {
		q += ` AND f.priority=?`
		args = append(args, priority)
	}
	if status != "" && status != "all" {
		q += ` AND f.status=?`
		args = append(args, status)
	} else {
		// 默认不显示已归档；草稿只对创建者可见
		q += ` AND f.status != 'archived'`
		if currentUserID > 0 {
			q += ` AND (f.status != 'draft' OR f.created_by = ?)`
			args = append(args, currentUserID)
		} else {
			q += ` AND f.status != 'draft'`
		}
	}
	if search != "" {
		q += ` AND (f.title LIKE ? OR f.description LIKE ?)`
		like := "%" + search + "%"
		args = append(args, like, like)
	}
	if groupID != nil && *groupID > 0 {
		q += ` AND f.group_id=?`
		args = append(args, *groupID)
	}
	if assigneeID != nil && *assigneeID > 0 {
		q += ` AND f.assigned_to=?`
		args = append(args, *assigneeID)
	}
	if creatorID != nil && *creatorID > 0 {
		q += ` AND f.created_by=?`
		args = append(args, *creatorID)
	}
	q += ` ORDER BY f.created_at DESC`
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*model.Feature
	for rows.Next() {
		f, err := scanFeature(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, f)
	}
	return list, rows.Err()
}

func (d *DB) ListFeaturesByUser(userID int64) ([]*model.Feature, error) {
	q := `SELECT ` + featureCols + `
		FROM features f
		JOIN users u ON u.id = f.created_by
		LEFT JOIN groups g ON g.id = f.group_id
		WHERE f.created_by=?
		ORDER BY f.created_at DESC`
	rows, err := d.Query(q, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*model.Feature
	for rows.Next() {
		f, err := scanFeature(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, f)
	}
	return list, rows.Err()
}

func (d *DB) GetFeature(id int64) (*model.Feature, error) {
	q := `SELECT ` + featureCols + `
		FROM features f
		JOIN users u ON u.id = f.created_by
		LEFT JOIN groups g ON g.id = f.group_id
		WHERE f.id=?`
	f, err := scanFeature(d.QueryRow(q, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return f, err
}

func (d *DB) CreateFeature(f *model.Feature) error {
	res, err := d.Exec(
		`INSERT INTO features (group_id, title, description, priority, status, created_by)
		 VALUES (?,?,?,?,?,?)`,
		f.GroupID, f.Title, f.Description, f.Priority, f.Status, f.CreatedBy,
	)
	if err != nil {
		return err
	}
	f.ID, _ = res.LastInsertId()
	return nil
}

func (d *DB) DeleteFeature(id int64, createdBy int64) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// comments has no ON DELETE CASCADE, must remove manually before deleting feature
	if _, err := tx.Exec(`DELETE FROM comments WHERE feature_id=?`, id); err != nil {
		return err
	}

	res, err := tx.Exec(
		`DELETE FROM features WHERE id=? AND created_by=? AND (status='pending' OR status='draft')`,
		id, createdBy,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("feature not found or cannot be retracted")
	}
	return tx.Commit()
}

func (d *DB) UpdateFeatureDraft(id, userID int64, title, description, priority string, groupID *int64) error {
	res, err := d.Exec(
		`UPDATE features SET title=?, description=?, priority=?, group_id=?, updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND created_by=? AND status='draft'`,
		title, description, priority, groupID, id, userID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("draft not found or permission denied")
	}
	return nil
}

func (d *DB) UpdateFeatureStatus(id int64, status string) error {
	_, err := d.Exec(
		`UPDATE features SET status=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		status, id,
	)
	return err
}

func (d *DB) GetStats() (*model.Stats, error) {
	s := &model.Stats{}
	err := d.QueryRow(`
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN status='pending'     THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status='in_progress' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status='done'        THEN 1 ELSE 0 END), 0)
		FROM features WHERE status != 'archived' AND status != 'draft'
	`).Scan(&s.Total, &s.Pending, &s.InProgress, &s.Done)
	return s, err
}

// AutoArchiveFeatures 将 done 超过 24h 的功能自动归档，并返回已归档的功能列表。
func (d *DB) AutoArchiveFeatures() ([]*model.Feature, error) {
	tx, err := d.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT ` + featureCols + `
		FROM features f
		JOIN users u ON u.id = f.created_by
		LEFT JOIN groups g ON g.id = f.group_id
		WHERE f.status='done' AND f.updated_at <= datetime('now', '-24 hours')
		ORDER BY f.updated_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var archived []*model.Feature
	for rows.Next() {
		f, err := scanFeature(rows)
		if err != nil {
			return nil, err
		}
		archived = append(archived, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	for _, f := range archived {
		if _, err := tx.Exec(`UPDATE features SET status='archived', updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='done'`, f.ID); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return archived, nil
}

// ── Groups ─────────────────────────────────────────────────────────────────

func (d *DB) ListGroups() ([]*model.Group, error) {
	rows, err := d.Query(`
		SELECT g.id, g.title, g.description, g.created_by, g.created_at,
		       COALESCE(NULLIF(u.display_name,''), u.username),
		       COUNT(f.id)
		FROM groups g
		JOIN users u ON u.id = g.created_by
		LEFT JOIN features f ON f.group_id = g.id
		GROUP BY g.id
		ORDER BY g.created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*model.Group
	for rows.Next() {
		g := &model.Group{}
		if err := rows.Scan(&g.ID, &g.Title, &g.Description, &g.CreatedBy, &g.CreatedAt, &g.CreatorName, &g.FeatureCount); err != nil {
			return nil, err
		}
		list = append(list, g)
	}
	return list, rows.Err()
}

func (d *DB) GetGroup(id int64) (*model.Group, error) {
	g := &model.Group{}
	err := d.QueryRow(`
		SELECT g.id, g.title, g.description, g.created_by, g.created_at, u.username, COUNT(f.id)
		FROM groups g
		JOIN users u ON u.id = g.created_by
		LEFT JOIN features f ON f.group_id = g.id
		WHERE g.id=?
		GROUP BY g.id
	`, id).Scan(&g.ID, &g.Title, &g.Description, &g.CreatedBy, &g.CreatedAt, &g.CreatorName, &g.FeatureCount)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return g, err
}

func (d *DB) CreateGroup(g *model.Group) error {
	res, err := d.Exec(
		`INSERT INTO groups (title, description, created_by) VALUES (?,?,?)`,
		g.Title, g.Description, g.CreatedBy,
	)
	if err != nil {
		return err
	}
	g.ID, _ = res.LastInsertId()
	return nil
}

func (d *DB) ListFeaturesInGroup(groupID int64) ([]*model.Feature, error) {
	q := `SELECT ` + featureCols + `
		FROM features f
		JOIN users u ON u.id = f.created_by
		LEFT JOIN groups g ON g.id = f.group_id
		WHERE f.group_id=? AND f.status != 'draft'
		ORDER BY
			CASE f.priority WHEN 'urgent' THEN 1 WHEN 'high' THEN 2 WHEN 'medium' THEN 3 ELSE 4 END,
			f.created_at DESC`
	rows, err := d.Query(q, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*model.Feature
	for rows.Next() {
		f, err := scanFeature(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, f)
	}
	return list, rows.Err()
}

// ── Comments ───────────────────────────────────────────────────────────────

func (d *DB) ListComments(featureID int64) ([]*model.Comment, error) {
	rows, err := d.Query(`
		SELECT c.id, c.feature_id, c.user_id, c.content, c.created_at,
		       COALESCE(NULLIF(u.display_name,''), u.username), u.role
		FROM comments c
		JOIN users u ON u.id = c.user_id
		WHERE c.feature_id = ?
		ORDER BY c.created_at ASC
	`, featureID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*model.Comment
	for rows.Next() {
		c := &model.Comment{}
		if err := rows.Scan(&c.ID, &c.FeatureID, &c.UserID, &c.Content, &c.CreatedAt, &c.Username, &c.UserRole); err != nil {
			return nil, err
		}
		list = append(list, c)
	}
	return list, rows.Err()
}

// ── Feature Events ─────────────────────────────────────────────────────────

func (d *DB) CreateFeatureEvent(e *model.FeatureEvent) error {
	res, err := d.Exec(
		`INSERT INTO feature_events (feature_id, operator_id, action, old_value, new_value)
		 VALUES (?,?,?,?,?)`,
		e.FeatureID, e.OperatorID, e.Action, e.OldValue, e.NewValue,
	)
	if err != nil {
		return err
	}
	e.ID, _ = res.LastInsertId()
	return nil
}

func (d *DB) ListFeatureEvents(featureID int64) ([]*model.FeatureEvent, error) {
	rows, err := d.Query(`
		SELECT fe.id, fe.feature_id, fe.operator_id, fe.action, fe.old_value, fe.new_value,
		       fe.created_at, COALESCE(NULLIF(u.display_name,''), u.username), u.role
		FROM feature_events fe
		JOIN users u ON u.id = fe.operator_id
		WHERE fe.feature_id = ?
		ORDER BY fe.created_at ASC
	`, featureID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*model.FeatureEvent
	for rows.Next() {
		e := &model.FeatureEvent{}
		if err := rows.Scan(&e.ID, &e.FeatureID, &e.OperatorID, &e.Action, &e.OldValue, &e.NewValue,
			&e.CreatedAt, &e.OperatorName, &e.OperatorRole); err != nil {
			return nil, err
		}
		list = append(list, e)
	}
	return list, rows.Err()
}

// ── Comments ───────────────────────────────────────────────────────────────

func (d *DB) CreateComment(c *model.Comment) error {
	res, err := d.Exec(
		`INSERT INTO comments (feature_id, user_id, content) VALUES (?,?,?)`,
		c.FeatureID, c.UserID, c.Content,
	)
	if err != nil {
		return err
	}
	c.ID, _ = res.LastInsertId()
	return nil
}

// ── Notifications ───────────────────────────────────────────────────────────

func (d *DB) CreateNotification(n *model.Notification) error {
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

func (d *DB) ListUnreadNotifications(userID int64) ([]*model.Notification, error) {
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

func (d *DB) MarkNotificationsReadByFeature(userID, featureID int64) error {
	_, err := d.Exec(
		`UPDATE notifications SET is_read=1 WHERE user_id=? AND feature_id=?`,
		userID, featureID,
	)
	return err
}

func (d *DB) MarkAllNotificationsRead(userID int64) error {
	_, err := d.Exec(`UPDATE notifications SET is_read=1 WHERE user_id=?`, userID)
	return err
}

func (d *DB) CountUnreadNotifications(userID int64) (int, error) {
	var count int
	err := d.QueryRow(`SELECT COUNT(*) FROM notifications WHERE user_id=? AND is_read=0`, userID).Scan(&count)
	return count, err
}

func (d *DB) ListRecentNotifications(userID int64, limit int) ([]*model.Notification, error) {
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

func (d *DB) CreateDirectMessage(senderID, recipientID int64, content string) (*model.DirectMessage, error) {
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

func (d *DB) CountUnreadDirectMessages(userID int64) (int, error) {
	var count int
	err := d.QueryRow(`SELECT COUNT(*) FROM direct_messages WHERE recipient_id=? AND is_read=0`, userID).Scan(&count)
	return count, err
}

func (d *DB) CountUnreadInbox(userID int64) (int, error) {
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

func (d *DB) ListMessageContacts(userID int64, search string) ([]*model.MessageContact, error) {
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

func (d *DB) BuildSystemContact(userID int64) (*model.MessageContact, error) {
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

func (d *DB) ListDirectMessages(userID, partnerID int64, limit int) ([]*model.DirectMessage, error) {
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

func (d *DB) MarkDirectMessagesRead(userID, partnerID int64) error {
	_, err := d.Exec(`UPDATE direct_messages SET is_read=1 WHERE recipient_id=? AND sender_id=? AND is_read=0`, userID, partnerID)
	return err
}

// ── Group subscriptions ────────────────────────────────────────────────────

func (d *DB) GetGroupSubscription(userID, groupID int64) (string, error) {
	var typ string
	err := d.QueryRow(
		`SELECT type FROM user_group_subscriptions WHERE user_id=? AND group_id=?`,
		userID, groupID,
	).Scan(&typ)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return typ, err
}

func (d *DB) UpsertGroupSubscription(userID, groupID int64, typ string) error {
	_, err := d.Exec(
		`INSERT INTO user_group_subscriptions (user_id, group_id, type) VALUES (?,?,?)
		 ON CONFLICT(user_id, group_id) DO UPDATE SET type=excluded.type`,
		userID, groupID, typ,
	)
	return err
}

func (d *DB) DeleteGroupSubscription(userID, groupID int64) error {
	_, err := d.Exec(
		`DELETE FROM user_group_subscriptions WHERE user_id=? AND group_id=?`,
		userID, groupID,
	)
	return err
}

// ListGroupMembers returns all members of a group with their type.
func (d *DB) ListGroupMembers(groupID int64) ([]*model.GroupMember, error) {
	rows, err := d.Query(`
		SELECT u.id, COALESCE(NULLIF(u.display_name,''), u.username), u.role, s.type
		FROM user_group_subscriptions s
		JOIN users u ON u.id = s.user_id
		WHERE s.group_id = ?
		ORDER BY s.type DESC, u.display_name ASC
	`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*model.GroupMember
	for rows.Next() {
		m := &model.GroupMember{}
		if err := rows.Scan(&m.UserID, &m.DisplayName, &m.Role, &m.Type); err != nil {
			return nil, err
		}
		list = append(list, m)
	}
	return list, rows.Err()
}

// ListAllUsers returns all users for member-picker UI.
func (d *DB) ListAllUsers() ([]*model.User, error) {
	rows, err := d.Query(
		`SELECT id, username, display_name, email, password, role, created_at FROM users ORDER BY display_name ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*model.User
	for rows.Next() {
		u := &model.User{}
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.Password, &u.Role, &u.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, u)
	}
	return list, rows.Err()
}

// ── Feature watches ────────────────────────────────────────────────────────

func (d *DB) WatchFeature(userID, featureID int64) error {
	_, err := d.Exec(
		`INSERT OR IGNORE INTO user_feature_watches (user_id, feature_id) VALUES (?,?)`,
		userID, featureID,
	)
	return err
}

func (d *DB) UnwatchFeature(userID, featureID int64) error {
	_, err := d.Exec(
		`DELETE FROM user_feature_watches WHERE user_id=? AND feature_id=?`,
		userID, featureID,
	)
	return err
}

func (d *DB) IsFeatureWatched(userID, featureID int64) (bool, error) {
	var n int
	err := d.QueryRow(
		`SELECT COUNT(*) FROM user_feature_watches WHERE user_id=? AND feature_id=?`,
		userID, featureID,
	).Scan(&n)
	return n > 0, err
}

// ListWatchedFeatures returns features watched by user, newest watch first.
func (d *DB) ListWatchedFeatures(userID int64) ([]*model.Feature, error) {
	q := `SELECT ` + featureCols + `
		FROM features f
		JOIN users u ON u.id = f.created_by
		LEFT JOIN groups g ON g.id = f.group_id
		JOIN user_feature_watches w ON w.feature_id = f.id AND w.user_id = ?
		ORDER BY w.created_at DESC`
	rows, err := d.Query(q, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*model.Feature
	for rows.Next() {
		f, err := scanFeature(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, f)
	}
	return list, rows.Err()
}

// ListSubscribedGroups returns groups the user has joined or is watching.
func (d *DB) ListSubscribedGroups(userID int64) ([]*model.GroupSubscription, error) {
	rows, err := d.Query(`
		SELECT g.id, g.title, s.type
		FROM user_group_subscriptions s
		JOIN groups g ON g.id = s.group_id
		WHERE s.user_id = ?
		ORDER BY s.type DESC, g.title ASC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*model.GroupSubscription
	for rows.Next() {
		gs := &model.GroupSubscription{}
		if err := rows.Scan(&gs.GroupID, &gs.GroupTitle, &gs.Type); err != nil {
			return nil, err
		}
		list = append(list, gs)
	}
	return list, rows.Err()
}

// ListFeaturesPersonal returns features relevant to a user (subscribed groups,
// commented, created, or watched), with watched ones sorted first.
func (d *DB) ListFeaturesPersonal(userID int64, priority, status, search string) ([]*model.Feature, error) {
	q := `SELECT ` + featureCols + `,
		CASE WHEN w.feature_id IS NOT NULL THEN 1 ELSE 0 END AS is_watched
		FROM features f
		JOIN users u ON u.id = f.created_by
		LEFT JOIN groups g ON g.id = f.group_id
		LEFT JOIN user_feature_watches w ON w.feature_id = f.id AND w.user_id = ?
		WHERE f.status != 'archived'
		AND (f.status != 'draft' OR f.created_by = ?)
		AND (
			f.group_id IN (SELECT group_id FROM user_group_subscriptions WHERE user_id = ?)
			OR f.id IN (SELECT DISTINCT feature_id FROM comments WHERE user_id = ?)
			OR f.created_by = ?
			OR w.feature_id IS NOT NULL
		)`
	args := []any{userID, userID, userID, userID, userID}
	if priority != "" && priority != "all" {
		q += ` AND f.priority=?`
		args = append(args, priority)
	}
	if status != "" && status != "all" {
		q += ` AND f.status=?`
		args = append(args, status)
	}
	if search != "" {
		q += ` AND (f.title LIKE ? OR f.description LIKE ?)`
		like := "%" + search + "%"
		args = append(args, like, like)
	}
	q += ` ORDER BY is_watched DESC, f.created_at DESC`

	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*model.Feature
	for rows.Next() {
		f := &model.Feature{}
		var isWatched int
		if err := rows.Scan(
			&f.ID, &f.GroupID, &f.Title, &f.Description, &f.Priority, &f.Status,
			&f.CreatedBy, &f.AssignedTo, &f.CreatedAt, &f.UpdatedAt,
			&f.CreatorName, &f.CreatorRole, &f.GroupTitle,
			&isWatched,
		); err != nil {
			return nil, err
		}
		f.IsWatched = isWatched == 1
		list = append(list, f)
	}
	return list, rows.Err()
}

// ListFeaturesWithWatch wraps ListFeatures and annotates IsWatched for a user.
func (d *DB) ListFeaturesWithWatch(userID int64, priority, status, search string, groupID, assigneeID, creatorID *int64) ([]*model.Feature, error) {
	features, err := d.ListFeatures(userID, priority, status, search, groupID, assigneeID, creatorID)
	if err != nil {
		return nil, err
	}
	if len(features) == 0 {
		return features, nil
	}
	// fetch watched IDs in one query
	ids := make([]any, len(features)+1)
	ids[0] = userID
	ph := make([]string, len(features))
	for i, f := range features {
		ids[i+1] = f.ID
		ph[i] = "?"
	}
	watched := map[int64]bool{}
	rows, err := d.Query(
		`SELECT feature_id FROM user_feature_watches WHERE user_id=? AND feature_id IN (`+strings.Join(ph, ",")+`)`,
		ids...,
	)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var fid int64
			_ = rows.Scan(&fid)
			watched[fid] = true
		}
	}
	// sort: watched first, then original order (stable)
	var pinned, rest []*model.Feature
	for _, f := range features {
		f.IsWatched = watched[f.ID]
		if f.IsWatched {
			pinned = append(pinned, f)
		} else {
			rest = append(rest, f)
		}
	}
	return append(pinned, rest...), nil
}
