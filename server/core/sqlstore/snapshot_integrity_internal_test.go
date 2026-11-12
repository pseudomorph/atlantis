// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/runatlantis/atlantis/server/events/models"
	. "github.com/runatlantis/atlantis/testing"
)

func TestSnapshotBlobRequiresReservedProducerAndCannotOverwrite(t *testing.T) {
	s := conformanceStore(t)
	p := claimPull()
	p.Execution = &models.Execution{Owner: "owner"}
	Ok(t, s.AcquireClaim(p, "owner"))
	producer := claimJob(p, uuid.NewString())
	Ok(t, s.StartJob(producer))
	backend := BlobStore{Store: s}
	_, err := backend.PutSnapshot(producer.ID, strings.NewReader("unreserved"))
	Assert(t, err != nil, "wrote without reservation")
	ErrContains(t, "FOREIGN KEY constraint failed", s.db.Exec("INSERT INTO snapshot_blobs(job_id,content) VALUES(?, ?)", producer.ID, []byte("orphan")).Error)
	snapshot := validSnapshot(producer.ID)
	Ok(t, s.BeginSnapshot(snapshot))
	_, err = backend.PutSnapshot(producer.ID, strings.NewReader("first"))
	Ok(t, err)
	_, err = backend.PutSnapshot(producer.ID, strings.NewReader("overwrite"))
	Assert(t, err != nil, "overwrote reserved bytes")
	reader, err := backend.OpenSnapshot(producer.ID)
	Ok(t, err)
	data, err := io.ReadAll(reader)
	Ok(t, err)
	Ok(t, reader.Close())
	Equals(t, "first", string(data))
	Assert(t, backend.DeleteSnapshot(producer.ID) != nil, "deleted a live snapshot")
	ErrContains(t, "FOREIGN KEY constraint failed", s.db.Exec("DELETE FROM plan_snapshots WHERE job_id=?", producer.ID).Error)
	ErrContains(t, "CHECK constraint failed", s.db.Exec("UPDATE plan_snapshots SET ready=TRUE WHERE job_id=?", producer.ID).Error)
	ErrContains(t, "CHECK constraint failed", s.db.Exec("UPDATE plan_snapshots SET purged=TRUE WHERE job_id=?", producer.ID).Error)
}

func TestOldDraftDatabaseFailsBeforeMigration(t *testing.T) {
	for _, shape := range []string{"repository column", "old artifact tables", "obsolete blob columns"} {
		t.Run(shape, func(t *testing.T) {
			directory := t.TempDir()
			s, err := OpenServer(directory)
			Ok(t, err)
			if shape == "repository column" {
				Ok(t, s.db.Exec("ALTER TABLE repositories ADD COLUMN owner TEXT").Error)
			} else if shape == "obsolete blob columns" {
				Ok(t, s.db.Exec("ALTER TABLE snapshot_blobs ADD COLUMN modified_at TIMESTAMP").Error)
			} else {
				Ok(t, s.db.Exec("CREATE TABLE artifacts(id TEXT PRIMARY KEY)").Error)
			}
			Ok(t, s.Close())
			reopened, err := OpenServer(directory)
			if reopened != nil {
				_ = reopened.Close()
			}
			ErrContains(t, "fresh data directory", err)
		})
	}
}
