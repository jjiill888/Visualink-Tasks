// 用户与会话的数据访问(原 internal/db 的 Users/Sessions 段)。
package auth

import (
	"database/sql"
	"strings"
	"time"

	"visualink/internal/model"
)

// Store 持有用户/会话表的数据访问;跨模块的用户查询都从这里走。
type Store struct {
	*sql.DB
}

func NewStore(db *sql.DB) *Store { return &Store{db} }

// ── Users ──────────────────────────────────────────────────────────────────

func (d *Store) CreateUser(u *model.User) error {
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

func (d *Store) GetUserByUsername(username string) (*model.User, error) {
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

func (d *Store) GetUserByID(id int64) (*model.User, error) {
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
func (d *Store) UsernameDisplayMap(usernames []string) (map[string]string, error) {
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
func (d *Store) DisplayNameUsernameMap(displayNames []string) (map[string]string, error) {
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

func (d *Store) GetUserByDisplayName(displayName string) (*model.User, error) {
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
func (d *Store) MentionDisplayMap(tokens []string) (map[string]string, error) {
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

func (d *Store) UsernameExists(username string) (bool, error) {
	var n int
	err := d.QueryRow(`SELECT COUNT(*) FROM users WHERE username=?`, username).Scan(&n)
	return n > 0, err
}

func (d *Store) EmailExists(email string) (bool, error) {
	var n int
	err := d.QueryRow(`SELECT COUNT(*) FROM users WHERE email=?`, email).Scan(&n)
	return n > 0, err
}

// ── Sessions ───────────────────────────────────────────────────────────────

func (d *Store) CreateSession(token string, userID int64, expires time.Time) error {
	_, err := d.Exec(
		`INSERT INTO sessions (token, user_id, expires_at) VALUES (?,?,?)`,
		token, userID, expires,
	)
	return err
}

func (d *Store) GetSession(token string) (int64, error) {
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

func (d *Store) DeleteSession(token string) error {
	_, err := d.Exec(`DELETE FROM sessions WHERE token=?`, token)
	return err
}

// ListAllUsers returns all users for member-picker UI.
func (d *Store) ListAllUsers() ([]*model.User, error) {
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
