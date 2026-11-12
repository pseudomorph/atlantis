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
    clone_url TEXT NOT NULL,
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
    closure_generation BIGINT NOT NULL DEFAULT 0 CHECK (closure_generation >= 0),
    CHECK (closed = (closed_at IS NOT NULL)),
    CHECK (NOT cleanup_complete OR closed),
    CHECK (NOT reopen_requested OR closed),
    CHECK (NOT closed OR cancelled),
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
    plan_job_id TEXT,
    state TEXT NOT NULL,
    error TEXT NOT NULL,
    private BOOLEAN NOT NULL,
    started_at TIMESTAMP NOT NULL,
    finished_at TIMESTAMP,
    owner TEXT NOT NULL CHECK (owner <> ''),
    managed_plan BOOLEAN NOT NULL DEFAULT TRUE,
    kind TEXT NOT NULL DEFAULT 'command',
    mutation_started BOOLEAN NOT NULL DEFAULT FALSE,
    tool_distribution TEXT NOT NULL DEFAULT '',
    tool_version TEXT NOT NULL DEFAULT '',
    CHECK (kind <> 'command' OR project_id IS NOT NULL),
    CHECK (kind <> 'workflow_hook' OR project_id IS NULL),
    CHECK (plan_job_id IS NULL OR (project_id IS NOT NULL AND plan_job_id <> id)),
    UNIQUE (id, project_id, pull_id),
    UNIQUE (id, pull_id),
    FOREIGN KEY (plan_job_id, project_id, pull_id) REFERENCES jobs(id, project_id, pull_id),
    FOREIGN KEY (pull_id, repository_id) REFERENCES pulls(id, repository_id),
    FOREIGN KEY (project_id, repository_id) REFERENCES projects(id, repository_id)
);
CREATE INDEX jobs_pull ON jobs(pull_id);
CREATE INDEX jobs_plan_consumers ON jobs(plan_job_id, state);

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
