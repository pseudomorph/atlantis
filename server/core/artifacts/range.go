// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package artifacts

import (
	"io"
	"math"
)

// RangeStore reads a bounded byte range without downloading the preceding
// bytes. Stores whose Open returns an io.Seeker do not need it.
type RangeStore interface {
	OpenRange(key string, offset, length int64) (io.ReadCloser, error)
}

func validRange(offset, length int64) bool {
	return offset >= 0 && length > 0 && offset <= math.MaxInt64-length
}
