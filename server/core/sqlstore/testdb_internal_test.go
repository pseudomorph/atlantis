// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	"github.com/runatlantis/atlantis/server/core/sqlstore/sqlstoretest"
	. "github.com/runatlantis/atlantis/testing"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestDatabase is one isolated, initially empty database. Each Open starts a
// new server against the same data, as a restart would.
type TestDatabase struct {
	Name    string
	DataDir string
	// dsn selects the test schema; it is empty for SQLite.
	dsn string
}

// Open starts a server against the database.
func (d TestDatabase) Open() (*Store, error) {
	if d.Name == "postgres" {
		return OpenPostgresServer(d.dsn, d.DataDir)
	}
	return OpenServer(d.DataDir)
}

// Fresh returns a new, empty database of the same kind.
func (d TestDatabase) Fresh(t *testing.T) TestDatabase {
	t.Helper()
	if d.Name == "postgres" {
		return PostgresDatabase(t)
	}
	return SQLiteDatabase(t)
}

// Store starts a server that closes when the test ends.
func (d TestDatabase) Store(t *testing.T) *Store {
	t.Helper()
	s, err := d.Open()
	Ok(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// SQLiteDatabase returns a SQLite database in a new data directory.
func SQLiteDatabase(t *testing.T) TestDatabase {
	return TestDatabase{Name: "sqlite", DataDir: t.TempDir()}
}

// PostgresDatabase returns a new schema in the PostgreSQL test database, or
// skips the test when none is configured.
func PostgresDatabase(t *testing.T) TestDatabase {
	t.Helper()
	return TestDatabase{Name: "postgres", DataDir: t.TempDir(), dsn: sqlstoretest.PostgresDSN(t)}
}

// ForEachDatabase runs fn against SQLite and, when configured, PostgreSQL.
func ForEachDatabase(t *testing.T, fn func(t *testing.T, d TestDatabase)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) { fn(t, SQLiteDatabase(t)) })
	t.Run("postgres", func(t *testing.T) { fn(t, PostgresDatabase(t)) })
}

// forEachStore runs fn against a new store on each database.
func forEachStore(t *testing.T, fn func(t *testing.T, s *Store)) {
	t.Helper()
	ForEachDatabase(t, func(t *testing.T, d TestDatabase) { fn(t, d.Store(t)) })
}

// gooseDialect is the Goose dialect for the store's database.
func gooseDialect(s *Store) goose.Dialect {
	if s.db.Dialector.Name() == "postgres" {
		return goose.DialectPostgres
	}
	return goose.DialectSQLite3
}

func (d TestDatabase) gooseDialect() goose.Dialect {
	if d.Name == "postgres" {
		return goose.DialectPostgres
	}
	return goose.DialectSQLite3
}

// openUnmanaged opens the database without the server lock or migrations, as
// an older release would. The caller closes it.
func openUnmanaged(t *testing.T, d TestDatabase) *gorm.DB {
	t.Helper()
	config := &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)}
	var dialector gorm.Dialector
	if d.Name == "postgres" {
		dialector = postgres.Open(d.dsn)
	} else {
		u := url.URL{Scheme: "file", Path: filepath.Join(d.DataDir, "atlantis.sqlite")}
		u.RawQuery = url.Values{"_pragma": {"foreign_keys(1)"}}.Encode()
		dialector = sqlite.Open(u.String())
	}
	orm, err := gorm.Open(dialector, config)
	Ok(t, err)
	conn, err := orm.DB()
	Ok(t, err)
	conn.SetMaxOpenConns(1)
	return orm
}

// injectFailure makes every statement of the given timing on table fail with
// message, for example "BEFORE INSERT" or "BEFORE UPDATE OF state". The returned
// function removes the failure.
func injectFailure(t *testing.T, s *Store, timing, table, message string) func() {
	t.Helper()
	name := "reject_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if s.db.Dialector.Name() == "postgres" {
		Ok(t, s.db.Exec(fmt.Sprintf("CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION '%s'; END$$", name, message)).Error)
		Ok(t, s.db.Exec(fmt.Sprintf("CREATE TRIGGER %s %s ON %s FOR EACH ROW EXECUTE FUNCTION %s()", name, timing, table, name)).Error)
		return func() {
			Ok(t, s.db.Exec(fmt.Sprintf("DROP TRIGGER %s ON %s", name, table)).Error)
			Ok(t, s.db.Exec(fmt.Sprintf("DROP FUNCTION %s()", name)).Error)
		}
	}
	Ok(t, s.db.Exec(fmt.Sprintf("CREATE TRIGGER %s %s ON %s BEGIN SELECT RAISE(ABORT, '%s'); END", name, timing, table, message)).Error)
	return func() { Ok(t, s.db.Exec("DROP TRIGGER "+name).Error) }
}

// foreignKeyViolation asserts that err is a foreign key failure on either database.
func foreignKeyViolation(t *testing.T, err error) {
	t.Helper()
	violation(t, err, "FOREIGN KEY constraint failed", "violates foreign key constraint")
}

// uniqueViolation asserts that err is a uniqueness failure on either database.
func uniqueViolation(t *testing.T, err error) {
	t.Helper()
	violation(t, err, "UNIQUE constraint failed", "violates unique constraint")
}

func violation(t *testing.T, err error, sqlite, postgres string) {
	t.Helper()
	Assert(t, err != nil, "constraint accepted the row")
	message := err.Error()
	Assert(t, strings.Contains(message, sqlite) || strings.Contains(message, postgres), "unexpected error %q", message)
}

// checkViolation asserts that err is a CHECK constraint failure on either database.
func checkViolation(t *testing.T, err error) {
	t.Helper()
	violation(t, err, "CHECK constraint failed", "violates check constraint")
}
