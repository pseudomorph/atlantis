// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package artifacts

import (
	"fmt"
	"io"
	"path"
	"strings"
	"time"
)

type Object struct {
	Key        string
	ModifiedAt time.Time
}

// ValidKey excludes aliases and traversal before any storage operation.
func ValidKey(key string) bool {
	return key != "" && key != "." && !strings.ContainsAny(key, "\\\x00") && !strings.HasPrefix(key, "/") && path.Clean(key) == key && key != ".." && !strings.HasPrefix(key, "../")
}

// Scoped confines every operation to a database-assigned repository/PR/job prefix.
type Scoped struct {
	Store     Store
	Prefix    string
	OwnsStore bool
}

func (s Scoped) key(key string) (string, error) {
	if !ValidKey(s.Prefix) || !ValidKey(key) {
		return "", fmt.Errorf("invalid artifact scope or key")
	}
	return s.Prefix + "/" + key, nil
}
func (s Scoped) Put(key string, r io.Reader) (Metadata, error) {
	k, e := s.key(key)
	if e != nil {
		return Metadata{}, e
	}
	return s.Store.Put(k, r)
}
func (s Scoped) Open(key string) (io.ReadCloser, error) {
	k, e := s.key(key)
	if e != nil {
		return nil, e
	}
	return s.Store.Open(k)
}
func (s Scoped) Delete(key string) error {
	k, e := s.key(key)
	if e != nil {
		return e
	}
	return s.Store.Delete(k)
}
func (s Scoped) Close() error {
	if s.OwnsStore {
		return s.Store.Close()
	}
	return nil
}
func (s Scoped) List() ([]Object, error) {
	objects, e := s.Store.List()
	if e != nil {
		return nil, e
	}
	var out []Object
	for _, o := range objects {
		if strings.HasPrefix(o.Key, s.Prefix+"/") {
			o.Key = strings.TrimPrefix(o.Key, s.Prefix+"/")
			out = append(out, o)
		}
	}
	return out, nil
}
