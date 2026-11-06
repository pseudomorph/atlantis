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
	"gorm.io/gorm/clause"
)

type blob struct {
	Key        string `gorm:"primaryKey"`
	Content    []byte
	ModifiedAt time.Time
}

func (blob) TableName() string { return "artifact_blobs" }

// BlobStore shares the application database connection; it does not own its lifecycle.
type BlobStore struct{ Store *Store }

func (s BlobStore) Close() error { return nil }
func (s BlobStore) Put(key string, r io.Reader) (artifacts.Metadata, error) {
	if !artifacts.ValidKey(key) {
		return artifacts.Metadata{}, fmt.Errorf("invalid artifact key")
	}
	content, err := io.ReadAll(r)
	if err != nil {
		return artifacts.Metadata{}, err
	}
	err = s.Store.db.Transaction(func(tx *gorm.DB) error {
		now, err := databaseNow(tx)
		if err != nil {
			return err
		}
		row := blob{key, content, now}
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"content", "modified_at"})}).Create(&row).Error
	})
	return artifacts.Metadata{Size: int64(len(content)), SHA256: fmt.Sprintf("%x", sha256.Sum256(content))}, err
}
func (s BlobStore) Open(key string) (io.ReadCloser, error) {
	if !artifacts.ValidKey(key) {
		return nil, fmt.Errorf("invalid artifact key")
	}
	var row blob
	if err := s.Store.db.Where("key = ?", key).Take(&row).Error; err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(row.Content)), nil
}
func (s BlobStore) Delete(key string) error {
	if !artifacts.ValidKey(key) {
		return fmt.Errorf("invalid artifact key")
	}
	return s.Store.db.Where("key = ?", key).Delete(&blob{}).Error
}
func (s BlobStore) List() ([]artifacts.Object, error) {
	var rows []blob
	if err := s.Store.db.Select("key", "modified_at").Find(&rows).Error; err != nil {
		return nil, err
	}
	var out []artifacts.Object
	for _, r := range rows {
		out = append(out, artifacts.Object{Key: r.Key, ModifiedAt: r.ModifiedAt})
	}
	return out, nil
}
