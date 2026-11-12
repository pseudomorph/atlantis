// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package artifacts

import (
	"errors"
	"time"
)

func (s *Service) Collect(now time.Time, _, planArtifacts time.Duration) error {
	rows, err := s.Records.ExpiredSnapshots(now, planArtifacts)
	if err != nil {
		return err
	}
	var result error
	for _, snapshot := range rows {
		if e := s.Blobs.DeleteSnapshot(snapshot.JobID); e != nil {
			result = errors.Join(result, e)
			continue
		}
		result = errors.Join(result, s.Records.MarkSnapshotPurged(snapshot.JobID))
	}
	refs, quiescent, e := s.Records.ReferencedSnapshots()
	if e != nil {
		return errors.Join(result, e)
	}
	return errors.Join(result, s.Blobs.CollectOrphans(refs, quiescent, now))
}
