// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package artifacts_test

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/runatlantis/atlantis/server/core/artifacts"
	. "github.com/runatlantis/atlantis/testing"
)

func TestFilesystemIntegrityAndConfinement(t *testing.T) {
	root := t.TempDir()
	store, err := artifacts.NewFilesystem(root)
	Ok(t, err)
	meta, err := store.Put("host/repo/job/plan.tfplan", strings.NewReader("plan"))
	Ok(t, err)
	Equals(t, int64(4), meta.Size)
	_, err = store.Put("../escape", strings.NewReader("no"))
	Assert(t, err != nil, "escaped root")
	outside := t.TempDir()
	Ok(t, os.Symlink(outside, filepath.Join(root, "outside")))
	_, err = store.Put("outside/escape", strings.NewReader("no"))
	Assert(t, err != nil, "followed escaping symlink")
	_, err = store.Put("host/repo/job/output.log", strings.NewReader("first\nsecond\n"))
	Ok(t, err)
	Ok(t, store.Close())
	store, err = artifacts.NewFilesystem(root)
	Ok(t, err)
	defer store.Close()
	reader, err := store.Open("host/repo/job/output.log")
	Ok(t, err)
	data, err := io.ReadAll(reader)
	Ok(t, err)
	Ok(t, reader.Close())
	Equals(t, "first\nsecond\n", string(data))
	Ok(t, store.Delete("host/repo/job/plan.tfplan"))
	Ok(t, store.Delete("host/repo/job/plan.tfplan"))
}
