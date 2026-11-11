-- Copyright 2026 The Atlantis Authors
-- SPDX-License-Identifier: Apache-2.0

-- +goose Up

CREATE TABLE vcs_instances (
    id TEXT PRIMARY KEY NOT NULL,
    host TEXT NOT NULL,
    port TEXT NOT NULL,
    base_path TEXT NOT NULL,
    provider BIGINT NOT NULL,
    UNIQUE (host, port, base_path)
);

CREATE TABLE repositories (
    id TEXT PRIMARY KEY NOT NULL,
    instance_id TEXT NOT NULL REFERENCES vcs_instances(id),
    full_name TEXT NOT NULL,
    owner TEXT NOT NULL,
    name TEXT NOT NULL,
    clone_url TEXT NOT NULL,
    hostname TEXT NOT NULL,
    UNIQUE (instance_id, full_name)
);

CREATE TABLE pulls (
    id TEXT PRIMARY KEY NOT NULL,
    repository_id TEXT NOT NULL REFERENCES repositories(id),
    number BIGINT NOT NULL,
    head_commit TEXT NOT NULL,
    url TEXT NOT NULL,
    head_branch TEXT NOT NULL,
    base_branch TEXT NOT NULL,
    author TEXT NOT NULL,
    state BIGINT NOT NULL,
    closed BOOLEAN NOT NULL DEFAULT FALSE,
    cancelled BOOLEAN NOT NULL DEFAULT FALSE,
    closed_at TIMESTAMP,
    cleanup_complete BOOLEAN NOT NULL DEFAULT FALSE,
    reopen_requested BOOLEAN NOT NULL DEFAULT FALSE,
    closure_generation BIGINT NOT NULL DEFAULT 0,
    UNIQUE (repository_id, number),
    UNIQUE (id, repository_id)
);

CREATE TABLE projects (
    id TEXT PRIMARY KEY NOT NULL,
    repository_id TEXT NOT NULL REFERENCES repositories(id),
    directory TEXT NOT NULL,
    name TEXT NOT NULL,
    workspace TEXT NOT NULL,
    UNIQUE (repository_id, directory, name, workspace),
    UNIQUE (id, repository_id)
);

CREATE TABLE locks (
    project_id TEXT PRIMARY KEY NOT NULL,
    repository_id TEXT NOT NULL,
    pull_id TEXT NOT NULL,
    revision TEXT NOT NULL,
    username TEXT NOT NULL,
    teams TEXT,
    created_at TIMESTAMP NOT NULL,
    FOREIGN KEY (project_id, repository_id) REFERENCES projects(id, repository_id),
    FOREIGN KEY (pull_id, repository_id) REFERENCES pulls(id, repository_id)
);
CREATE INDEX locks_pull ON locks(pull_id);

CREATE TABLE pull_statuses (
    pull_id TEXT PRIMARY KEY NOT NULL REFERENCES pulls(id),
    revision TEXT NOT NULL,
    base_branch TEXT NOT NULL
);

CREATE TABLE project_statuses (
    pull_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    repository_id TEXT NOT NULL,
    position BIGINT NOT NULL,
    status BIGINT NOT NULL,
    policies TEXT,
    plan_hash TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (pull_id, project_id),
    UNIQUE (pull_id, position),
    FOREIGN KEY (pull_id, repository_id) REFERENCES pulls(id, repository_id),
    FOREIGN KEY (pull_id) REFERENCES pull_statuses(pull_id) ON DELETE CASCADE,
    FOREIGN KEY (project_id, repository_id) REFERENCES projects(id, repository_id)
);

CREATE TABLE command_locks (
    command BIGINT PRIMARY KEY NOT NULL,
    unix_time BIGINT NOT NULL
);

CREATE TABLE execution_claims (
    pull_id TEXT PRIMARY KEY NOT NULL REFERENCES pulls(id),
    owner TEXT NOT NULL UNIQUE,
    state TEXT NOT NULL,
    acquired_at TIMESTAMP NOT NULL
);

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
    plan_job_id TEXT,
    state TEXT NOT NULL,
    error TEXT NOT NULL,
    private BOOLEAN NOT NULL,
    started_at TIMESTAMP NOT NULL,
    finished_at TIMESTAMP,
    owner TEXT NOT NULL DEFAULT '',
    managed_plan BOOLEAN NOT NULL DEFAULT TRUE,
    kind TEXT NOT NULL DEFAULT 'command',
    mutation_started BOOLEAN NOT NULL DEFAULT FALSE,
    UNIQUE (id, project_id, pull_id),
    UNIQUE (id, pull_id),
    FOREIGN KEY (plan_job_id, project_id, pull_id) REFERENCES jobs(id, project_id, pull_id),
    FOREIGN KEY (pull_id, repository_id) REFERENCES pulls(id, repository_id),
    FOREIGN KEY (project_id, repository_id) REFERENCES projects(id, repository_id)
);
CREATE INDEX jobs_pull ON jobs(pull_id);
CREATE INDEX jobs_plan_consumers ON jobs(plan_job_id, state);

CREATE TABLE artifacts (
    id TEXT PRIMARY KEY NOT NULL,
    job_id TEXT NOT NULL REFERENCES jobs(id),
    kind TEXT NOT NULL,
    location TEXT NOT NULL UNIQUE,
    size BIGINT NOT NULL CHECK (size >= 0),
    sha256 TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL,
    deleted BOOLEAN NOT NULL DEFAULT FALSE,
    ready BOOLEAN NOT NULL DEFAULT FALSE,
    sequence BIGINT NOT NULL DEFAULT 0,
    relative_path TEXT NOT NULL DEFAULT '',
    purged BOOLEAN NOT NULL DEFAULT FALSE,
    unreferenced_at TIMESTAMP,
    CHECK (NOT purged OR deleted)
);
CREATE INDEX artifacts_job ON artifacts(job_id, kind);
CREATE UNIQUE INDEX log_sequence ON artifacts(job_id, sequence) WHERE kind = 'log';
CREATE UNIQUE INDEX artifact_file ON artifacts(job_id, kind, relative_path) WHERE kind <> 'log';

CREATE TABLE pending_plans (
    project_id TEXT NOT NULL,
    pull_id TEXT NOT NULL,
    job_id TEXT NOT NULL,
    ready BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (project_id, pull_id),
    FOREIGN KEY (job_id, project_id, pull_id) REFERENCES jobs(id, project_id, pull_id)
);

CREATE INDEX pending_plans_job ON pending_plans(job_id);

CREATE TABLE artifact_blobs (
    key TEXT PRIMARY KEY NOT NULL,
    content BYTEA NOT NULL,
    modified_at TIMESTAMP NOT NULL
);

CREATE TABLE pending_pull_lifecycles (
    pull_id TEXT PRIMARY KEY NOT NULL REFERENCES pulls(id),
    generation BIGINT NOT NULL
);
