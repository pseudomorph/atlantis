// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"time"

	"github.com/runatlantis/atlantis/server/events/models"
)

// LogStore owns the byte-offset journal and archive handoff independently of job outcomes.
type LogStore interface {
	Log(string) (*models.LogStream, error)
	AppendLogChunk(string, []byte) error
	ReadLogJournal(jobID string, offset, limit int64) (*models.LogStream, []byte, error)
	PendingLogArchives() ([]models.LogStream, error)
	MarkLogArchived(jobID string, length int64, checksum string) error
	ExpiredLogs(time.Time, time.Duration) ([]models.LogStream, error)
	MarkLogPurged(string) error
}
