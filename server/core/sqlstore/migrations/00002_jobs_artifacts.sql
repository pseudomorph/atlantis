-- Copyright 2026 The Atlantis Authors
-- SPDX-License-Identifier: Apache-2.0

-- +goose Up
CREATE TABLE jobs (
    id TEXT PRIMARY KEY NOT NULL,
    repository_id TEXT NOT NULL,
    pull_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    command TEXT NOT NULL,
    sub_command TEXT NOT NULL,
    arguments TEXT,
    steps TEXT NOT NULL,
    issuer TEXT NOT NULL,
    issuer_teams TEXT,
    trigger TEXT NOT NULL,
    revision TEXT NOT NULL,
    plan_job_id TEXT REFERENCES jobs(id),
    state TEXT NOT NULL,
    error TEXT NOT NULL,
    private BOOLEAN NOT NULL,
    started_at TIMESTAMP NOT NULL,
    finished_at TIMESTAMP,
    UNIQUE (id, project_id, pull_id),
    FOREIGN KEY (pull_id, repository_id) REFERENCES pulls(id, repository_id),
    FOREIGN KEY (project_id, repository_id) REFERENCES projects(id, repository_id)
);
CREATE INDEX jobs_pull ON jobs(pull_id);
CREATE TABLE artifacts (
    id TEXT PRIMARY KEY NOT NULL,
    job_id TEXT NOT NULL REFERENCES jobs(id),
    kind TEXT NOT NULL,
    location TEXT NOT NULL UNIQUE,
    size BIGINT NOT NULL CHECK (size >= 0),
    sha256 TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL
);
CREATE INDEX artifacts_job ON artifacts(job_id, kind);
CREATE TABLE pending_plans (
    project_id TEXT NOT NULL,
    pull_id TEXT NOT NULL,
    job_id TEXT NOT NULL,
    ready BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (project_id, pull_id),
    FOREIGN KEY (job_id, project_id, pull_id) REFERENCES jobs(id, project_id, pull_id)
);
