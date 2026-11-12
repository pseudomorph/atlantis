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

func TestExternalStoresDeprecationWarningFollowsLogStyle(t *testing.T) {
	if strings.Contains(externalStoresDeprecationWarning, ":") {
		t.Fatalf("log message must not contain colons: %q", externalStoresDeprecationWarning)
	}
	if externalStoresDeprecationWarning != strings.ToLower(externalStoresDeprecationWarning) {
		t.Fatalf("log message must be lowercase: %q", externalStoresDeprecationWarning)
	}
	for _, want := range []string{"enable-external-stores", "deprecated", "durable-storage", "s3", "migration"} {
		if !strings.Contains(externalStoresDeprecationWarning, want) {
			t.Fatalf("warning %q does not mention %q", externalStoresDeprecationWarning, want)
		}
	}
}
