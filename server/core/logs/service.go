// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

// Package logs streams job output through the database journal and hands sealed
// logs to an optional write-once archive.
package logs

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/runatlantis/atlantis/server/core/artifacts"
	"github.com/runatlantis/atlantis/server/core/db"
	"github.com/runatlantis/atlantis/server/events/models"
)

const readBatch int64 = 1024 * 1024

// Service appends running output to the byte-offset journal in Records. When
// Archive is set, ArchiveSealed copies each sealed log there once and deletes
// the journal bytes in the same transaction that publishes the archive. When
// Archive is nil, the journal is the final copy.
type Service struct {
	Records     db.LogStore
	Archive     artifacts.Store
	maintenance sync.Mutex
}

// Key is a stable storage contract independent of repository paths and filenames.
func Key(jobID string) string {
	component := jobID
	if component == "." || component == ".." {
		return ""
	}
	if strings.ContainsAny(component, "/\\") || component == "" {
		return ""
	}
	return path.Join("logs/v1", component)
}

func (s *Service) Info(id string) (*models.LogStream, error) { return s.Records.Log(id) }

func (s *Service) Append(id string, data []byte) error {
	return s.Records.AppendLogChunk(id, data)
}

// ReadAfter returns committed bytes after offset. Offsets are stable across the
// archive handoff: the journal serves reads until the handoff commits, then the
// archive serves the same byte positions.
func (s *Service) ReadAfter(id string, offset int64) ([]byte, int64, error) {
	stream, data, err := s.Records.ReadLogJournal(id, offset, readBatch)
	if err != nil || stream == nil {
		return nil, offset, err
	}
	if stream.DeletedAt != nil || offset == stream.ByteLength {
		return nil, offset, nil
	}
	length := min(readBatch, stream.ByteLength-offset)
	if stream.ArchivedAt != nil {
		data, err = s.readArchive(id, offset, length)
	}
	if err != nil {
		return nil, offset, err
	}
	if int64(len(data)) != length {
		return nil, offset, fmt.Errorf("log data differs from committed length")
	}
	return data, offset + length, nil
}

func (s *Service) readArchive(id string, offset, length int64) ([]byte, error) {
	if s.Archive == nil || Key(id) == "" {
		return nil, fmt.Errorf("log archive is unavailable")
	}
	reader, err := s.Archive.Open(Key(id))
	if err != nil {
		return nil, err
	}
	if seeker, ok := reader.(io.Seeker); ok {
		_, err = seeker.Seek(offset, io.SeekStart)
	} else if offset != 0 {
		_, err = io.CopyN(io.Discard, reader, offset)
	}
	var data []byte
	if err == nil {
		data, err = io.ReadAll(io.LimitReader(reader, length))
	}
	return data, errors.Join(err, reader.Close())
}

func (s *Service) ReadAll(id string) ([]byte, error) {
	var all []byte
	var offset int64
	for {
		data, next, err := s.ReadAfter(id, offset)
		if err != nil {
			return nil, err
		}
		all = append(all, data...)
		if next == offset {
			info, err := s.Info(id)
			if err != nil {
				return nil, err
			}
			if info != nil && info.ArchivedAt != nil && info.DeletedAt == nil && fmt.Sprintf("%x", sha256.Sum256(all)) != info.ArchiveSHA256 {
				return nil, fmt.Errorf("log archive checksum mismatch")
			}
			return all, nil
		}
		offset = next
	}
}

type journalReader struct {
	store          db.LogStore
	job            string
	offset, length int64
	buffer         []byte
}

func (r *journalReader) Read(target []byte) (int, error) {
	if len(target) == 0 {
		return 0, nil
	}
	if len(r.buffer) == 0 {
		if r.offset == r.length {
			return 0, io.EOF
		}
		stream, data, err := r.store.ReadLogJournal(r.job, r.offset, readBatch)
		if err != nil {
			return 0, err
		}
		if stream == nil || stream.DeletedAt != nil || stream.ArchivedAt != nil || stream.SealedAt == nil || stream.ByteLength != r.length {
			return 0, fmt.Errorf("sealed log changed during archival")
		}
		if int64(len(data)) != min(readBatch, r.length-r.offset) {
			return 0, fmt.Errorf("log journal is incomplete")
		}
		r.buffer = data
	}
	n := copy(target, r.buffer)
	r.buffer = r.buffer[n:]
	r.offset += int64(n)
	return n, nil
}

// ArchiveSealed never changes the execution result. Failed archive writes retain
// all journal bytes, and the next maintenance pass retries them.
func (s *Service) ArchiveSealed() error {
	if s.Archive == nil {
		return nil
	}
	s.maintenance.Lock()
	defer s.maintenance.Unlock()
	streams, err := s.Records.PendingLogArchives()
	if err != nil {
		return err
	}
	var failures error
	for _, stream := range streams {
		if Key(stream.JobID) == "" {
			failures = errors.Join(failures, fmt.Errorf("invalid log archive key"))
			continue
		}
		reader := &journalReader{store: s.Records, job: stream.JobID, length: stream.ByteLength}
		hash := sha256.New()
		meta, err := s.Archive.Put(Key(stream.JobID), io.TeeReader(reader, hash))
		checksum := fmt.Sprintf("%x", hash.Sum(nil))
		if err == nil && (reader.offset != stream.ByteLength || meta.Size != stream.ByteLength || meta.SHA256 != checksum) {
			err = fmt.Errorf("log archive differs from sealed output")
		}
		if err == nil {
			if err = s.Records.MarkLogArchived(stream.JobID, stream.ByteLength, checksum); err != nil {
				err = s.discardExpiredArchive(stream.JobID, err)
			}
		}
		failures = errors.Join(failures, err)
	}
	return failures
}

// discardExpiredArchive handles a handoff that failed after the archive write
// landed. Retention on another server can expire the log between the journal
// read and the handoff. Its archive delete then ran before this write, and
// nothing else would remove the object, because retention skips purged logs.
// When the record shows the log expired, the object is deleted here and the
// handoff failure is dropped. Otherwise the object stays: the next pass
// rewrites the same key, or retention deletes it.
func (s *Service) discardExpiredArchive(id string, handoff error) error {
	info, err := s.Records.Log(id)
	if err != nil {
		return errors.Join(handoff, err)
	}
	if info != nil && info.DeletedAt == nil {
		return handoff
	}
	if err := s.Archive.Delete(Key(id)); err != nil {
		return fmt.Errorf("deleting archive of expired log: %w", err)
	}
	return nil
}

func (s *Service) Collect(now time.Time, retention time.Duration) error {
	s.maintenance.Lock()
	defer s.maintenance.Unlock()
	streams, err := s.Records.ExpiredLogs(now, retention)
	if err != nil {
		return err
	}
	var failures error
	for _, stream := range streams {
		var err error
		if s.Archive != nil {
			if Key(stream.JobID) == "" {
				err = fmt.Errorf("invalid log archive key")
			} else {
				// An archive write can have succeeded before its handoff committed.
				err = s.Archive.Delete(Key(stream.JobID))
			}
		}
		if err == nil {
			err = s.Records.MarkLogPurged(stream.JobID)
		}
		failures = errors.Join(failures, err)
	}
	return errors.Join(failures, s.collectWriteTemps(now))
}

// collectWriteTemps removes temporary files that a crashed archive write left
// under logs/v1. Files younger than artifacts.OrphanGrace may belong to a write
// still in progress and are kept.
func (s *Service) collectWriteTemps(now time.Time) error {
	if s.Archive == nil {
		return nil
	}
	objects, err := s.Archive.List()
	if err != nil {
		return err
	}
	var failures error
	for _, o := range objects {
		if isWriteTemp(o.Key) && o.ModifiedAt.Before(now.Add(-artifacts.OrphanGrace)) {
			failures = errors.Join(failures, s.Archive.Delete(o.Key))
		}
	}
	return failures
}

// isWriteTemp reports whether key is a Filesystem.Put temporary file
// (<archive key>.<uuid>.tmp) for a log archive.
func isWriteTemp(key string) bool {
	rest, ok := strings.CutSuffix(key, ".tmp")
	if !ok {
		return false
	}
	archive, id, ok := strings.Cut(rest, ".")
	if !ok || Key(strings.TrimPrefix(archive, "logs/v1/")) != archive {
		return false
	}
	_, err := uuid.Parse(id)
	return err == nil
}
