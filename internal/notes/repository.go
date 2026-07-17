package notes

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"visualink/internal/model"
)

// Repo 持有云笔记(正文/FTS/分组/分享/修订/附件)全部表的数据访问。
type Repo struct {
	*sql.DB
}

func NewRepo(db *sql.DB) *Repo { return &Repo{db} }

// ErrNoteConflict 表示乐观锁校验失败：客户端打开笔记后，其他人已保存过新版本。
var ErrNoteConflict = errors.New("note updated by someone else")

// migrateNotes 建立云笔记相关表。遵循项目惯例：CREATE TABLE IF NOT EXISTS 幂等建表。
// notes.updated_at 用毫秒精度文本（strftime %f），既做排序键也做乐观锁 token。
func Migrate(d *sql.DB) error {
	// FTS5 trigram 分词器探测：笔记以中文为主，unicode61 无法按子串命中中文，
	// trigram（SQLite >= 3.34 内置）是唯一开箱可用的中文子串方案。
	// 若编译进来的 SQLite 不支持，这里会显式报错阻止启动，而不是静默降级。
	if _, err := d.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS notes_fts_probe USING fts5(x, tokenize='trigram')`); err != nil {
		return fmt.Errorf("当前 SQLite 不支持 FTS5 trigram 分词器（笔记中文搜索依赖它）: %w", err)
	}
	if _, err := d.Exec(`DROP TABLE IF EXISTS notes_fts_probe`); err != nil {
		return err
	}

	_, err := d.Exec(`
	CREATE TABLE IF NOT EXISTS notes (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		title      TEXT NOT NULL DEFAULT '',
		content_md TEXT NOT NULL DEFAULT '',
		owner_id   INTEGER NOT NULL REFERENCES users(id),
		updated_by INTEGER NOT NULL DEFAULT 0,
		is_private INTEGER NOT NULL DEFAULT 0,
		visibility TEXT NOT NULL DEFAULT 'public',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
		deleted_at DATETIME
	);

	CREATE INDEX IF NOT EXISTS idx_notes_updated ON notes(updated_at DESC);

	CREATE TABLE IF NOT EXISTS note_shares (
		note_id    INTEGER NOT NULL REFERENCES notes(id) ON DELETE CASCADE,
		user_id    INTEGER NOT NULL REFERENCES users(id),
		role       TEXT NOT NULL CHECK (role IN ('editor','reader')),
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (note_id, user_id)
	);

	CREATE INDEX IF NOT EXISTS idx_note_shares_user ON note_shares(user_id);

	CREATE TABLE IF NOT EXISTS note_revisions (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		note_id    INTEGER NOT NULL REFERENCES notes(id) ON DELETE CASCADE,
		content_md TEXT NOT NULL,
		saved_by   INTEGER NOT NULL REFERENCES users(id),
		saved_at   DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_note_revisions_note ON note_revisions(note_id, id DESC);

	CREATE TABLE IF NOT EXISTS note_attachments (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		note_id     INTEGER NOT NULL REFERENCES notes(id) ON DELETE CASCADE,
		filename    TEXT NOT NULL,
		stored_path TEXT NOT NULL,
		size        INTEGER NOT NULL DEFAULT 0,
		created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_note_attachments_note ON note_attachments(note_id);

	CREATE TABLE IF NOT EXISTS note_groups (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		owner_id   INTEGER NOT NULL REFERENCES users(id),
		name       TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	`)
	if err != nil {
		return err
	}

	// 老库补列（列已存在时报错，按项目惯例忽略），并把旧的 is_private 开关
	// 一次性并入 visibility：迁移后 is_private 归零，重启不会把 owner 后来
	// 改回公开的笔记再翻回私有。
	_, _ = d.Exec(`ALTER TABLE notes ADD COLUMN visibility TEXT NOT NULL DEFAULT 'public'`)
	// 文档组归属列（0/NULL = 未分组；组是纯组织结构，权限仍看 visibility）
	_, _ = d.Exec(`ALTER TABLE notes ADD COLUMN group_id INTEGER`)
	if _, err := d.Exec(`UPDATE notes SET visibility='private', is_private=0 WHERE is_private=1`); err != nil {
		return err
	}

	// external-content FTS：notes_fts 不重复存正文，rowid 即 notes.id，
	// 由下面三个 trigger 保持与 notes(title, content_md) 同步。
	_, err = d.Exec(`
	CREATE VIRTUAL TABLE IF NOT EXISTS notes_fts USING fts5(
		title, content_md,
		content='notes', content_rowid='id',
		tokenize='trigram'
	);

	CREATE TRIGGER IF NOT EXISTS notes_fts_ai AFTER INSERT ON notes BEGIN
		INSERT INTO notes_fts(rowid, title, content_md) VALUES (new.id, new.title, new.content_md);
	END;

	CREATE TRIGGER IF NOT EXISTS notes_fts_ad AFTER DELETE ON notes BEGIN
		INSERT INTO notes_fts(notes_fts, rowid, title, content_md) VALUES ('delete', old.id, old.title, old.content_md);
	END;

	CREATE TRIGGER IF NOT EXISTS notes_fts_au AFTER UPDATE OF title, content_md ON notes BEGIN
		INSERT INTO notes_fts(notes_fts, rowid, title, content_md) VALUES ('delete', old.id, old.title, old.content_md);
		INSERT INTO notes_fts(rowid, title, content_md) VALUES (new.id, new.title, new.content_md);
	END;
	`)
	return err
}

// noteCols 依赖两个约定：?1 恒为查询者 userID；FROM 里挂上
// noteShareJoin（按查询者 LEFT JOIN note_shares）。MyAccess 在 SQL 里算好：
// owner 全权；私有仅 owner；公开人人可写；受限按名单 role 给读/写。
const noteCols = `
	n.id, n.title, n.content_md, n.owner_id, n.visibility, COALESCE(n.group_id, 0), n.created_at, n.updated_at,
	COALESCE(NULLIF(o.display_name,''), o.username),
	COALESCE((SELECT COALESCE(NULLIF(u2.display_name,''), u2.username) FROM users u2 WHERE u2.id = n.updated_by), ''),
	COALESCE(g.name, ''),
	CASE
		WHEN n.owner_id = ?1 THEN 'owner'
		WHEN n.visibility = 'private' THEN 'none'
		WHEN n.visibility = 'public' THEN 'edit'
		WHEN s.role = 'editor' THEN 'edit'
		WHEN s.role = 'reader' THEN 'read'
		ELSE 'none'
	END
`

// noteShareJoin 挂名单（算 MyAccess）与文档组名，与 noteCols 配套使用。
const noteShareJoin = ` LEFT JOIN note_shares s ON s.note_id = n.id AND s.user_id = ?1
	LEFT JOIN note_groups g ON g.id = n.group_id`

func scanNote(row interface{ Scan(...any) error }) (*model.Note, error) {
	n := &model.Note{}
	err := row.Scan(
		&n.ID, &n.Title, &n.ContentMD, &n.OwnerID, &n.Visibility, &n.GroupID, &n.CreatedAt, &n.UpdatedAt,
		&n.OwnerName, &n.UpdaterName, &n.GroupName, &n.MyAccess,
	)
	return n, err
}

// ftsQuote 把用户输入包成 FTS5 短语查询，避免 AND/OR/NEAR 等语法被解释。
func ftsQuote(q string) string {
	return `"` + strings.ReplaceAll(q, `"`, `""`) + `"`
}

// ListNotes 返回当前用户可见的笔记（公开 + 自己的 + 名单内的受限），
// 按更新时间倒序。search 非空时走 FTS；trigram 要求至少 3 个字符才能命中，
// 少于 3 个字符（常见的双字中文词）退化为 LIKE 子串匹配。
func (d *Repo) ListNotes(userID int64, search string) ([]*model.Note, error) {
	base := `SELECT ` + noteCols + `
		FROM notes n
		JOIN users o ON o.id = n.owner_id` + noteShareJoin + `
		WHERE n.deleted_at IS NULL
		  AND (n.owner_id = ?1 OR n.visibility = 'public'
		       OR (n.visibility = 'restricted' AND s.user_id IS NOT NULL))`
	args := []any{userID}

	search = strings.TrimSpace(search)
	if search != "" {
		if len([]rune(search)) >= 3 {
			base += ` AND n.id IN (SELECT rowid FROM notes_fts WHERE notes_fts MATCH ?)`
			args = append(args, ftsQuote(search))
		} else {
			base += ` AND (n.title LIKE ? OR n.content_md LIKE ?)`
			like := "%" + search + "%"
			args = append(args, like, like)
		}
	}
	base += ` ORDER BY n.updated_at DESC LIMIT 200`

	rows, err := d.Query(base, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*model.Note
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, n)
	}
	return list, rows.Err()
}

// GetNote 返回未删除的笔记（MyAccess 按 userID 算好）；不存在或已软删除
// 返回 nil。是否放行由 handler 依据 MyAccess 判断。
func (d *Repo) GetNote(id, userID int64) (*model.Note, error) {
	q := `SELECT ` + noteCols + `
		FROM notes n
		JOIN users o ON o.id = n.owner_id` + noteShareJoin + `
		WHERE n.id = ? AND n.deleted_at IS NULL`
	n, err := scanNote(d.QueryRow(q, userID, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return n, err
}

// CreateNote 建空笔记；groupID > 0 时直接落进该文档组（组归属校验在
// handler），visibility 由 handler 校验合法档位（分区头「＋」新建的文档
// 直接落在对应可见性分区）。
func (d *Repo) CreateNote(ownerID int64, title string, groupID int64, visibility string) (int64, error) {
	res, err := d.Exec(
		`INSERT INTO notes (title, owner_id, updated_by, group_id, visibility) VALUES (?,?,?,NULLIF(?,0),?)`,
		title, ownerID, ownerID, groupID, visibility,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SaveNote 保存标题与正文，事务内完成：
//  1. 乐观锁：baseUpdatedAt 非空且与当前 updated_at 不一致 → ErrNoteConflict
//     （阶段二协作模式下由前端传空串跳过检查）
//  2. 历史版本：正文有变化，且距最近一条 revision 超过 5 分钟（或还没有
//     revision）时，把新正文快照存一条 revision，并裁剪到每篇最多 100 条
//  3. 更新 notes 行（FTS 由 trigger 自动同步），返回新的 updated_at
//
// 可见性/名单不在保存路径里改，见下方 Permissions 一节的专用方法。
func (d *Repo) SaveNote(id, userID int64, title, contentMD, baseUpdatedAt string) (string, error) {
	tx, err := d.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	var curContent, curUpdatedAt string
	err = tx.QueryRow(
		`SELECT content_md, updated_at FROM notes WHERE id=? AND deleted_at IS NULL`, id,
	).Scan(&curContent, &curUpdatedAt)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("note not found")
	}
	if err != nil {
		return "", err
	}
	if baseUpdatedAt != "" && baseUpdatedAt != curUpdatedAt {
		return "", ErrNoteConflict
	}

	if contentMD != curContent {
		var recentCount int
		if err := tx.QueryRow(
			`SELECT COUNT(*) FROM note_revisions
			 WHERE note_id=? AND saved_at > datetime('now','-5 minutes')`, id,
		).Scan(&recentCount); err != nil {
			return "", err
		}
		if recentCount == 0 {
			if _, err := tx.Exec(
				`INSERT INTO note_revisions (note_id, content_md, saved_by) VALUES (?,?,?)`,
				id, contentMD, userID,
			); err != nil {
				return "", err
			}
			if _, err := tx.Exec(
				`DELETE FROM note_revisions WHERE note_id=? AND id NOT IN
				 (SELECT id FROM note_revisions WHERE note_id=? ORDER BY id DESC LIMIT 100)`,
				id, id,
			); err != nil {
				return "", err
			}
		}
	}

	var newUpdatedAt string
	err = tx.QueryRow(
		`UPDATE notes SET title=?, content_md=?, updated_by=?,
		        updated_at=strftime('%Y-%m-%d %H:%M:%f','now')
		 WHERE id=? RETURNING updated_at`,
		title, contentMD, userID, id,
	).Scan(&newUpdatedAt)
	if err != nil {
		return "", err
	}
	return newUpdatedAt, tx.Commit()
}

// SoftDeleteNote 软删除，仅 owner 可操作。同时从 FTS 索引移除（软删除不触发
// notes 表的 DELETE trigger，这里手动清索引）。
func (d *Repo) SoftDeleteNote(id, ownerID int64) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`UPDATE notes SET deleted_at=CURRENT_TIMESTAMP WHERE id=? AND owner_id=? AND deleted_at IS NULL`,
		id, ownerID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("note not found or permission denied")
	}
	if _, err := tx.Exec(
		`INSERT INTO notes_fts(notes_fts, rowid, title, content_md)
		 SELECT 'delete', id, title, content_md FROM notes WHERE id=?`, id,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// ── Note groups（文档组：侧栏文件夹，纯组织结构不承载权限） ─────────────────

// CreateNoteGroup 建文档组（name 由 handler 校验非空/限长）。
func (d *Repo) CreateNoteGroup(ownerID int64, name string) (int64, error) {
	res, err := d.Exec(`INSERT INTO note_groups (owner_id, name) VALUES (?,?)`, ownerID, name)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DeleteNoteGroup 删组（仅 owner）：事务内先把组内笔记回到未分组，再删组行。
// 不删除任何笔记。
func (d *Repo) DeleteNoteGroup(id, ownerID int64) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM note_groups WHERE id=? AND owner_id=?`, id, ownerID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("group not found or permission denied")
	}
	if _, err := tx.Exec(`UPDATE notes SET group_id=NULL WHERE group_id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// SetNoteGroup 移动笔记归属组（groupID=0 移出）。双重属主校验在 SQL 里：
// 笔记必须是 userID 自己的；目标组（非 0 时）也必须是自己建的。
func (d *Repo) SetNoteGroup(noteID, userID, groupID int64) error {
	res, err := d.Exec(
		`UPDATE notes SET group_id=NULLIF(?,0)
		 WHERE id=? AND owner_id=? AND deleted_at IS NULL
		   AND (? = 0 OR EXISTS(SELECT 1 FROM note_groups WHERE id=? AND owner_id=?))`,
		groupID, noteID, userID, groupID, groupID, userID,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("note/group not found or permission denied")
	}
	return nil
}

// ListNoteGroups 返回用户自己建的全部文档组（含空组），按建立先后。
func (d *Repo) ListNoteGroups(ownerID int64) ([]*model.NoteGroup, error) {
	rows, err := d.Query(`SELECT id, owner_id, name FROM note_groups WHERE owner_id=? ORDER BY id`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*model.NoteGroup
	for rows.Next() {
		gr := &model.NoteGroup{}
		if err := rows.Scan(&gr.ID, &gr.OwnerID, &gr.Name); err != nil {
			return nil, err
		}
		list = append(list, gr)
	}
	return list, rows.Err()
}

// OwnsNoteGroup 组是否属于该用户（/notes/new?group=N 的入组校验）。
func (d *Repo) OwnsNoteGroup(id, ownerID int64) bool {
	var one int
	err := d.QueryRow(`SELECT 1 FROM note_groups WHERE id=? AND owner_id=?`, id, ownerID).Scan(&one)
	return err == nil
}

// ── Permissions（可见性 + 名单） ────────────────────────────────────────────

// ListNoteShares 返回笔记的名单成员（带显示名），按加入先后排序。
func (d *Repo) ListNoteShares(noteID int64) ([]*model.NoteShare, error) {
	rows, err := d.Query(`
		SELECT s.note_id, s.user_id, s.role,
		       u.username, COALESCE(NULLIF(u.display_name,''), u.username)
		FROM note_shares s
		JOIN users u ON u.id = s.user_id
		WHERE s.note_id = ?
		ORDER BY s.created_at, s.user_id
	`, noteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*model.NoteShare
	for rows.Next() {
		sh := &model.NoteShare{}
		if err := rows.Scan(&sh.NoteID, &sh.UserID, &sh.Role, &sh.Username, &sh.DisplayName); err != nil {
			return nil, err
		}
		list = append(list, sh)
	}
	return list, rows.Err()
}

// UpsertNoteShare 添加成员或改角色（role ∈ editor/reader，handler 已校验）。
// 同一事务里把笔记切到 restricted——「加了第一个人就只剩创建者+名单」的语义；
// 删人不做反向自动切换，防止删光名单时静默变回所有人可写。
func (d *Repo) UpsertNoteShare(noteID, userID int64, role string) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`INSERT INTO note_shares (note_id, user_id, role) VALUES (?,?,?)
		 ON CONFLICT(note_id, user_id) DO UPDATE SET role=excluded.role`,
		noteID, userID, role,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`UPDATE notes SET visibility='restricted' WHERE id=? AND visibility <> 'restricted'`,
		noteID,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *Repo) RemoveNoteShare(noteID, userID int64) error {
	_, err := d.Exec(`DELETE FROM note_shares WHERE note_id=? AND user_id=?`, noteID, userID)
	return err
}

// SetNoteVisibility 直接切换可见性档位（visibility 由 handler 校验）。
func (d *Repo) SetNoteVisibility(noteID int64, visibility string) error {
	res, err := d.Exec(
		`UPDATE notes SET visibility=? WHERE id=? AND deleted_at IS NULL`,
		visibility, noteID,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("note not found")
	}
	return nil
}

// SearchShareCandidates 权限面板的用户搜索：用户名/显示名子串匹配，
// 排除创建者与已在名单内的用户，最多 10 条。
func (d *Repo) SearchShareCandidates(noteID, ownerID int64, q string) ([]*model.User, error) {
	esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
	like := "%" + esc + "%"
	rows, err := d.Query(`
		SELECT id, username, COALESCE(NULLIF(display_name,''), username)
		FROM users
		WHERE (username LIKE ? ESCAPE '\' OR display_name LIKE ? ESCAPE '\')
		  AND id <> ?
		  AND id NOT IN (SELECT user_id FROM note_shares WHERE note_id=?)
		ORDER BY username
		LIMIT 10
	`, like, like, ownerID, noteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*model.User
	for rows.Next() {
		u := &model.User{}
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName); err != nil {
			return nil, err
		}
		list = append(list, u)
	}
	return list, rows.Err()
}

// ── Revisions ──────────────────────────────────────────────────────────────

func (d *Repo) ListNoteRevisions(noteID int64) ([]*model.NoteRevision, error) {
	rows, err := d.Query(`
		SELECT r.id, r.note_id, r.saved_by, r.saved_at,
		       COALESCE(NULLIF(u.display_name,''), u.username)
		FROM note_revisions r
		JOIN users u ON u.id = r.saved_by
		WHERE r.note_id = ?
		ORDER BY r.id DESC
	`, noteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*model.NoteRevision
	for rows.Next() {
		rev := &model.NoteRevision{}
		if err := rows.Scan(&rev.ID, &rev.NoteID, &rev.SavedBy, &rev.SavedAt, &rev.SaverName); err != nil {
			return nil, err
		}
		list = append(list, rev)
	}
	return list, rows.Err()
}

func (d *Repo) GetNoteRevision(noteID, revID int64) (*model.NoteRevision, error) {
	rev := &model.NoteRevision{}
	err := d.QueryRow(`
		SELECT r.id, r.note_id, r.content_md, r.saved_by, r.saved_at,
		       COALESCE(NULLIF(u.display_name,''), u.username)
		FROM note_revisions r
		JOIN users u ON u.id = r.saved_by
		WHERE r.id = ? AND r.note_id = ?
	`, revID, noteID).Scan(&rev.ID, &rev.NoteID, &rev.ContentMD, &rev.SavedBy, &rev.SavedAt, &rev.SaverName)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return rev, err
}

// RestoreNoteRevision 恢复到某历史版本：恢复前先把当前正文存一条 revision
// （不受 5 分钟节流限制，保证可以回退「恢复」这个动作本身），再覆盖正文。
func (d *Repo) RestoreNoteRevision(noteID, revID, userID int64) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var revContent string
	err = tx.QueryRow(
		`SELECT content_md FROM note_revisions WHERE id=? AND note_id=?`, revID, noteID,
	).Scan(&revContent)
	if err == sql.ErrNoRows {
		return fmt.Errorf("revision not found")
	}
	if err != nil {
		return err
	}

	if _, err := tx.Exec(
		`INSERT INTO note_revisions (note_id, content_md, saved_by)
		 SELECT id, content_md, ? FROM notes WHERE id=? AND deleted_at IS NULL`,
		userID, noteID,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`DELETE FROM note_revisions WHERE note_id=? AND id NOT IN
		 (SELECT id FROM note_revisions WHERE note_id=? ORDER BY id DESC LIMIT 100)`,
		noteID, noteID,
	); err != nil {
		return err
	}

	res, err := tx.Exec(
		`UPDATE notes SET content_md=?, updated_by=?,
		        updated_at=strftime('%Y-%m-%d %H:%M:%f','now')
		 WHERE id=? AND deleted_at IS NULL`,
		revContent, userID, noteID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("note not found")
	}
	return tx.Commit()
}

// ── Attachments ────────────────────────────────────────────────────────────

func (d *Repo) CreateNoteAttachment(a *model.NoteAttachment) error {
	res, err := d.Exec(
		`INSERT INTO note_attachments (note_id, filename, stored_path, size) VALUES (?,?,?,?)`,
		a.NoteID, a.Filename, a.StoredPath, a.Size,
	)
	if err != nil {
		return err
	}
	a.ID, _ = res.LastInsertId()
	return nil
}

// ── 回收站 ──────────────────────────────────────────────────────────────────
// 软删除(deleted_at)早已存在,这里补齐找回链路:列表/恢复/彻底删除/到期清理。
// 权属校验都压在 SQL 的 owner_id 条件里——回收站只对创建者存在。

// TrashedNote 回收站行的最小视图(不复用 noteCols:已删笔记不参与权限联查)。
// DeletedAt 直接扫 time.Time——modernc 驱动把 DATETIME 列以 RFC3339 交出,
// 自己按 "2006-01-02 15:04:05" 解析会失败(实测界面漏出原始串)。
type TrashedNote struct {
	ID        int64
	Title     string
	DeletedAt time.Time
}

// DeletedAtLabel 转东八区显示(与文库列表的时间格式一致)。
func (t TrashedNote) DeletedAtLabel() string {
	return t.DeletedAt.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04")
}

// ListTrashedNotes 列出自己删除的笔记,新删的在前。
func (d *Repo) ListTrashedNotes(ownerID int64) ([]*TrashedNote, error) {
	rows, err := d.Query(
		`SELECT id, title, deleted_at FROM notes
		 WHERE owner_id = ? AND deleted_at IS NOT NULL
		 ORDER BY deleted_at DESC LIMIT 200`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*TrashedNote
	for rows.Next() {
		t := &TrashedNote{}
		if err := rows.Scan(&t.ID, &t.Title, &t.DeletedAt); err != nil {
			return nil, err
		}
		list = append(list, t)
	}
	return list, rows.Err()
}

// RecoverNote 从回收站恢复(清 deleted_at,笔记回到原分区/原分组)。
// ⚠ FTS 成对律:SoftDeleteNote 删除时把行摘出 notes_fts(已删的不该被搜到),
// 恢复必须塞回去——否则恢复后搜不到,且再次软删除时 FTS 'delete' 命令
// 对不在索引的行报错(external-content 表的 'delete' 要求行在索引中)。
func (d *Repo) RecoverNote(id, ownerID int64) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`UPDATE notes SET deleted_at = NULL
		 WHERE id = ? AND owner_id = ? AND deleted_at IS NOT NULL`, id, ownerID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("note not in trash or not owned")
	}
	if _, err := tx.Exec(
		`INSERT INTO notes_fts(rowid, title, content_md)
		 SELECT id, title, content_md FROM notes WHERE id=?`, id,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// PurgeNote 彻底删除:行删除经外键 CASCADE 带走 shares/revisions/attachments;
// 磁盘附件目录由 handler 负责移除。
// ⚠ FTS 成对律的另一面:软删除时行已被摘出索引,而 notes 的 AFTER DELETE
// 触发器会再发一次 FTS 'delete'——对不在索引的行报错。先把行塞回索引,
// 让触发器的 'delete' 有的放矢(同一事务内,索引不会短暂可搜)。
func (d *Repo) PurgeNote(id, ownerID int64) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		`INSERT INTO notes_fts(rowid, title, content_md)
		 SELECT id, title, content_md FROM notes
		 WHERE id = ? AND owner_id = ? AND deleted_at IS NOT NULL`, id, ownerID,
	); err != nil {
		return err
	}
	res, err := tx.Exec(
		`DELETE FROM notes WHERE id = ? AND owner_id = ? AND deleted_at IS NOT NULL`,
		id, ownerID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("note not in trash or not owned")
	}
	return tx.Commit()
}

// PurgeExpiredNotes 清理删除超过 days 天的笔记,返回被清的 id 供磁盘附件清理。
func (d *Repo) PurgeExpiredNotes(days int) ([]int64, error) {
	rows, err := d.Query(
		`SELECT id FROM notes
		 WHERE deleted_at IS NOT NULL AND deleted_at < datetime('now', ?)`,
		fmt.Sprintf("-%d days", days))
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		// 与 PurgeNote 同一 FTS 成对律:先塞回索引再删,触发器才不报错
		tx, err := d.Begin()
		if err != nil {
			return ids, err
		}
		if _, err := tx.Exec(
			`INSERT INTO notes_fts(rowid, title, content_md)
			 SELECT id, title, content_md FROM notes WHERE id = ?`, id,
		); err != nil {
			tx.Rollback()
			return ids, err
		}
		if _, err := tx.Exec(`DELETE FROM notes WHERE id = ?`, id); err != nil {
			tx.Rollback()
			return ids, err
		}
		if err := tx.Commit(); err != nil {
			return ids, err
		}
	}
	return ids, nil
}
