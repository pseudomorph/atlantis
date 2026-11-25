// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

// Package sqlstoretest provides PostgreSQL databases for durable-storage tests.
package sqlstoretest

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// PostgresDSNEnv names the environment variable with a PostgreSQL connection
// string for storage tests. PostgreSQL tests skip when it is unset.
const PostgresDSNEnv = "ATLANTIS_TEST_POSTGRES_DSN"

// PostgresDSN creates an empty schema in the test database and returns a
// connection string whose search_path selects it. The schema is dropped when
// the test ends. The test is skipped when PostgresDSNEnv is unset.
func PostgresDSN(t testing.TB) string {
	t.Helper()
	dsn := os.Getenv(PostgresDSNEnv)
	if dsn == "" {
		t.Skipf("set %s to run PostgreSQL storage tests", PostgresDSNEnv)
	}
	schema := "atlantis_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	exec(t, dsn, "CREATE SCHEMA "+schema)
	t.Cleanup(func() { exec(t, dsn, "DROP SCHEMA "+schema+" CASCADE") })
	if !strings.Contains(dsn, "://") {
		return fmt.Sprintf("%s search_path=%s", dsn, schema)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parsing %s: %v", PostgresDSNEnv, err)
	}
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	return u.String()
}

// AgeHeartbeats moves the heartbeat of the active claims matching where, a SQL
// condition on execution_claims, a day into the past. It stands in for a
// server that stopped refreshing them longer ago than the claim expiry.
func AgeHeartbeats(t testing.TB, dsn, where string, args ...any) {
	t.Helper()
	exec(t, dsn, "UPDATE execution_claims SET heartbeat_at = heartbeat_at - interval '1 day' WHERE state = 'active' AND ("+where+")", args...)
}

func exec(t testing.TB, dsn, statement string, args ...any) {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connecting to the PostgreSQL test database: %v", err)
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(context.Background(), statement, args...); err != nil {
		t.Fatalf("running %q: %v", statement, err)
	}
}
