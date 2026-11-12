// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package artifacts

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
)

// SnapshotBackend stores one immutable bundle per producing job.
type SnapshotBackend interface {
	PutSnapshot(string, io.Reader) (Metadata, error)
	OpenSnapshot(string) (io.ReadCloser, error)
	DeleteSnapshot(string) error
	Close() error
	CollectOrphans(map[string]bool, bool, time.Time) error
}

// ObjectSnapshots isolates plan bundles from log archives and other objects.
type ObjectSnapshots struct{ Store Store }

const snapshotPrefix = "snapshots/v2/"
const snapshotName = "plan-snapshot.tar.gz"

func snapshotKey(jobID string) (string, error) {
	if _, err := uuid.Parse(jobID); err != nil {
		return "", fmt.Errorf("invalid snapshot job ID: %w", err)
	}
	return snapshotPrefix + jobID + "/" + snapshotName, nil
}
func (s ObjectSnapshots) PutSnapshot(id string, r io.Reader) (Metadata, error) {
	k, e := snapshotKey(id)
	if e != nil {
		return Metadata{}, e
	}
	return s.Store.Put(k, r)
}
func (s ObjectSnapshots) OpenSnapshot(id string) (io.ReadCloser, error) {
	k, e := snapshotKey(id)
	if e != nil {
		return nil, e
	}
	return s.Store.Open(k)
}
func (s ObjectSnapshots) DeleteSnapshot(id string) error {
	k, e := snapshotKey(id)
	if e != nil {
		return e
	}
	return s.Store.Delete(k)
}
func (s ObjectSnapshots) Close() error { return s.Store.Close() }
func (s ObjectSnapshots) CollectOrphans(refs map[string]bool, quiescent bool, now time.Time) error {
	if !quiescent {
		return nil
	}
	objects, e := s.Store.List()
	if e != nil {
		return e
	}
	var result error
	for _, o := range objects {
		if !strings.HasPrefix(o.Key, snapshotPrefix) {
			continue
		}
		relative := strings.TrimPrefix(o.Key, snapshotPrefix)
		id, tail, ok := strings.Cut(relative, "/")
		if !ok || tail != snapshotName {
			continue
		}
		if _, err := snapshotKey(id); err != nil {
			continue
		}
		if !refs[id] && o.ModifiedAt.Before(now.Add(-time.Hour)) {
			result = errors.Join(result, s.Store.Delete(o.Key))
		}
	}
	return result
}
