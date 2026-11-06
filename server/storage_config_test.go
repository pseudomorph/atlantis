// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
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
		{Database: server.StorageDatabaseConfig{Type: "postgres"}},
		{Database: valid.Database, Artifacts: server.StorageArtifactsConfig{Type: "s3"}},
		{Database: valid.Database, Retention: server.StorageRetentionConfig{Logs: "0s"}},
		{Database: valid.Database, Retention: server.StorageRetentionConfig{PlanArtifacts: "-1h"}},
		{Database: valid.Database, WorkflowFiles: []string{"../outside"}},
	} {
		Assert(t, bad.Validate() != nil, "invalid storage configuration accepted")
	}
}
