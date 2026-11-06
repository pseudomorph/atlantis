// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package artifacts

import (
	"errors"
	"strings"
	"time"
)

func (s *Service) Collect(now time.Time, logs, planArtifacts time.Duration) error {
	maintenance := s.Records
	rows, err := maintenance.ExpiredArtifacts(now, logs, planArtifacts)
	if err != nil {
		return err
	}
	for _, a := range rows {
		// Check the assigned job scope even during deletion.
		j, e := s.Records.Job(a.JobID)
		if e != nil || j == nil {
			err = errors.Join(err, e)
			continue
		}
		if !validArtifactLocation(*j, a.Location) {
			err = errors.Join(err, errors.New("artifact deletion is outside its assigned job scope"))
			continue
		}
		if e := (Scoped{Store: s.Blobs, Prefix: artifactPath(*j, "")}).Delete(strings.TrimPrefix(a.Location, artifactPath(*j, "")+"/")); e != nil {
			err = errors.Join(err, e)
			continue
		}
		err = errors.Join(err, maintenance.MarkPurged(a.ID))
	}
	refs, quiescent, e := maintenance.ReferencedObjects()
	if e != nil {
		return errors.Join(err, e)
	}
	if quiescent {
		objects, e := s.Blobs.List()
		if e != nil {
			return errors.Join(err, e)
		}
		for _, o := range objects {
			if !refs[o.Key] && o.ModifiedAt.Before(now.Add(-time.Hour)) {
				err = errors.Join(err, s.Blobs.Delete(o.Key))
			}
		}
	}
	return err
}
