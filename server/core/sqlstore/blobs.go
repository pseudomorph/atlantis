// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"time"

	"github.com/runatlantis/atlantis/server/core/artifacts"
	"gorm.io/gorm"
)

type snapshotBlob struct {
	JobID   string `gorm:"primaryKey"`
	Content []byte
}

func (snapshotBlob) TableName() string { return "snapshot_blobs" }

// BlobStore stores one bundle under its reserved snapshot foreign key.
// The application store owns the shared database connection.
type BlobStore struct{ Store *Store }

var _ artifacts.SnapshotBackend = BlobStore{}

func (s BlobStore) Close() error { return nil }
func (s BlobStore) PutSnapshot(id string, r io.Reader) (artifacts.Metadata, error) {
	content, err := io.ReadAll(r)
	if err != nil {
		return artifacts.Metadata{}, err
	}
	err = s.Store.db.Transaction(func(tx *gorm.DB) error {
		if _, err := s.Store.artifactJob(tx, id); err != nil {
			return err
		}
		var intent planSnapshot
		if err := tx.Where("job_id = ? AND ready = ? AND deleted = ?", id, false, false).Take(&intent).Error; err != nil {
			return err
		}
		return tx.Create(&snapshotBlob{JobID: id, Content: content}).Error
	})
	return artifacts.Metadata{Size: int64(len(content)), SHA256: fmt.Sprintf("%x", sha256.Sum256(content))}, err
}
func (s BlobStore) OpenSnapshot(id string) (io.ReadCloser, error) {
	var row snapshotBlob
	if err := s.Store.db.Where("job_id = ?", id).Take(&row).Error; err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(row.Content)), nil
}
func (s BlobStore) DeleteSnapshot(id string) error {
	return s.Store.db.Transaction(func(tx *gorm.DB) error {
		var row planSnapshot
		if err := tx.Where("job_id = ? AND deleted = ?", id, true).Take(&row).Error; err != nil {
			return err
		}
		return tx.Where("job_id = ?", id).Delete(&snapshotBlob{}).Error
	})
}

// Database blobs cannot be orphaned: their parent reservation is created first.
func (s BlobStore) CollectOrphans(map[string]bool, bool, time.Time) error { return nil }
