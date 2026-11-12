-- Copyright 2026 The Atlantis Authors
-- SPDX-License-Identifier: Apache-2.0

-- +goose Up
CREATE TABLE job_logs (
    job_id TEXT PRIMARY KEY NOT NULL REFERENCES jobs(id),
    byte_length BIGINT NOT NULL DEFAULT 0 CHECK (byte_length >= 0),
    sealed_at TIMESTAMP,
    interrupted BOOLEAN NOT NULL DEFAULT FALSE,
    archived_at TIMESTAMP,
    archive_sha256 TEXT NOT NULL DEFAULT '',
    retention_started_at TIMESTAMP,
    deleted_at TIMESTAMP,
    purged_at TIMESTAMP,
    CHECK (archived_at IS NULL OR (sealed_at IS NOT NULL AND length(archive_sha256) = 64)),
    CHECK (purged_at IS NULL OR deleted_at IS NOT NULL)
);
CREATE TABLE job_log_chunks (
    job_id TEXT NOT NULL REFERENCES job_logs(job_id),
    start_offset BIGINT NOT NULL CHECK (start_offset >= 0),
    content BYTEA NOT NULL CHECK (length(content) > 0),
    PRIMARY KEY (job_id, start_offset)
);
