// Package database 是共享 SQLite 句柄:打开连接、PRAGMA、按序执行各模块
// 自带的迁移函数。schema 归属各业务模块(谁的表谁建),本包不含任何业务表。
package database

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// DB 是全系统唯一的数据库句柄;各模块 Repo/Store 共享其中的 *sql.DB。
type DB struct {
	*sql.DB
}

// Migrator 是模块自带的建表/升级函数;Open 按传入顺序执行
// (外键引用决定顺序:auth(users) 必须最先)。
type Migrator func(*sql.DB) error

// Open 打开数据库并执行迁移。SQLite 单写者,连接数固定为 1。
func Open(path string, migrators ...Migrator) (*DB, error) {
	sqldb, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	sqldb.SetMaxOpenConns(1)
	if _, err := sqldb.Exec(`
		PRAGMA journal_mode=WAL;
		PRAGMA foreign_keys=ON;
		PRAGMA synchronous=NORMAL;
		PRAGMA cache_size=-8000;
		PRAGMA busy_timeout=5000;
		PRAGMA temp_store=MEMORY;
	`); err != nil {
		return nil, fmt.Errorf("pragma: %w", err)
	}
	for _, m := range migrators {
		if err := m(sqldb); err != nil {
			return nil, fmt.Errorf("migrate: %w", err)
		}
	}
	return &DB{sqldb}, nil
}
