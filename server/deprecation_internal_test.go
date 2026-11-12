// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"strings"
	"testing"
)

func TestDeprecationWarningsFollowLogStyle(t *testing.T) {
	for warning, replacement := range map[string]string{boltDBDeprecationWarning: "sqlite", redisDeprecationWarning: "postgres"} {
		if strings.Contains(warning, ":") {
			t.Fatalf("log message must not contain colons: %q", warning)
		}
		if warning != strings.ToLower(warning) {
			t.Fatalf("log message must be lowercase: %q", warning)
		}
		for _, want := range []string{"deprecated", "durable-storage", replacement, "migration"} {
			if !strings.Contains(warning, want) {
				t.Fatalf("warning %q does not mention %q", warning, want)
			}
		}
	}
}
