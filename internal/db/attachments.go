package db

import (
	"database/sql"
	"strings"
	"time"

	"featuretrack/internal/model"
)

func (d *DB) migrateAttachments() error {
	_, err := d.Exec(`
	CREATE TABLE IF NOT EXISTS attachments (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		owner_kind   TEXT NOT NULL DEFAULT '',
		owner_id     INTEGER NOT NULL DEFAULT 0,
		uploader_id  INTEGER NOT NULL REFERENCES users(id),
		path_full    TEXT NOT NULL,
		path_thumb   TEXT NOT NULL,
		width        INTEGER NOT NULL DEFAULT 0,
		height       INTEGER NOT NULL DEFAULT 0,
		bytes        INTEGER NOT NULL DEFAULT 0,
		original     TEXT NOT NULL DEFAULT '',
		created_at   DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		return err
	}
	_, _ = d.Exec(`CREATE INDEX IF NOT EXISTS idx_attachments_owner ON attachments(owner_kind, owner_id)`)
	return nil
}

// CreateAttachment inserts an orphan attachment (owner fields empty) — caller
// later calls AttachTo once it knows the feature/comment id.
func (d *DB) CreateAttachment(a *model.Attachment) error {
	res, err := d.Exec(
		`INSERT INTO attachments (owner_kind, owner_id, uploader_id, path_full, path_thumb, width, height, bytes, original)
		 VALUES (?,?,?,?,?,?,?,?,?)`,
		a.OwnerKind, a.OwnerID, a.UploaderID, a.PathFull, a.PathThumb, a.Width, a.Height, a.Bytes, a.Original,
	)
	if err != nil {
		return err
	}
	a.ID, _ = res.LastInsertId()
	_ = d.QueryRow(`SELECT created_at FROM attachments WHERE id=?`, a.ID).Scan(&a.CreatedAt)
	return nil
}

// AttachTo binds a set of orphan attachments (owner_kind='', owner_id=0) to
// a concrete owner. Only attachments uploaded by uploaderID are bound — this
// prevents client-supplied ids from hijacking other users' uploads.
func (d *DB) AttachTo(kind string, ownerID, uploaderID int64, attachmentIDs []int64) error {
	if len(attachmentIDs) == 0 {
		return nil
	}
	placeholders := make([]string, len(attachmentIDs))
	args := []any{kind, ownerID, uploaderID}
	for i, id := range attachmentIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	q := `UPDATE attachments SET owner_kind=?, owner_id=?
	      WHERE owner_kind='' AND uploader_id=? AND id IN (` + strings.Join(placeholders, ",") + `)`
	_, err := d.Exec(q, args...)
	return err
}

// SyncFeatureAttachments binds any orphan ids (uploaded by uploaderID) to the
// feature, and detaches (back to orphan) any existing feature attachments
// not present in the desired id list. Orphans will be GC'd by PurgeOrphanAttachments.
// Used on edit flows where the user may have removed previously-attached images.
func (d *DB) SyncFeatureAttachments(featureID, uploaderID int64, ids []int64) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	keep := map[int64]bool{}
	for _, id := range ids {
		keep[id] = true
	}

	rows, err := tx.Query(`SELECT id FROM attachments WHERE owner_kind='feature' AND owner_id=?`, featureID)
	if err != nil {
		return err
	}
	var existing []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		existing = append(existing, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, id := range existing {
		if !keep[id] {
			if _, err := tx.Exec(`UPDATE attachments SET owner_kind='', owner_id=0 WHERE id=? AND owner_kind='feature' AND owner_id=?`, id, featureID); err != nil {
				return err
			}
		}
	}

	var toBind []int64
	for _, id := range ids {
		already := false
		for _, eid := range existing {
			if eid == id {
				already = true
				break
			}
		}
		if !already {
			toBind = append(toBind, id)
		}
	}
	if len(toBind) > 0 {
		placeholders := make([]string, len(toBind))
		args := []any{featureID, uploaderID}
		for i, id := range toBind {
			placeholders[i] = "?"
			args = append(args, id)
		}
		q := `UPDATE attachments SET owner_kind='feature', owner_id=?
		      WHERE owner_kind='' AND uploader_id=? AND id IN (` + strings.Join(placeholders, ",") + `)`
		if _, err := tx.Exec(q, args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (d *DB) GetAttachment(id int64) (*model.Attachment, error) {
	a := &model.Attachment{}
	err := d.QueryRow(
		`SELECT id, owner_kind, owner_id, uploader_id, path_full, path_thumb, width, height, bytes, original, created_at
		 FROM attachments WHERE id=?`, id,
	).Scan(&a.ID, &a.OwnerKind, &a.OwnerID, &a.UploaderID, &a.PathFull, &a.PathThumb, &a.Width, &a.Height, &a.Bytes, &a.Original, &a.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return a, err
}

// ListAttachmentsForOwners loads attachments for many owners in a single query.
// Returns a map keyed by owner_id. Avoids N+1 when rendering lists (e.g., comments).
func (d *DB) ListAttachmentsForOwners(kind string, ownerIDs []int64) (map[int64][]*model.Attachment, error) {
	out := map[int64][]*model.Attachment{}
	if len(ownerIDs) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(ownerIDs))
	args := []any{kind}
	for i, id := range ownerIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	q := `SELECT id, owner_kind, owner_id, uploader_id, path_full, path_thumb, width, height, bytes, original, created_at
	      FROM attachments WHERE owner_kind=? AND owner_id IN (` + strings.Join(placeholders, ",") + `)
	      ORDER BY id ASC`
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		a := &model.Attachment{}
		if err := rows.Scan(&a.ID, &a.OwnerKind, &a.OwnerID, &a.UploaderID, &a.PathFull, &a.PathThumb, &a.Width, &a.Height, &a.Bytes, &a.Original, &a.CreatedAt); err != nil {
			return nil, err
		}
		out[a.OwnerID] = append(out[a.OwnerID], a)
	}
	return out, rows.Err()
}

// DetachAttachments orphans all attachments of a given owner so PurgeOrphanAttachments
// will GC them. Used when a comment is soft-deleted.
func (d *DB) DetachAttachments(kind string, ownerID int64) error {
	_, err := d.Exec(`UPDATE attachments SET owner_kind='', owner_id=0 WHERE owner_kind=? AND owner_id=?`, kind, ownerID)
	return err
}

func (d *DB) ListAttachments(kind string, ownerID int64) ([]*model.Attachment, error) {
	rows, err := d.Query(
		`SELECT id, owner_kind, owner_id, uploader_id, path_full, path_thumb, width, height, bytes, original, created_at
		 FROM attachments WHERE owner_kind=? AND owner_id=? ORDER BY id ASC`,
		kind, ownerID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*model.Attachment
	for rows.Next() {
		a := &model.Attachment{}
		if err := rows.Scan(&a.ID, &a.OwnerKind, &a.OwnerID, &a.UploaderID, &a.PathFull, &a.PathThumb, &a.Width, &a.Height, &a.Bytes, &a.Original, &a.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, a)
	}
	return list, rows.Err()
}

// PurgeOrphanAttachments deletes orphan rows older than cutoff and returns
// their on-disk paths so the caller can unlink the files.
func (d *DB) PurgeOrphanAttachments(olderThan time.Time) ([]string, error) {
	rows, err := d.Query(
		`SELECT id, path_full, path_thumb FROM attachments
		 WHERE owner_kind='' AND created_at < ?`, olderThan,
	)
	if err != nil {
		return nil, err
	}
	var ids []int64
	var paths []string
	for rows.Next() {
		var id int64
		var full, thumb string
		if err := rows.Scan(&id, &full, &thumb); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
		paths = append(paths, full, thumb)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	_, err = d.Exec(`DELETE FROM attachments WHERE id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	return paths, err
}
