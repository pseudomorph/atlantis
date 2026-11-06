// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

// Package sqlstore implements the experimental relational state store.
package sqlstore

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/gofrs/flock"
	"github.com/pressly/goose/v3"
	"github.com/runatlantis/atlantis/server/core/db"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// databaseTime keeps TIMESTAMP values comparable across database drivers.
func databaseTime(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }

// Store owns the database shared by locks and status. Its schema is managed by Goose.
type Store struct {
	db    *gorm.DB
	conn  *sql.DB
	owner *flock.Flock
}

var _ db.Database = (*Store)(nil)

// openSQLite opens the connection after OpenServer acquires the process lock.
func openSQLite(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("creating data directory: %w", err)
	}
	filename, err := filepath.Abs(filepath.Join(dataDir, "atlantis.sqlite"))
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(dataDir, "atlantis.db")); err == nil {
		return nil, fmt.Errorf("durable-storage requires a fresh data directory without atlantis.db; drain the old installation and regenerate plans")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if _, err := os.Stat(filename); errors.Is(err, os.ErrNotExist) {
		entries, err := os.ReadDir(dataDir)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.Name() != "bin" && entry.Name() != "plugin-cache" && entry.Name() != ".durable-storage.lock" {
				return nil, fmt.Errorf("durable-storage requires a fresh data directory on first use; drain the old installation and regenerate plans")
			}
		}
	} else if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite file: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: filename}
	u.RawQuery = url.Values{"_pragma": {"foreign_keys(1)", "busy_timeout(5000)", "journal_mode(WAL)"}, "_txlock": {"immediate"}}.Encode()
	orm, err := gorm.Open(sqlite.Open(u.String()), &gorm.Config{NowFunc: func() time.Time { return databaseTime(time.Now()) }, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, fmt.Errorf("opening sqlite: %w", err)
	}
	conn, err := orm.DB()
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)
	if err := migrate(conn); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("migrating sqlite: %w", err)
	}
	return &Store{db: orm, conn: conn}, nil
}

func migrate(conn *sql.DB) error {
	files, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return err
	}
	migrator, err := goose.NewProvider(goose.DialectSQLite3, conn, files)
	if err != nil {
		return err
	}
	current, available, err := migrator.GetVersions(context.Background())
	if err != nil {
		return err
	}
	if current > available {
		return fmt.Errorf("database schema %d is newer than supported schema %d", current, available)
	}
	_, err = migrator.Up(context.Background())
	return err
}

func (s *Store) Close() error {
	err := s.conn.Close()
	if s.owner != nil {
		err = errors.Join(err, s.owner.Unlock())
	}
	return err
}
func (s *Store) Ping() error { return s.conn.Ping() }

// OpenServer prevents two Atlantis processes from recovering or executing against one SQLite store.
func OpenServer(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, err
	}
	owner := flock.New(filepath.Join(dataDir, ".durable-storage.lock"))
	held, err := owner.TryLock()
	if err != nil {
		return nil, fmt.Errorf("locking SQLite server: %w", err)
	}
	if !held {
		return nil, fmt.Errorf("durable-storage SQLite is already in use by another server")
	}
	store, err := openSQLite(dataDir)
	if err != nil {
		_ = owner.Unlock()
		return nil, err
	}
	store.owner = owner
	return store, nil
}
