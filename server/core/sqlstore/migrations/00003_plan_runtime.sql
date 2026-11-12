-- Copyright 2026 The Atlantis Authors
-- SPDX-License-Identifier: Apache-2.0

-- +goose Up
ALTER TABLE jobs ADD COLUMN tool_distribution TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN tool_version TEXT NOT NULL DEFAULT '';
