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
	s3 := server.StorageArtifactsConfig{Type: "s3", S3: server.StorageS3Config{Bucket: "atlantis", Region: "us-east-1", Prefix: "team/atlantis/", Endpoint: "http://minio:9000", ForcePathStyle: true, Profile: "artifacts"}}
	withS3 := valid
	withS3.Artifacts = s3
	Ok(t, withS3.Validate())
	Equals(t, "/data/s3-uploads", withS3.S3UploadDirectory("/data"))
	for _, bad := range []server.StorageConfig{
		{}, {Database: server.StorageDatabaseConfig{Type: "redis"}},
		{Database: server.StorageDatabaseConfig{Type: "postgres"}},
		{Database: server.StorageDatabaseConfig{Type: "postgres", ConnectionStringEnv: "DSN", ConnectionStringFile: "/run/secrets/dsn"}},
		{Database: server.StorageDatabaseConfig{Type: "sqlite", ConnectionStringEnv: "DSN"}},
		{Database: server.StorageDatabaseConfig{Type: "sqlite", ConnectionStringFile: "/run/secrets/dsn"}},
		{Database: valid.Database, Artifacts: server.StorageArtifactsConfig{Type: "s3"}},
		{Database: valid.Database, Artifacts: server.StorageArtifactsConfig{Type: "s3", S3: server.StorageS3Config{Bucket: "b"}}},
		{Database: valid.Database, Artifacts: server.StorageArtifactsConfig{Type: "s3", S3: server.StorageS3Config{Region: "us-east-1"}}},
		{Database: valid.Database, Artifacts: server.StorageArtifactsConfig{Type: "s3", Directory: "/srv", S3: s3.S3}},
		{Database: valid.Database, Artifacts: server.StorageArtifactsConfig{Type: "s3", S3: server.StorageS3Config{Bucket: "b", Region: "r", Prefix: "../escape"}}},
		{Database: valid.Database, Artifacts: server.StorageArtifactsConfig{Type: "s3", S3: server.StorageS3Config{Bucket: "b", Region: "r", Prefix: "/absolute"}}},
		{Database: valid.Database, Artifacts: server.StorageArtifactsConfig{Type: "filesystem", S3: s3.S3}},
		{Database: valid.Database, Artifacts: server.StorageArtifactsConfig{Type: "database", S3: s3.S3}},
		{Database: valid.Database, Artifacts: server.StorageArtifactsConfig{Type: "gcs"}},
		{Database: valid.Database, Artifacts: server.StorageArtifactsConfig{Directory: "/var/lib/atlantis"}},
		{Database: valid.Database, Retention: server.StorageRetentionConfig{Logs: "0s"}},
		{Database: valid.Database, Retention: server.StorageRetentionConfig{PlanArtifacts: "-1h"}},
		{Database: valid.Database, WorkflowFiles: []string{"../outside"}},
	} {
		Assert(t, bad.Validate() != nil, "invalid storage configuration accepted")
	}
}

func TestPostgresConnectionString(t *testing.T) {
	for _, valid := range []server.StorageDatabaseConfig{
		{Type: "postgres", ConnectionStringEnv: "ATLANTIS_STORAGE_TEST_DSN"},
		{Type: "postgres", ConnectionStringFile: "/run/secrets/dsn"},
	} {
		Ok(t, server.StorageConfig{Database: valid}.Validate())
	}

	t.Setenv("ATLANTIS_STORAGE_TEST_DSN", " postgres://atlantis@db/atlantis\n")
	dsn, err := server.StorageDatabaseConfig{Type: "postgres", ConnectionStringEnv: "ATLANTIS_STORAGE_TEST_DSN"}.ConnectionString()
	Ok(t, err)
	Equals(t, "postgres://atlantis@db/atlantis", dsn)
	t.Setenv("ATLANTIS_STORAGE_TEST_DSN", "")
	_, err = server.StorageDatabaseConfig{Type: "postgres", ConnectionStringEnv: "ATLANTIS_STORAGE_TEST_DSN"}.ConnectionString()
	ErrContains(t, "is empty", err)

	file := filepath.Join(t.TempDir(), "dsn")
	Ok(t, os.WriteFile(file, []byte("host=db user=atlantis\n"), 0600))
	dsn, err = server.StorageDatabaseConfig{Type: "postgres", ConnectionStringFile: file}.ConnectionString()
	Ok(t, err)
	Equals(t, "host=db user=atlantis", dsn)
	Ok(t, os.WriteFile(file, []byte("\n"), 0600))
	_, err = server.StorageDatabaseConfig{Type: "postgres", ConnectionStringFile: file}.ConnectionString()
	ErrContains(t, "is empty", err)
	_, err = server.StorageDatabaseConfig{Type: "postgres", ConnectionStringFile: filepath.Join(t.TempDir(), "missing")}.ConnectionString()
	ErrContains(t, "reading storage.database.connection_string_file", err)
}
