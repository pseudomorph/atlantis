// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package artifacts_test

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/runatlantis/atlantis/server/core/artifacts"
	. "github.com/runatlantis/atlantis/testing"
)

func TestArtifactBackendConformance(t *testing.T) {
	factories := map[string]func(*testing.T) artifacts.Store{
		"filesystem": func(t *testing.T) artifacts.Store {
			s, e := artifacts.NewFilesystem(t.TempDir())
			Ok(t, e)
			t.Cleanup(func() { _ = s.Close() })
			return s
		},
	}
	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			backend := factory(t)
			scope := artifacts.Scoped{Store: backend, Prefix: "repository/pr/job"}
			content := []byte{0, 1, 2, 3, 255}
			metadata, err := scope.Put("plan", bytes.NewReader(content))
			Ok(t, err)
			Equals(t, int64(len(content)), metadata.Size)
			Equals(t, fmt.Sprintf("%x", sha256.Sum256(content)), metadata.SHA256)
			r, err := scope.Open("plan")
			Ok(t, err)
			got, err := io.ReadAll(r)
			Ok(t, err)
			Ok(t, r.Close())
			Equals(t, content, got)
			for _, bad := range []string{"../other", "/absolute", "a/../plan", "a\\plan", "a\x00plan", "."} {
				_, err := scope.Put(bad, strings.NewReader("bad"))
				Assert(t, err != nil, "write escaped scope %q", bad)
				_, err = scope.Open(bad)
				Assert(t, err != nil, "read escaped scope %q", bad)
				Assert(t, scope.Delete(bad) != nil, "delete escaped scope %q", bad)
			}
			objects, err := scope.List()
			Ok(t, err)
			Equals(t, 1, len(objects))
			Equals(t, "plan", objects[0].Key)
			other := artifacts.Scoped{Store: backend, Prefix: "repository/pr/other-job"}
			_, err = other.Open("plan")
			Assert(t, err != nil, "other job read artifact")
			Ok(t, scope.Delete("plan"))
			Ok(t, scope.Delete("plan"))
			_, err = scope.Open("plan")
			Assert(t, err != nil, "deleted object remained readable")
		})
	}
}
