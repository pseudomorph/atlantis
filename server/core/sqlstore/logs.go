// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"errors"
	"fmt"
	"time"

	"github.com/runatlantis/atlantis/server/core/db"
	"github.com/runatlantis/atlantis/server/events/models"
	"gorm.io/gorm"
)

var _ db.LogStore = (*Store)(nil)

type logStream struct {
	models.LogStream `gorm:"embedded"`
}

func (logStream) TableName() string { return "job_logs" }

type logChunk struct {
	JobID       string `gorm:"primaryKey;autoIncrement:false"`
	StartOffset int64  `gorm:"primaryKey;autoIncrement:false"`
	Content     []byte
}

func (logChunk) TableName() string { return "job_log_chunks" }

func (s *Store) Log(id string) (*models.LogStream, error) {
	var row logStream
	err := s.db.Where("job_id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &row.LogStream, err
}

func (s *Store) appendLog(id string, offset *int64, content []byte, length int64) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if _, err := s.artifactJob(tx, id); err != nil {
			return err
		}
		var stream logStream
		if err := tx.Where("job_id = ?", id).Take(&stream).Error; err != nil {
			return err
		}
		if stream.SealedAt != nil || stream.DeletedAt != nil {
			return fmt.Errorf("log stream is sealed")
		}
		if offset != nil && *offset != stream.ByteLength {
			return fmt.Errorf("log append offset changed")
		}
		if length <= 0 || length > int64(^uint64(0)>>1)-stream.ByteLength {
			return fmt.Errorf("invalid log append length")
		}
		if content != nil {
			if err := tx.Create(&logChunk{JobID: id, StartOffset: stream.ByteLength, Content: content}).Error; err != nil {
				return err
			}
		}
		return tx.Model(&logStream{}).Where("job_id = ?", id).Update("byte_length", stream.ByteLength+length).Error
	})
}

func (s *Store) AppendLogChunk(id string, content []byte) error {
	if len(content) == 0 {
		return nil
	}
	return s.appendLog(id, nil, content, int64(len(content)))
}

func (s *Store) CommitFileLog(id string, offset, length int64) error {
	return s.appendLog(id, &offset, nil, length)
}

// ReadLogJournal snapshots the publication marker and bytes together, so archive
// handoff cannot delete chunks between a reader's marker and content queries.
func (s *Store) ReadLogJournal(id string, offset, limit int64) (*models.LogStream, []byte, error) {
	if offset < 0 || limit <= 0 {
		return nil, nil, fmt.Errorf("invalid log read range")
	}
	var stream logStream
	var data []byte
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("job_id = ?", id).Take(&stream).Error; err != nil {
			return err
		}
		if offset > stream.ByteLength {
			return fmt.Errorf("log offset exceeds committed output")
		}
		if stream.ArchivedAt != nil || stream.DeletedAt != nil || offset == stream.ByteLength {
			return nil
		}
		end := stream.ByteLength
		if limit < end-offset {
			end = offset + limit
		}
		var chunks []logChunk
		if err := tx.Raw(`SELECT
 CASE WHEN start_offset < @offset THEN @offset ELSE start_offset END AS start_offset,
 substr(content,
   CAST(CASE WHEN start_offset < @offset THEN @offset - start_offset + 1 ELSE 1 END AS INTEGER),
   CAST(@end - CASE WHEN start_offset < @offset THEN @offset ELSE start_offset END AS INTEGER)) AS content
 FROM job_log_chunks
 WHERE job_id = @job AND start_offset >= (
   SELECT COALESCE(MAX(start_offset), 0) FROM job_log_chunks WHERE job_id = @job AND start_offset <= @offset)
 AND start_offset < @end ORDER BY start_offset`, map[string]any{"offset": offset, "end": end, "job": id}).Scan(&chunks).Error; err != nil {
			return err
		}
		position := offset
		for _, chunk := range chunks {
			start := chunk.StartOffset
			chunkEnd := start + int64(len(chunk.Content))
			if start != position {
				return fmt.Errorf("log journal offsets are inconsistent")
			}
			if chunkEnd > end {
				chunkEnd = end
			}
			data = append(data, chunk.Content[position-start:chunkEnd-start]...)
			position = chunkEnd
		}
		return nil
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, nil
	}
	return &stream.LogStream, data, err
}

func (s *Store) PendingLogArchives() ([]models.LogStream, error) {
	var rows []models.LogStream
	err := s.db.Table("job_logs").Where("sealed_at IS NOT NULL AND archived_at IS NULL AND deleted_at IS NULL").Order("job_id").Find(&rows).Error
	return rows, err
}

func (s *Store) MarkLogArchived(id string, length int64, checksum string) error {
	if len(checksum) != 64 {
		return fmt.Errorf("invalid log archive checksum")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var stream logStream
		if err := tx.Where("job_id = ?", id).Take(&stream).Error; err != nil {
			return err
		}
		if stream.SealedAt == nil || stream.DeletedAt != nil || stream.ByteLength != length {
			return fmt.Errorf("log archive no longer matches sealed output")
		}
		if stream.ArchivedAt != nil {
			if stream.ArchiveSHA256 != checksum {
				return fmt.Errorf("log archive checksum changed")
			}
			return nil
		}
		now, err := databaseNow(tx)
		if err != nil {
			return err
		}
		if err := tx.Model(&logStream{}).Where("job_id = ?", id).Updates(map[string]any{"archived_at": now, "archive_sha256": checksum}).Error; err != nil {
			return err
		}
		return tx.Where("job_id = ?", id).Delete(&logChunk{}).Error
	})
}

func sealLog(tx *gorm.DB, id string, now time.Time, interrupted bool) error {
	return tx.Model(&logStream{}).Where("job_id = ? AND sealed_at IS NULL", id).Updates(map[string]any{"sealed_at": now, "interrupted": interrupted}).Error
}

func anchorLogRetention(tx *gorm.DB, pullID string) error {
	return tx.Exec(`UPDATE job_logs SET retention_started_at = (SELECT closed_at FROM pulls WHERE id = ?)
 WHERE retention_started_at IS NULL AND job_id IN (SELECT id FROM jobs WHERE pull_id = ?)`, pullID, pullID).Error
}

func (s *Store) ExpiredLogs(now time.Time, retention time.Duration) ([]models.LogStream, error) {
	var expired []models.LogStream
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var streams []logStream
		if err := tx.Where("purged_at IS NULL").Find(&streams).Error; err != nil {
			return err
		}
		for _, stream := range streams {
			var owner job
			if err := tx.Where("id = ?", stream.JobID).Take(&owner).Error; err != nil {
				return err
			}
			var active int64
			if err := tx.Model(&executionClaim{}).Where("pull_id = ?", owner.PullID).Count(&active).Error; err != nil {
				return err
			}
			if active != 0 || owner.State == "running" || stream.SealedAt == nil {
				continue
			}
			if stream.DeletedAt == nil {
				if stream.RetentionStartedAt == nil || now.Before(stream.RetentionStartedAt.Add(retention)) {
					continue
				}
				if err := tx.Model(&logStream{}).Where("job_id = ?", stream.JobID).Update("deleted_at", databaseTime(now)).Error; err != nil {
					return err
				}
				if err := tx.Where("job_id = ?", stream.JobID).Delete(&logChunk{}).Error; err != nil {
					return err
				}
				deleted := databaseTime(now)
				stream.DeletedAt = &deleted
			}
			expired = append(expired, stream.LogStream)
		}
		return nil
	})
	return expired, err
}

func (s *Store) MarkLogPurged(id string) error {
	now, err := s.Now()
	if err != nil {
		return err
	}
	return s.db.Model(&logStream{}).Where("job_id = ? AND deleted_at IS NOT NULL", id).Update("purged_at", now).Error
}

func anchorFinishedLog(tx *gorm.DB, row job, now time.Time) error {
	var pull struct {
		Number   int
		ClosedAt *time.Time
	}
	if err := tx.Table("pulls").Where("id = ?", row.PullID).Take(&pull).Error; err != nil {
		return err
	}
	start := pull.ClosedAt
	if pull.Number < 0 {
		start = &now
	}
	if start == nil {
		return nil
	}
	return tx.Model(&logStream{}).Where("job_id = ? AND retention_started_at IS NULL", row.ID).Update("retention_started_at", *start).Error
}

func (s *Store) CheckLogAppend(id string, offset int64) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if _, err := s.artifactJob(tx, id); err != nil {
			return err
		}
		var stream logStream
		if err := tx.Where("job_id = ?", id).Take(&stream).Error; err != nil {
			return err
		}
		if stream.SealedAt != nil || stream.DeletedAt != nil || stream.ByteLength != offset {
			return fmt.Errorf("log append is no longer current")
		}
		return nil
	})
}
