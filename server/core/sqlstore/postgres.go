// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"context"
	"database/sql/driver"
	"fmt"
	"hash/fnv"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pressly/goose/v3"
	gooselock "github.com/pressly/goose/v3/lock"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// advisoryLockClass is the first key of the two-key advisory locks that
// maintenance passes take. The second key hashes the schema and the pass name.
// Migrations use the one-key form, so the two never collide.
const advisoryLockClass int32 = 0x41544c53

// OpenPostgresServer opens PostgreSQL storage. Several servers may share one
// schema; each needs its own data directory, which a file lock enforces.
// connString is a libpq keyword/value or URL connection string; its search_path
// selects the schema. Migrations run under a session-level advisory lock, so
// servers that start together migrate once while the others wait.
func OpenPostgresServer(connString, dataDir string) (*Store, error) {
	if strconv.IntSize < 64 {
		return nil, fmt.Errorf("durable-storage requires a 64-bit server; legacy storage remains supported")
	}
	if _, err := pgx.ParseConfig(connString); err != nil {
		// pgx redacts the password from the connection string it echoes.
		return nil, fmt.Errorf("parsing PostgreSQL connection string: %w", err)
	}
	owner, err := lockDataDir(dataDir, "durable-storage data directory is already in use by another server; give each server its own data-dir")
	if err != nil {
		return nil, err
	}
	store, err := openPostgres(connString, dataDir)
	if err != nil {
		_ = owner.Unlock()
		return nil, err
	}
	store.owner = owner
	return store, nil
}

func openPostgres(connString, dataDir string) (*Store, error) {
	orm, err := gorm.Open(postgres.Open(connString), gormConfig())
	if err != nil {
		return nil, fmt.Errorf("opening PostgreSQL: %w", err)
	}
	conn, err := orm.DB()
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(16)
	conn.SetMaxIdleConns(4)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var schema *string
	if err := conn.QueryRowContext(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("reading PostgreSQL schema: %w", err)
	}
	if schema == nil {
		_ = conn.Close()
		return nil, fmt.Errorf("the PostgreSQL search_path names no existing schema")
	}
	var initialized bool
	if err := conn.QueryRowContext(ctx, "SELECT to_regclass('goose_db_version') IS NOT NULL").Scan(&initialized); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("reading PostgreSQL schema version: %w", err)
	}
	if err := checkDataDir(dataDir, initialized); err != nil {
		_ = conn.Close()
		return nil, err
	}
	// The session locker retries every second for up to 5 minutes while another
	// server migrates the same schema.
	locker, err := gooselock.NewPostgresSessionLocker(gooselock.WithLockID(migrationLockID(*schema)), gooselock.WithLockTimeout(1, 300), gooselock.WithUnlockTimeout(1, 60))
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := migrate(conn, goose.DialectPostgres, goose.WithSessionLocker(locker)); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("migrating PostgreSQL: %w", err)
	}
	return &Store{db: orm, conn: conn, schema: *schema, instance: uuid.NewString(), claimExpiry: ClaimExpiry}, nil
}

// migrationLockID is the advisory lock that serializes migrations of one schema.
func migrationLockID(schema string) int64 {
	return int64(advisoryLockClass)<<32 | int64(hashKey(schema))
}

// hashKey maps a name to the second key of an advisory lock.
func hashKey(name string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return h.Sum32()
}

// RunMaintenance runs pass unless another server holds the lock for the pass
// named name, and reports whether it ran. With SQLite only one server exists,
// so pass always runs. With PostgreSQL the lock is a session-level advisory
// lock held on a dedicated connection for the length of the pass; it keeps
// servers from repeating each other's work and VCS comments. Correctness does
// not depend on it: the row-lock rule still governs every write a pass makes,
// and a lock that ends with a lost connection lets two servers run one pass.
func (s *Store) RunMaintenance(name string, pass func()) (bool, error) {
	if s.schema == "" {
		pass()
		return true, nil
	}
	ctx := context.Background()
	conn, err := s.conn.Conn(ctx)
	if err != nil {
		return false, fmt.Errorf("reserving maintenance connection: %w", err)
	}
	key := int32(hashKey(s.schema + "\x00" + name))
	var held bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1, $2)", advisoryLockClass, key).Scan(&held); err != nil {
		_ = conn.Close()
		return false, fmt.Errorf("locking maintenance pass %q: %w", name, err)
	}
	if !held {
		return false, conn.Close()
	}
	defer func() {
		var released bool
		if err := conn.QueryRowContext(ctx, "SELECT pg_advisory_unlock($1, $2)", advisoryLockClass, key).Scan(&released); err != nil || !released {
			// Discard the connection, which ends the session and its lock.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		_ = conn.Close()
	}()
	pass()
	return true, nil
}
