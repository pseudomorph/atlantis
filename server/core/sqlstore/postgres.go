// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pressly/goose/v3"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// serverLockClass is the first key of the advisory lock that makes one server
// the only user of a PostgreSQL schema. The second key hashes the schema name.
const serverLockClass int32 = 0x41544c53

// ErrServerLockLost reports that the PostgreSQL session holding the server lock ended.
var ErrServerLockLost = errors.New("durable-storage lost its PostgreSQL server lock; restart the server")

// OpenPostgresServer opens PostgreSQL storage for the only server using it.
// connString is a libpq keyword/value or URL connection string; its search_path
// selects the schema. A session-level advisory lock, held on a dedicated
// connection until Close, keeps a second server from opening the same schema.
// It is taken before migrations run.
func OpenPostgresServer(connString, dataDir string) (*Store, error) {
	if strconv.IntSize < 64 {
		return nil, fmt.Errorf("durable-storage requires a 64-bit server; legacy storage remains supported")
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("creating data directory: %w", err)
	}
	config, err := pgx.ParseConfig(connString)
	if err != nil {
		// pgx redacts the password from the connection string it echoes.
		return nil, fmt.Errorf("parsing PostgreSQL connection string: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("connecting to PostgreSQL: %w", err)
	}
	store, err := openPostgres(ctx, session, connString, dataDir)
	if err != nil {
		_ = session.Close(context.Background())
		return nil, err
	}
	return store, nil
}

func openPostgres(ctx context.Context, session *pgx.Conn, connString, dataDir string) (*Store, error) {
	var schema *string
	if err := session.QueryRow(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
		return nil, fmt.Errorf("reading PostgreSQL schema: %w", err)
	}
	if schema == nil {
		return nil, fmt.Errorf("the PostgreSQL search_path names no existing schema")
	}
	key := fnv.New32a()
	_, _ = key.Write([]byte(*schema))
	var held bool
	if err := session.QueryRow(ctx, "SELECT pg_try_advisory_lock($1, $2)", serverLockClass, int32(key.Sum32())).Scan(&held); err != nil {
		return nil, fmt.Errorf("locking PostgreSQL server: %w", err)
	}
	if !held {
		return nil, fmt.Errorf("durable-storage PostgreSQL schema %q is already in use by another server", *schema)
	}
	var initialized bool
	if err := session.QueryRow(ctx, "SELECT to_regclass('goose_db_version') IS NOT NULL").Scan(&initialized); err != nil {
		return nil, fmt.Errorf("reading PostgreSQL schema version: %w", err)
	}
	if err := checkDataDir(dataDir, initialized); err != nil {
		return nil, err
	}
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
	if err := migrate(conn, goose.DialectPostgres); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("migrating PostgreSQL: %w", err)
	}
	return &Store{db: orm, conn: conn, session: session, instance: uuid.NewString(), claimExpiry: ClaimExpiry}, nil
}

// VerifyServerLock checks that this server still holds exclusive use of its
// database. A SQLite file lock lasts as long as the process. The PostgreSQL
// advisory lock ends with its session, so a failed check is permanent: the
// store then admits no new claims and stops refreshing heartbeats, and the
// claims it holds expire and need stopped-work recovery.
func (s *Store) VerifyServerLock() error {
	if s.session == nil {
		return nil
	}
	if s.serverLockLost.Load() {
		return ErrServerLockLost
	}
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	if s.session.IsClosed() {
		s.serverLockLost.Store(true)
		return ErrServerLockLost
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.session.Ping(ctx); err != nil {
		s.serverLockLost.Store(true)
		return fmt.Errorf("%w: %w", ErrServerLockLost, err)
	}
	return nil
}

// checkServerLock refuses new ownership once the server lock is known lost.
func (s *Store) checkServerLock() error {
	if s.serverLockLost.Load() {
		return ErrServerLockLost
	}
	return nil
}
