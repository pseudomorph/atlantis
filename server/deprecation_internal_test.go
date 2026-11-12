// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"strings"
	"testing"
)

func TestBoltDBDeprecationWarningFollowsLogStyle(t *testing.T) {
	if strings.Contains(boltDBDeprecationWarning, ":") {
		t.Fatalf("log message must not contain colons: %q", boltDBDeprecationWarning)
	}
	if boltDBDeprecationWarning != strings.ToLower(boltDBDeprecationWarning) {
		t.Fatalf("log message must be lowercase: %q", boltDBDeprecationWarning)
	}
	for _, want := range []string{"deprecated", "durable-storage", "sqlite", "migration"} {
		if !strings.Contains(boltDBDeprecationWarning, want) {
			t.Fatalf("warning %q does not mention %q", boltDBDeprecationWarning, want)
		}
	}
}
