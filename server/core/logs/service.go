// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

// Package logs separates durable live output from completed-log archival.
package logs

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/runatlantis/atlantis/server/core/artifacts"
	"github.com/runatlantis/atlantis/server/core/db"
	"github.com/runatlantis/atlantis/server/events/models"
)

const readBatch int64 = 1024 * 1024

type Service struct {
	Records     db.LogStore
	Mode        string
	Archive     artifacts.Store
	Directory   string
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
	if len(data) == 0 {
		return nil
	}
	if s.Mode != "filesystem" {
		return s.Records.AppendLogChunk(id, data)
	}
	stream, err := s.Records.Log(id)
	if err != nil {
		return err
	}
	if stream == nil || stream.SealedAt != nil || stream.DeletedAt != nil {
		return fmt.Errorf("log stream is missing or sealed")
	}
	if err := s.Records.CheckLogAppend(id, stream.ByteLength); err != nil {
		return err
	}
	key := Key(id)
	if key == "" || s.Directory == "" {
		return fmt.Errorf("invalid filesystem log destination")
	}
	if err := os.MkdirAll(s.Directory, 0700); err != nil {
		return err
	}
	root, err := os.OpenRoot(s.Directory)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.MkdirAll("logs/v1", 0700); err != nil {
		return err
	}
	_, statErr := root.Stat(key)
	created := os.IsNotExist(statErr)
	file, err := root.OpenFile(key, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() != stream.ByteLength {
		return fmt.Errorf("filesystem log length differs from committed output")
	}
	if _, err := file.WriteAt(data, stream.ByteLength); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if created {
		for _, name := range []string{"logs/v1", "logs", "."} {
			directory, err := root.Open(name)
			if err != nil {
				return err
			}
			err = errors.Join(directory.Sync(), directory.Close())
			if err != nil {
				return err
			}
		}
	}
	return s.Records.CommitFileLog(id, stream.ByteLength, int64(len(data)))
}

func (s *Service) ReadAfter(id string, offset int64) ([]byte, int64, error) {
	stream, data, err := s.Records.ReadLogJournal(id, offset, readBatch)
	if err != nil || stream == nil {
		return nil, offset, err
	}
	if stream.DeletedAt != nil || offset == stream.ByteLength {
		return nil, offset, nil
	}
	length := min(readBatch, stream.ByteLength-offset)
	if s.Mode == "filesystem" {
		data, err = s.readFile(id, offset, length)
	} else if stream.ArchivedAt != nil {
		if s.Archive == nil || Key(id) == "" {
			return nil, offset, fmt.Errorf("log archive is unavailable")
		}
		var reader io.ReadCloser
		if ranged, ok := s.Archive.(interface {
			OpenRange(string, int64, int64) (io.ReadCloser, error)
		}); ok {
			reader, err = ranged.OpenRange(Key(id), offset, length)
		} else {
			reader, err = s.Archive.Open(Key(id))
			if err == nil && offset != 0 {
				_, err = io.CopyN(io.Discard, reader, offset)
				if err != nil {
					_ = reader.Close()
				}
			}
		}
		if err == nil {
			data, err = io.ReadAll(io.LimitReader(reader, length))
			err = errors.Join(err, reader.Close())
		}
	}
	if err != nil {
		return nil, offset, err
	}
	if int64(len(data)) != length {
		return nil, offset, fmt.Errorf("log data differs from committed length")
	}
	return data, offset + length, nil
}

func (s *Service) readFile(id string, offset, length int64) ([]byte, error) {
	if Key(id) == "" || s.Directory == "" {
		return nil, fmt.Errorf("invalid filesystem log destination")
	}
	root, err := os.OpenRoot(s.Directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.Open(Key(id))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data := make([]byte, length)
	_, err = file.ReadAt(data, offset)
	return data, err
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

// ArchiveSealed never changes the execution result. Failed uploads retain all journal bytes.
func (s *Service) ArchiveSealed() error {
	if s.Mode != "s3" {
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
		if s.Archive == nil || Key(stream.JobID) == "" {
			failures = errors.Join(failures, fmt.Errorf("log archive is unavailable"))
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
			err = s.Records.MarkLogArchived(stream.JobID, stream.ByteLength, checksum)
		}
		failures = errors.Join(failures, err)
	}
	return failures
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
		switch s.Mode {
		case "filesystem":
			if Key(stream.JobID) == "" || s.Directory == "" {
				err = fmt.Errorf("invalid filesystem log destination")
				break
			}
			var root *os.Root
			root, err = os.OpenRoot(s.Directory)
			if err == nil {
				err = root.Remove(Key(stream.JobID))
				_ = root.Close()
			}
			if os.IsNotExist(err) {
				err = nil
			}
		case "s3":
			if s.Archive == nil || Key(stream.JobID) == "" {
				err = fmt.Errorf("log archive is unavailable")
				break
			}
			// An upload can have succeeded before its acknowledgement was persisted.
			err = s.Archive.Delete(Key(stream.JobID))
		}
		if err == nil {
			err = s.Records.MarkLogPurged(stream.JobID)
		}
		failures = errors.Join(failures, err)
	}
	return failures
}
