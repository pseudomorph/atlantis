// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package models

import "time"

type LogStream struct {
	JobID              string `gorm:"primaryKey;autoIncrement:false"`
	ByteLength         int64
	SealedAt           *time.Time
	Interrupted        bool
	ArchivedAt         *time.Time
	ArchiveSHA256      string
	RetentionStartedAt *time.Time
	DeletedAt          *time.Time
	PurgedAt           *time.Time
}
