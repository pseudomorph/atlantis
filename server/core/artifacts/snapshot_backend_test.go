// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package artifacts_test

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/runatlantis/atlantis/server/core/artifacts"
	. "github.com/runatlantis/atlantis/testing"
)

func TestObjectSnapshotsIsolatesJobsAndLogArchives(t *testing.T) {
	factories := map[string]func(*testing.T) artifacts.Store{
		"filesystem": func(t *testing.T) artifacts.Store {
			store, err := artifacts.NewFilesystem(t.TempDir())
			Ok(t, err)
			t.Cleanup(func() { _ = store.Close() })
			return store
		},
		"s3 API": func(t *testing.T) artifacts.Store {
			return artifacts.NewS3WithClient(&objectClient{objects: map[string][]byte{}}, "bucket", "prefix")
		},
	}
	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			raw := factory(t)
			backend := artifacts.ObjectSnapshots{Store: raw}
			first, second := uuid.NewString(), uuid.NewString()
			_, err := backend.PutSnapshot(first, strings.NewReader("first bundle"))
			Ok(t, err)
			_, err = backend.PutSnapshot(second, strings.NewReader("second bundle"))
			Ok(t, err)
			_, err = raw.Put("logs/v1/"+first, strings.NewReader("log archive"))
			Ok(t, err)
			reader, err := backend.OpenSnapshot(first)
			Ok(t, err)
			data, err := io.ReadAll(reader)
			Ok(t, err)
			Ok(t, reader.Close())
			Equals(t, []byte("first bundle"), data)
			_, err = backend.PutSnapshot("../outside", bytes.NewReader(nil))
			Assert(t, err != nil, "accepted unsafe identity")
			Ok(t, backend.CollectOrphans(map[string]bool{first: true}, false, time.Now().Add(2*time.Hour)))
			reader, err = backend.OpenSnapshot(second)
			Ok(t, err)
			Ok(t, reader.Close())
			Ok(t, backend.CollectOrphans(map[string]bool{first: true}, true, time.Now().Add(2*time.Hour)))
			_, err = backend.OpenSnapshot(second)
			Assert(t, err != nil, "orphan snapshot remained")
			reader, err = raw.Open("logs/v1/" + first)
			Ok(t, err)
			Ok(t, reader.Close())
			reader, err = backend.OpenSnapshot(first)
			Ok(t, err)
			Ok(t, reader.Close())
			Ok(t, backend.DeleteSnapshot(first))
			Ok(t, backend.DeleteSnapshot(first))
		})
	}
}
