// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package artifacts

import "io"

type Metadata struct {
	Size   int64
	SHA256 string
}

// Store uses opaque relative keys confined to a backend root.
// Filesystem and S3 implement byte transport; database snapshots use a typed foreign key.
type Store interface {
	Close() error
	List() ([]Object, error)
	Put(key string, content io.Reader) (Metadata, error)
	Open(key string) (io.ReadCloser, error)
	Delete(key string) error
}
