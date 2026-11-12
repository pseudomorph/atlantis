// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package artifacts

import (
	"fmt"
	"io"
	"math"
)

// RangeStore reads a bounded byte range without downloading the preceding bytes.
type RangeStore interface {
	OpenRange(key string, offset, length int64) (io.ReadCloser, error)
}

func validRange(offset, length int64) bool {
	return offset >= 0 && length > 0 && offset <= math.MaxInt64-length
}

type rangeReader struct {
	io.Reader
	io.Closer
}

func (s *Filesystem) OpenRange(key string, offset, length int64) (io.ReadCloser, error) {
	if !ValidKey(key) || !validRange(offset, length) {
		return nil, fmt.Errorf("invalid artifact range or key")
	}
	file, err := s.root.Open(key)
	if err != nil {
		return nil, err
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, err
	}
	return &rangeReader{Reader: io.LimitReader(file, length), Closer: file}, nil
}

func (s Scoped) OpenRange(key string, offset, length int64) (io.ReadCloser, error) {
	k, err := s.key(key)
	if err != nil {
		return nil, err
	}
	if !validRange(offset, length) {
		return nil, fmt.Errorf("invalid artifact range")
	}
	if store, ok := s.Store.(RangeStore); ok {
		return store.OpenRange(k, offset, length)
	}
	reader, err := s.Store.Open(k)
	if err != nil {
		return nil, err
	}
	if _, err := io.CopyN(io.Discard, reader, offset); err != nil {
		_ = reader.Close()
		return nil, err
	}
	return &rangeReader{Reader: io.LimitReader(reader, length), Closer: reader}, nil
}
