-- Copyright 2026 The Atlantis Authors
-- SPDX-License-Identifier: Apache-2.0

-- +goose Up

ALTER TABLE jobs ADD COLUMN managed_plan BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE jobs ADD COLUMN mutation_started BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE jobs ADD COLUMN tool_distribution TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN tool_version TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX jobs_project_pull ON jobs(id, project_id, pull_id);

-- A consumer binds the ready producer of its own project and pull at admission.
-- The binding is insert-only, so the selected plan never changes.
CREATE TABLE plan_bindings (
    job_id TEXT PRIMARY KEY NOT NULL,
    plan_job_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    pull_id TEXT NOT NULL,
    CHECK (plan_job_id <> job_id),
    FOREIGN KEY (job_id, project_id, pull_id) REFERENCES jobs(id, project_id, pull_id),
    FOREIGN KEY (plan_job_id, project_id, pull_id) REFERENCES jobs(id, project_id, pull_id)
);
CREATE INDEX plan_bindings_plan ON plan_bindings(plan_job_id);

CREATE TABLE plan_snapshots (
    job_id TEXT PRIMARY KEY NOT NULL REFERENCES jobs(id),
    manifest TEXT NOT NULL,
    size BIGINT NOT NULL DEFAULT 0 CHECK (size >= 0),
    sha256 TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL,
    ready BOOLEAN NOT NULL DEFAULT FALSE,
    deleted BOOLEAN NOT NULL DEFAULT FALSE,
    purged BOOLEAN NOT NULL DEFAULT FALSE,
    unreferenced_at TIMESTAMP,
    CHECK (NOT ready OR (size > 0 AND length(sha256) = 64)),
    CHECK (NOT purged OR deleted)
);

CREATE TABLE pending_plans (
    project_id TEXT NOT NULL,
    pull_id TEXT NOT NULL,
    job_id TEXT NOT NULL,
    ready BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (project_id, pull_id),
    FOREIGN KEY (job_id, project_id, pull_id) REFERENCES jobs(id, project_id, pull_id)
);
CREATE INDEX pending_plans_job ON pending_plans(job_id);

CREATE TABLE snapshot_blobs (
    job_id TEXT PRIMARY KEY NOT NULL REFERENCES plan_snapshots(job_id),
    content BYTEA NOT NULL
);
