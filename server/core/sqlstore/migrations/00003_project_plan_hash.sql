-- Copyright 2026 The Atlantis Authors
-- SPDX-License-Identifier: Apache-2.0

-- +goose Up
ALTER TABLE project_statuses ADD COLUMN plan_hash TEXT NOT NULL DEFAULT '';
