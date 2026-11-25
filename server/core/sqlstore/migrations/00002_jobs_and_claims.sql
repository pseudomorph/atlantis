-- Copyright 2026 The Atlantis Authors
-- SPDX-License-Identifier: Apache-2.0

-- +goose Up

-- Pull lifecycle. Each check is attached to the column that introduces it,
-- because SQLite cannot add a table constraint to an existing table. The
-- defaults describe an open pull, so existing rows satisfy every check.
ALTER TABLE pulls ADD COLUMN cancelled BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE pulls ADD COLUMN closed BOOLEAN NOT NULL DEFAULT FALSE CHECK (NOT closed OR cancelled);
ALTER TABLE pulls ADD COLUMN closed_at TIMESTAMP CHECK (closed = (closed_at IS NOT NULL));
ALTER TABLE pulls ADD COLUMN cleanup_complete BOOLEAN NOT NULL DEFAULT FALSE CHECK (NOT cleanup_complete OR closed);
ALTER TABLE pulls ADD COLUMN reopen_requested BOOLEAN NOT NULL DEFAULT FALSE CHECK (NOT reopen_requested OR closed);
ALTER TABLE pulls ADD COLUMN closure_generation BIGINT NOT NULL DEFAULT 0 CHECK (closure_generation >= 0);

CREATE TABLE execution_claims (
    pull_id TEXT PRIMARY KEY NOT NULL REFERENCES pulls(id),
    owner TEXT NOT NULL UNIQUE CHECK (owner <> ''),
    state TEXT NOT NULL,
    acquired_at TIMESTAMP NOT NULL
);

CREATE TABLE jobs (
    id TEXT PRIMARY KEY NOT NULL,
    repository_id TEXT NOT NULL,
    pull_id TEXT NOT NULL,
    project_id TEXT,
    command TEXT NOT NULL,
    sub_command TEXT NOT NULL,
    arguments TEXT,
    steps TEXT NOT NULL,
    issuer TEXT NOT NULL,
    issuer_teams TEXT,
    trigger TEXT NOT NULL,
    revision TEXT NOT NULL,
    state TEXT NOT NULL,
    error TEXT NOT NULL,
    private BOOLEAN NOT NULL,
    started_at TIMESTAMP NOT NULL,
    finished_at TIMESTAMP,
    owner TEXT NOT NULL CHECK (owner <> ''),
    kind TEXT NOT NULL DEFAULT 'command',
    -- Command jobs belong to a project; every other kind is pull-scoped.
    CHECK ((kind = 'command') = (project_id IS NOT NULL)),
    UNIQUE (id, pull_id),
    FOREIGN KEY (pull_id, repository_id) REFERENCES pulls(id, repository_id),
    FOREIGN KEY (project_id, repository_id) REFERENCES projects(id, repository_id)
);
CREATE INDEX jobs_pull ON jobs(pull_id);

CREATE TABLE pending_pull_lifecycles (
    pull_id TEXT PRIMARY KEY NOT NULL REFERENCES pulls(id),
    generation BIGINT NOT NULL CHECK (generation > 0)
);

CREATE TABLE execution_recoveries (
    id TEXT PRIMARY KEY NOT NULL,
    pull_id TEXT NOT NULL REFERENCES pulls(id),
    previous_owner TEXT NOT NULL CHECK (previous_owner <> ''),
    recovery_owner TEXT NOT NULL CHECK (recovery_owner <> ''),
    source_job_id TEXT,
    actor TEXT NOT NULL CHECK (actor <> ''),
    recovered_at TIMESTAMP NOT NULL,
    CHECK (previous_owner <> recovery_owner),
    FOREIGN KEY (source_job_id, pull_id) REFERENCES jobs(id, pull_id)
);
