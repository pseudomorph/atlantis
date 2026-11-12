// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"testing"

	"github.com/runatlantis/atlantis/server"

	. "github.com/runatlantis/atlantis/testing"
)

func TestStorageConfig(t *testing.T) {
	valid := server.StorageConfig{Database: server.StorageDatabaseConfig{Type: "sqlite"}}
	Ok(t, valid.Validate())
	for _, bad := range []server.StorageConfig{
		{}, {Database: server.StorageDatabaseConfig{Type: "redis"}},
		{Database: server.StorageDatabaseConfig{Type: "postgres"}},
	} {
		Assert(t, bad.Validate() != nil, "invalid storage configuration accepted")
	}
}
