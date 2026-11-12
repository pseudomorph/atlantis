-- Copyright 2026 The Atlantis Authors
-- SPDX-License-Identifier: Apache-2.0

-- +goose Up
CREATE TABLE execution_recoveries (
    id TEXT PRIMARY KEY NOT NULL,
    pull_id TEXT NOT NULL REFERENCES pulls(id),
    previous_owner TEXT NOT NULL,
    recovery_owner TEXT NOT NULL,
    source_job_id TEXT,
    actor TEXT NOT NULL,
    recovered_at TIMESTAMP NOT NULL,
    FOREIGN KEY (source_job_id, pull_id) REFERENCES jobs(id, pull_id)
);
