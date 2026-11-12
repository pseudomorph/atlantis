// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package server

// boltDBDeprecationWarning is logged at startup when BoltDB is the locking
// backend. ADR 0005 deprecates BoltDB in the release where the durable-storage
// experiment covers single-instance installs with SQLite. The removal rule
// and migration steps are in the deprecation notice in
// runatlantis.io/docs/experiments.md.
const boltDBDeprecationWarning = "boltdb locking is deprecated and will be removed in a future release; " +
	"single-instance installs should move to the durable-storage experiment with sqlite, " +
	"see runatlantis.io/docs/experiments#durable-storage for the replacement and migration steps"

// redisDeprecationWarning is logged at startup when Redis is the locking
// backend. ADR 0005 deprecates Redis in the release where the durable-storage
// experiment covers multiple instances with PostgreSQL. The removal rule and
// migration steps are in the deprecation notice in
// runatlantis.io/docs/experiments.md.
const redisDeprecationWarning = "redis locking is deprecated and will be removed in a future release; " +
	"installs should move to the durable-storage experiment with postgres, " +
	"see runatlantis.io/docs/experiments#durable-storage for the replacement and migration steps"
