// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/runatlantis/atlantis/server"

	. "github.com/runatlantis/atlantis/testing"
)

func TestStorageConfig(t *testing.T) {
	valid := server.StorageConfig{Database: server.StorageDatabaseConfig{Type: "sqlite"}}
	Ok(t, valid.Validate())
	logs, planArtifacts, err := valid.Durations()
	Ok(t, err)
	Equals(t, 7*24*time.Hour, logs)
	Equals(t, time.Duration(0), planArtifacts)
	for _, bad := range []server.StorageConfig{
		{}, {Database: server.StorageDatabaseConfig{Type: "redis"}},
		{Database: server.StorageDatabaseConfig{Type: "sqlite", DSN: "not-used"}},
		{Database: server.StorageDatabaseConfig{Type: "postgres"}},
		{Database: server.StorageDatabaseConfig{Type: "postgres", DSN: "secret", DSNEnv: "OTHER_SECRET"}},
		{Database: valid.Database, Artifacts: server.StorageArtifactsConfig{Type: "s3"}},
		{Database: valid.Database, Retention: server.StorageRetentionConfig{Logs: "0s"}},
		{Database: valid.Database, Retention: server.StorageRetentionConfig{PlanArtifacts: "-1h"}},
		{Database: valid.Database, WorkflowFiles: []string{"../outside"}},
	} {
		Assert(t, bad.Validate() != nil, "invalid storage configuration accepted")
	}
	t.Setenv("STORAGE_TEST_DSN", "postgresql://test-only")
	fromEnv := server.StorageConfig{Database: server.StorageDatabaseConfig{Type: "postgres", DSNEnv: "STORAGE_TEST_DSN"}}
	Ok(t, fromEnv.Validate())
	dsn, err := fromEnv.ConnectionString()
	Ok(t, err)
	Equals(t, "postgresql://test-only", dsn)
	file := filepath.Join(t.TempDir(), "dsn")
	Ok(t, os.WriteFile(file, []byte("postgresql://file-only\n"), 0600))
	fromFile := server.StorageConfig{Database: server.StorageDatabaseConfig{Type: "postgres", DSNFile: file}}
	Ok(t, fromFile.Validate())
	dsn, err = fromFile.ConnectionString()
	Ok(t, err)
	Equals(t, "postgresql://file-only", dsn)
}
