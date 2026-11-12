-- Copyright 2026 The Atlantis Authors
-- SPDX-License-Identifier: Apache-2.0

-- +goose Up

-- Webhook deliveries already accepted, keyed by the VCS provider and the
-- delivery ID it sends in a header. Servers sharing the database record each
-- delivery before acting on it, so a redelivery has no side effects.
-- Maintenance deletes rows older than the delivery retention.
CREATE TABLE webhook_deliveries (
    provider TEXT NOT NULL CHECK (provider <> ''),
    delivery_id TEXT NOT NULL CHECK (delivery_id <> ''),
    received_at TIMESTAMP NOT NULL,
    PRIMARY KEY (provider, delivery_id)
);

CREATE INDEX webhook_deliveries_received_at ON webhook_deliveries(received_at);
