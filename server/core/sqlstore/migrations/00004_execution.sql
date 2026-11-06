-- Copyright 2026 The Atlantis Authors
-- SPDX-License-Identifier: Apache-2.0

-- +goose Up
ALTER TABLE pulls ADD COLUMN closed BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE pulls ADD COLUMN cancelled BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE pulls ADD COLUMN closed_at TIMESTAMP;
CREATE TABLE execution_scopes (
 id TEXT PRIMARY KEY NOT NULL,
 repository_id TEXT NOT NULL REFERENCES repositories(id),
 metadata_pull_id TEXT NOT NULL REFERENCES pulls(id),
 pull_id TEXT REFERENCES pulls(id),
 kind TEXT NOT NULL,
 ref TEXT NOT NULL,
 CHECK ((kind = 'pr' AND pull_id IS NOT NULL) OR (kind = 'api' AND pull_id IS NULL))
);
CREATE TABLE execution_claims (
 scope_id TEXT NOT NULL UNIQUE REFERENCES execution_scopes(id),
 pull_id TEXT PRIMARY KEY NOT NULL REFERENCES pulls(id),
 owner TEXT NOT NULL UNIQUE,
 state TEXT NOT NULL,
 acquired_at TIMESTAMP NOT NULL,
 heartbeat_at TIMESTAMP NOT NULL,
 lease_until TIMESTAMP NOT NULL
);
ALTER TABLE jobs ADD COLUMN owner TEXT NOT NULL DEFAULT '';
CREATE TABLE artifact_inputs (
 job_id TEXT NOT NULL REFERENCES jobs(id),
 artifact_id TEXT NOT NULL REFERENCES artifacts(id),
 PRIMARY KEY(job_id, artifact_id)
);
CREATE TABLE artifact_blobs (
 key TEXT PRIMARY KEY NOT NULL,
 content BYTEA NOT NULL,
 modified_at TIMESTAMP NOT NULL
);
ALTER TABLE artifacts ADD COLUMN deleted BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE artifacts ADD COLUMN ready BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE artifacts ADD COLUMN sequence BIGINT NOT NULL DEFAULT 0;
ALTER TABLE artifacts ADD COLUMN relative_path TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX log_sequence ON artifacts(job_id,sequence) WHERE kind = 'log';

ALTER TABLE artifacts ADD COLUMN purged BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE artifacts ADD COLUMN unreferenced_at TIMESTAMP;

ALTER TABLE jobs ADD COLUMN scope TEXT REFERENCES execution_scopes(id);

ALTER TABLE pulls ADD COLUMN cleanup_complete BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE jobs ADD COLUMN managed_plan BOOLEAN NOT NULL DEFAULT TRUE;

CREATE UNIQUE INDEX artifact_file ON artifacts(job_id, kind, relative_path) WHERE kind <> 'log';

ALTER TABLE jobs ADD COLUMN kind TEXT NOT NULL DEFAULT 'command';

ALTER TABLE jobs ADD COLUMN mutation_started BOOLEAN NOT NULL DEFAULT FALSE;
