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

// ObjectSnapshots keeps plan bundles under their own key prefix, apart from other objects.
type ObjectSnapshots struct{ Store Store }

const snapshotPrefix = "snapshots/v1/"
const snapshotName = "plan-snapshot.tar.gz"

// orphanGrace keeps unreferenced bundles and leftover write temporaries long
// enough that collection never removes an upload that is still in progress.
const orphanGrace = time.Hour

// isWriteTemp reports whether tail names a Filesystem.Put temporary file
// (<name>.<uuid>.tmp) for the snapshot bundle.
func isWriteTemp(tail string) bool {
	middle, ok := strings.CutPrefix(tail, snapshotName+".")
	if !ok {
		return false
	}
	id, ok := strings.CutSuffix(middle, ".tmp")
	if !ok {
		return false
	}
	_, err := uuid.Parse(id)
	return err == nil
}

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
		if !ok {
			continue
		}
		if _, err := snapshotKey(id); err != nil {
			continue
		}
		stale := o.ModifiedAt.Before(now.Add(-orphanGrace))
		switch {
		case tail == snapshotName:
			if !refs[id] && stale {
				result = errors.Join(result, s.Store.Delete(o.Key))
			}
		case isWriteTemp(tail):
			// A crashed Put leaves its temporary file; no record ever refers to it.
			if stale {
				result = errors.Join(result, s.Store.Delete(o.Key))
			}
		}
	}
	return result
}
