package db

import (
	"database/sql"
	"fmt"
	"time"

	"featuretrack/internal/model"

	_ "modernc.org/sqlite"
)

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

	CREATE TABLE IF NOT EXISTS users (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		username   TEXT UNIQUE NOT NULL,
		email      TEXT UNIQUE NOT NULL,
		password   TEXT NOT NULL,
		role       TEXT NOT NULL DEFAULT 'pm',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
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
	`)
	return err
}

// ── Users ──────────────────────────────────────────────────────────────────

func (d *DB) CreateUser(u *model.User) error {
	res, err := d.Exec(
		`INSERT INTO users (username, email, password, role) VALUES (?,?,?,?)`,
		u.Username, u.Email, u.Password, u.Role,
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
		`SELECT id, username, email, password, role, created_at FROM users WHERE username=?`,
		username,
	).Scan(&u.ID, &u.Username, &u.Email, &u.Password, &u.Role, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return u, err
}

func (d *DB) GetUserByID(id int64) (*model.User, error) {
	u := &model.User{}
	err := d.QueryRow(
		`SELECT id, username, email, password, role, created_at FROM users WHERE id=?`, id,
	).Scan(&u.ID, &u.Username, &u.Email, &u.Password, &u.Role, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return u, err
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
	u.username, u.role, COALESCE(g.title,'')
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
func (d *DB) ListFeatures(priority, status, search string, groupID, assigneeID, creatorID *int64) ([]*model.Feature, error) {
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
	res, err := d.Exec(
		`DELETE FROM features WHERE id=? AND created_by=? AND status='pending'`,
		id, createdBy,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("feature not found or cannot be retracted")
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
		FROM features
	`).Scan(&s.Total, &s.Pending, &s.InProgress, &s.Done)
	return s, err
}

// ── Groups ─────────────────────────────────────────────────────────────────

func (d *DB) ListGroups() ([]*model.Group, error) {
	rows, err := d.Query(`
		SELECT g.id, g.title, g.description, g.created_by, g.created_at,
		       u.username,
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
		WHERE f.group_id=?
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
		SELECT c.id, c.feature_id, c.user_id, c.content, c.created_at, u.username, u.role
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
		       fe.created_at, u.username, u.role
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
