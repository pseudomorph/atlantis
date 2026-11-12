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
	filesystem := valid
	filesystem.Artifacts = server.StorageArtifactsConfig{Type: "filesystem"}
	filesystem.Retention.Logs = "48h"
	filesystem.Retention.PlanArtifacts = "24h"
	Ok(t, filesystem.Validate())
	logs, planArtifacts, err = filesystem.Durations()
	Ok(t, err)
	Equals(t, 48*time.Hour, logs)
	Equals(t, 24*time.Hour, planArtifacts)
	Equals(t, "/data/artifacts", filesystem.ArtifactDirectory("/data"))
	filesystem.Artifacts.Directory = "/srv/artifacts"
	Equals(t, "/srv/artifacts", filesystem.ArtifactDirectory("/data"))
	for _, bad := range []server.StorageConfig{
		{}, {Database: server.StorageDatabaseConfig{Type: "redis"}},
		{Database: server.StorageDatabaseConfig{Type: "postgres"}},
		{Database: valid.Database, Artifacts: server.StorageArtifactsConfig{Type: "s3"}},
		{Database: valid.Database, Artifacts: server.StorageArtifactsConfig{Directory: "/var/lib/atlantis"}},
		{Database: valid.Database, Retention: server.StorageRetentionConfig{Logs: "0s"}},
		{Database: valid.Database, Retention: server.StorageRetentionConfig{PlanArtifacts: "-1h"}},
		{Database: valid.Database, WorkflowFiles: []string{"../outside"}},
	} {
		Assert(t, bad.Validate() != nil, "invalid storage configuration accepted")
	}
}
