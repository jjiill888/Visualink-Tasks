// 功能/讨论组/评论/事件/关注/订阅的数据访问(原 internal/db 主体)。
package tasks

import (
	"database/sql"
	"fmt"

	"visualink/internal/model"
)

// Repo 持有协作系统(功能看板/讨论组)全部表的数据访问。
type Repo struct {
	*sql.DB
}

func NewRepo(db *sql.DB) *Repo { return &Repo{db} }

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
// limit/offset: 分页参数，limit=0 表示不限制
func (d *Repo) ListFeatures(currentUserID int64, priority, status, search string, groupID, assigneeID, creatorID *int64, limit, offset int) ([]*model.Feature, error) {
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
	if limit > 0 {
		q += ` LIMIT ? OFFSET ?`
		args = append(args, limit, offset)
	}
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

func (d *Repo) ListFeaturesByUser(userID int64) ([]*model.Feature, error) {
	q := `SELECT ` + featureCols + `,
		CASE WHEN EXISTS (
			SELECT 1 FROM comments c
			WHERE c.feature_id = f.id AND c.is_deleted = 0
			AND c.id > COALESCE((
				SELECT fcr.last_seen_comment_id FROM feature_comment_reads fcr
				WHERE fcr.user_id = ? AND fcr.feature_id = f.id
			), 0)
		) THEN 1 ELSE 0 END AS has_unread_comments
		FROM features f
		JOIN users u ON u.id = f.created_by
		LEFT JOIN groups g ON g.id = f.group_id
		WHERE f.created_by=?
		ORDER BY f.created_at DESC`
	rows, err := d.Query(q, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*model.Feature
	for rows.Next() {
		f := &model.Feature{}
		var isUnread int
		if err := rows.Scan(
			&f.ID, &f.GroupID, &f.Title, &f.Description, &f.Priority, &f.Status,
			&f.CreatedBy, &f.AssignedTo, &f.CreatedAt, &f.UpdatedAt,
			&f.CreatorName, &f.CreatorRole, &f.GroupTitle,
			&isUnread,
		); err != nil {
			return nil, err
		}
		f.HasUnreadComments = isUnread == 1
		list = append(list, f)
	}
	return list, rows.Err()
}

func (d *Repo) GetFeature(id int64) (*model.Feature, error) {
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

func (d *Repo) CreateFeature(f *model.Feature) error {
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

func (d *Repo) DeleteFeature(id int64, createdBy int64) error {
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

func (d *Repo) UpdateFeatureDraft(id, userID int64, title, description, priority string, groupID *int64) error {
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

func (d *Repo) UpdateFeatureContent(id, userID int64, title, description, priority string, groupID *int64) error {
	res, err := d.Exec(
		`UPDATE features SET title=?, description=?, priority=?, group_id=?, updated_at=CURRENT_TIMESTAMP
		 WHERE id=? AND created_by=? AND (status='pending' OR status='rejected')`,
		title, description, priority, groupID, id, userID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("feature not found or permission denied")
	}
	return nil
}

func (d *Repo) UpdateFeatureStatus(id int64, status string) error {
	_, err := d.Exec(
		`UPDATE features SET status=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		status, id,
	)
	return err
}

func (d *Repo) GetStats() (*model.Stats, error) {
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
func (d *Repo) AutoArchiveFeatures() ([]*model.Feature, error) {
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

func (d *Repo) ListGroups() ([]*model.Group, error) {
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

func (d *Repo) GetGroup(id int64) (*model.Group, error) {
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

func (d *Repo) CreateGroup(g *model.Group) error {
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

func (d *Repo) ListFeaturesInGroup(groupID int64) ([]*model.Feature, error) {
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

func (d *Repo) ListComments(featureID int64) ([]*model.Comment, error) {
	rows, err := d.Query(`
		SELECT c.id, c.feature_id, c.user_id, c.content, c.created_at,
		       COALESCE(NULLIF(u.display_name,''), u.username), u.role
		FROM comments c
		JOIN users u ON u.id = c.user_id
		WHERE c.feature_id = ? AND c.is_deleted = 0
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

func (d *Repo) CreateFeatureEvent(e *model.FeatureEvent) error {
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

func (d *Repo) ListFeatureEvents(featureID int64) ([]*model.FeatureEvent, error) {
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

func (d *Repo) CreateComment(c *model.Comment) error {
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

// MarkCommentsRead records the max non-deleted comment ID seen by a user on a feature.
func (d *Repo) MarkCommentsRead(userID, featureID int64) error {
	_, err := d.Exec(`
		INSERT INTO feature_comment_reads (user_id, feature_id, last_seen_comment_id)
		SELECT ?, ?, COALESCE(MAX(id), 0) FROM comments WHERE feature_id = ? AND is_deleted = 0
		ON CONFLICT(user_id, feature_id) DO UPDATE SET
			last_seen_comment_id = MAX(last_seen_comment_id, excluded.last_seen_comment_id)
	`, userID, featureID, featureID)
	return err
}

// HasUnreadComments reports whether a feature the user created has comments they haven't seen yet.
func (d *Repo) HasUnreadComments(userID, featureID int64) (bool, error) {
	var count int
	err := d.QueryRow(`
		SELECT COUNT(*) FROM comments
		WHERE feature_id = ? AND is_deleted = 0
		AND ? = (SELECT created_by FROM features WHERE id = ?)
		AND id > COALESCE((
			SELECT last_seen_comment_id FROM feature_comment_reads
			WHERE user_id = ? AND feature_id = ?
		), 0)
	`, featureID, userID, featureID, userID, featureID).Scan(&count)
	return count > 0, err
}

// DeleteComment soft-deletes a comment. Only the comment owner or an admin may delete.
func (d *Repo) DeleteComment(commentID, userID int64, isAdmin bool) (bool, error) {
	var res sql.Result
	var err error
	if isAdmin {
		res, err = d.Exec(`UPDATE comments SET is_deleted = 1 WHERE id = ? AND is_deleted = 0`, commentID)
	} else {
		res, err = d.Exec(`UPDATE comments SET is_deleted = 1 WHERE id = ? AND user_id = ? AND is_deleted = 0`, commentID, userID)
	}
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ── Group subscriptions ────────────────────────────────────────────────────

func (d *Repo) GetGroupSubscription(userID, groupID int64) (string, error) {
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

func (d *Repo) UpsertGroupSubscription(userID, groupID int64, typ string) error {
	_, err := d.Exec(
		`INSERT INTO user_group_subscriptions (user_id, group_id, type) VALUES (?,?,?)
		 ON CONFLICT(user_id, group_id) DO UPDATE SET type=excluded.type`,
		userID, groupID, typ,
	)
	return err
}

func (d *Repo) DeleteGroupSubscription(userID, groupID int64) error {
	_, err := d.Exec(
		`DELETE FROM user_group_subscriptions WHERE user_id=? AND group_id=?`,
		userID, groupID,
	)
	return err
}

// ListGroupMembers returns all members of a group with their type.
func (d *Repo) ListGroupMembers(groupID int64) ([]*model.GroupMember, error) {
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

// ── Feature watches ────────────────────────────────────────────────────────

func (d *Repo) WatchFeature(userID, featureID int64) error {
	_, err := d.Exec(
		`INSERT OR IGNORE INTO user_feature_watches (user_id, feature_id) VALUES (?,?)`,
		userID, featureID,
	)
	return err
}

func (d *Repo) UnwatchFeature(userID, featureID int64) error {
	_, err := d.Exec(
		`DELETE FROM user_feature_watches WHERE user_id=? AND feature_id=?`,
		userID, featureID,
	)
	return err
}

func (d *Repo) IsFeatureWatched(userID, featureID int64) (bool, error) {
	var n int
	err := d.QueryRow(
		`SELECT COUNT(*) FROM user_feature_watches WHERE user_id=? AND feature_id=?`,
		userID, featureID,
	).Scan(&n)
	return n > 0, err
}

// ListWatchedFeatures returns features watched by user, newest watch first.
func (d *Repo) ListWatchedFeatures(userID int64) ([]*model.Feature, error) {
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
func (d *Repo) ListSubscribedGroups(userID int64) ([]*model.GroupSubscription, error) {
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
func (d *Repo) ListFeaturesPersonal(userID int64, priority, status, search string, limit, offset int) ([]*model.Feature, error) {
	q := `SELECT ` + featureCols + `,
		CASE WHEN w.feature_id IS NOT NULL THEN 1 ELSE 0 END AS is_watched,
		CASE WHEN f.created_by = ? AND EXISTS (
			SELECT 1 FROM comments c
			WHERE c.feature_id = f.id AND c.is_deleted = 0
			AND c.id > COALESCE((
				SELECT fcr.last_seen_comment_id FROM feature_comment_reads fcr
				WHERE fcr.user_id = ? AND fcr.feature_id = f.id
			), 0)
		) THEN 1 ELSE 0 END AS has_unread_comments
		FROM features f
		JOIN users u ON u.id = f.created_by
		LEFT JOIN groups g ON g.id = f.group_id
		LEFT JOIN user_feature_watches w ON w.feature_id = f.id AND w.user_id = ?
		WHERE f.status != 'archived'
		AND (f.status != 'draft' OR f.created_by = ?)
		AND (
			f.group_id IN (SELECT group_id FROM user_group_subscriptions WHERE user_id = ?)
			OR f.id IN (SELECT DISTINCT feature_id FROM comments WHERE user_id = ? AND is_deleted = 0)
			OR f.created_by = ?
			OR w.feature_id IS NOT NULL
		)`
	args := []any{userID, userID, userID, userID, userID, userID, userID}
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
	if limit > 0 {
		q += ` LIMIT ? OFFSET ?`
		args = append(args, limit, offset)
	}

	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*model.Feature
	for rows.Next() {
		f := &model.Feature{}
		var isWatched, isUnread int
		if err := rows.Scan(
			&f.ID, &f.GroupID, &f.Title, &f.Description, &f.Priority, &f.Status,
			&f.CreatedBy, &f.AssignedTo, &f.CreatedAt, &f.UpdatedAt,
			&f.CreatorName, &f.CreatorRole, &f.GroupTitle,
			&isWatched, &isUnread,
		); err != nil {
			return nil, err
		}
		f.IsWatched = isWatched == 1
		f.HasUnreadComments = isUnread == 1
		list = append(list, f)
	}
	return list, rows.Err()
}

// ListFeaturesWithWatch does a single SQL query that joins user_feature_watches,
// sorts watched items first, and supports pagination via limit/offset.
func (d *Repo) ListFeaturesWithWatch(userID int64, priority, status, search string, groupID, assigneeID, creatorID *int64, limit, offset int) ([]*model.Feature, error) {
	q := `SELECT ` + featureCols + `,
		CASE WHEN w.feature_id IS NOT NULL THEN 1 ELSE 0 END AS is_watched,
		CASE WHEN f.created_by = ? AND EXISTS (
			SELECT 1 FROM comments c
			WHERE c.feature_id = f.id AND c.is_deleted = 0
			AND c.id > COALESCE((
				SELECT fcr.last_seen_comment_id FROM feature_comment_reads fcr
				WHERE fcr.user_id = ? AND fcr.feature_id = f.id
			), 0)
		) THEN 1 ELSE 0 END AS has_unread_comments
		FROM features f
		JOIN users u ON u.id = f.created_by
		LEFT JOIN groups g ON g.id = f.group_id
		LEFT JOIN user_feature_watches w ON w.feature_id = f.id AND w.user_id = ?
		WHERE 1=1`
	args := []any{userID, userID, userID}
	if priority != "" && priority != "all" {
		q += ` AND f.priority=?`
		args = append(args, priority)
	}
	if status != "" && status != "all" {
		q += ` AND f.status=?`
		args = append(args, status)
	} else {
		q += ` AND f.status != 'archived'`
		if userID > 0 {
			q += ` AND (f.status != 'draft' OR f.created_by = ?)`
			args = append(args, userID)
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
	q += ` ORDER BY is_watched DESC, f.created_at DESC`
	if limit > 0 {
		q += ` LIMIT ? OFFSET ?`
		args = append(args, limit, offset)
	}
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*model.Feature
	for rows.Next() {
		f := &model.Feature{}
		var isWatched, isUnread int
		if err := rows.Scan(
			&f.ID, &f.GroupID, &f.Title, &f.Description, &f.Priority, &f.Status,
			&f.CreatedBy, &f.AssignedTo, &f.CreatedAt, &f.UpdatedAt,
			&f.CreatorName, &f.CreatorRole, &f.GroupTitle,
			&isWatched, &isUnread,
		); err != nil {
			return nil, err
		}
		f.IsWatched = isWatched == 1
		f.HasUnreadComments = isUnread == 1
		list = append(list, f)
	}
	return list, rows.Err()
}
