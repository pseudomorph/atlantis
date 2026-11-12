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
