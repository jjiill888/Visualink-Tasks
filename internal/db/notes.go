package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"featuretrack/internal/model"
)

// ErrNoteConflict 表示乐观锁校验失败：客户端打开笔记后，其他人已保存过新版本。
var ErrNoteConflict = errors.New("note updated by someone else")

// migrateNotes 建立云笔记相关表。遵循项目惯例：CREATE TABLE IF NOT EXISTS 幂等建表。
// notes.updated_at 用毫秒精度文本（strftime %f），既做排序键也做乐观锁 token。
func (d *DB) migrateNotes() error {
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
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f','now')),
		deleted_at DATETIME
	);

	CREATE INDEX IF NOT EXISTS idx_notes_updated ON notes(updated_at DESC);

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
	`)
	if err != nil {
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

const noteCols = `
	n.id, n.title, n.content_md, n.owner_id, n.is_private, n.created_at, n.updated_at,
	COALESCE(NULLIF(o.display_name,''), o.username),
	COALESCE((SELECT COALESCE(NULLIF(u2.display_name,''), u2.username) FROM users u2 WHERE u2.id = n.updated_by), '')
`

func scanNote(row interface{ Scan(...any) error }) (*model.Note, error) {
	n := &model.Note{}
	var isPrivate int
	err := row.Scan(
		&n.ID, &n.Title, &n.ContentMD, &n.OwnerID, &isPrivate, &n.CreatedAt, &n.UpdatedAt,
		&n.OwnerName, &n.UpdaterName,
	)
	n.IsPrivate = isPrivate == 1
	return n, err
}

// ftsQuote 把用户输入包成 FTS5 短语查询，避免 AND/OR/NEAR 等语法被解释。
func ftsQuote(q string) string {
	return `"` + strings.ReplaceAll(q, `"`, `""`) + `"`
}

// ListNotes 返回当前用户可见的笔记（非私有 + 自己的私有），按更新时间倒序。
// search 非空时走 FTS；trigram 要求至少 3 个字符才能命中，
// 少于 3 个字符（常见的双字中文词）退化为 LIKE 子串匹配。
func (d *DB) ListNotes(userID int64, search string) ([]*model.Note, error) {
	base := `SELECT ` + noteCols + `
		FROM notes n
		JOIN users o ON o.id = n.owner_id
		WHERE n.deleted_at IS NULL
		  AND (n.is_private = 0 OR n.owner_id = ?)`
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

// GetNote 返回未删除的笔记；不存在或已软删除返回 nil。可见性由 handler 判断。
func (d *DB) GetNote(id int64) (*model.Note, error) {
	q := `SELECT ` + noteCols + `
		FROM notes n
		JOIN users o ON o.id = n.owner_id
		WHERE n.id = ? AND n.deleted_at IS NULL`
	n, err := scanNote(d.QueryRow(q, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return n, err
}

func (d *DB) CreateNote(ownerID int64, title string) (int64, error) {
	res, err := d.Exec(
		`INSERT INTO notes (title, owner_id, updated_by) VALUES (?,?,?)`,
		title, ownerID, ownerID,
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
// isPrivate 为 nil 表示不改变私有状态（非 owner 的保存请求由 handler 传 nil）。
func (d *DB) SaveNote(id, userID int64, title, contentMD, baseUpdatedAt string, isPrivate *bool) (string, error) {
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

	var privArg any // nil → COALESCE 保持原值
	if isPrivate != nil {
		if *isPrivate {
			privArg = 1
		} else {
			privArg = 0
		}
	}
	var newUpdatedAt string
	err = tx.QueryRow(
		`UPDATE notes SET title=?, content_md=?, updated_by=?,
		        is_private=COALESCE(?, is_private),
		        updated_at=strftime('%Y-%m-%d %H:%M:%f','now')
		 WHERE id=? RETURNING updated_at`,
		title, contentMD, userID, privArg, id,
	).Scan(&newUpdatedAt)
	if err != nil {
		return "", err
	}
	return newUpdatedAt, tx.Commit()
}

// SoftDeleteNote 软删除，仅 owner 可操作。同时从 FTS 索引移除（软删除不触发
// notes 表的 DELETE trigger，这里手动清索引）。
func (d *DB) SoftDeleteNote(id, ownerID int64) error {
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

// ── Revisions ──────────────────────────────────────────────────────────────

func (d *DB) ListNoteRevisions(noteID int64) ([]*model.NoteRevision, error) {
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

func (d *DB) GetNoteRevision(noteID, revID int64) (*model.NoteRevision, error) {
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
func (d *DB) RestoreNoteRevision(noteID, revID, userID int64) error {
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

func (d *DB) CreateNoteAttachment(a *model.NoteAttachment) error {
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
