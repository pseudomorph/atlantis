-- Copyright 2026 The Atlantis Authors
-- SPDX-License-Identifier: Apache-2.0

-- +goose Up

-- Server instances. A claim records the process that holds it and the last
-- heartbeat from that process. Claims left by an earlier version have no
-- instance; their heartbeat starts at acquisition, and startup marks them
-- uncertain before any work is admitted.
ALTER TABLE execution_claims ADD COLUMN server_instance_id TEXT NOT NULL DEFAULT '';
ALTER TABLE execution_claims ADD COLUMN heartbeat_at TIMESTAMP;
UPDATE execution_claims SET heartbeat_at = acquired_at;

-- Jobs record the instance that ran them; earlier jobs keep an empty instance.
ALTER TABLE jobs ADD COLUMN server_instance_id TEXT NOT NULL DEFAULT '';

-- Recovery audits record the instance that held the reconciled claim and the
-- instance that performed the recovery.
ALTER TABLE execution_recoveries ADD COLUMN previous_server_instance_id TEXT NOT NULL DEFAULT '';
ALTER TABLE execution_recoveries ADD COLUMN recovery_server_instance_id TEXT NOT NULL DEFAULT '';
