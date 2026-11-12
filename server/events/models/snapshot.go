// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package models

import (
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"strings"
	"time"
)

// PlanSnapshot is the immutable plan output owned by one producing job.
type PlanSnapshot struct {
	JobID     string
	Manifest  SnapshotManifest
	Size      int64
	SHA256    string
	CreatedAt time.Time
}

// SnapshotManifest binds every archive member to its checkout destination.
type SnapshotManifest struct {
	Version       int            `json:"version"`
	DataDirectory string         `json:"data_directory,omitempty"`
	Files         []SnapshotFile `json:"files"`
}

// SnapshotFile describes a regular file or an explicitly captured directory.
type SnapshotFile struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256,omitempty"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
}

func (m SnapshotManifest) File(kind string) (SnapshotFile, bool) {
	for _, f := range m.Files {
		if f.Kind == kind {
			return f, true
		}
	}
	return SnapshotFile{}, false
}

func safeSnapshotPath(p string) bool {
	return p != "" && p != "." && path.Clean(p) == p && !strings.HasPrefix(p, "/") && p != ".." && !strings.HasPrefix(p, "../") && !strings.ContainsAny(p, "\\\x00:")
}

// Validate rejects ambiguous destinations and unsupported member types.
func (m SnapshotManifest) Validate() error {
	if m.Version != 1 {
		return fmt.Errorf("unsupported plan snapshot version %d", m.Version)
	}
	if m.DataDirectory != "" && !safeSnapshotPath(m.DataDirectory) {
		return fmt.Errorf("invalid snapshot data directory")
	}
	seen, kinds := map[string]bool{}, map[string]bool{}
	for _, f := range m.Files {
		mode := os.FileMode(f.Mode)
		if !safeSnapshotPath(f.Path) || seen[f.Path] || f.Size < 0 || mode & ^(os.ModeDir|0777) != 0 || mode.Perm()&0022 != 0 {
			return fmt.Errorf("invalid snapshot member %q", f.Path)
		}
		seen[f.Path] = true
		switch f.Kind {
		case "plan", "show", "lockfile", "backend", "workspace":
			if kinds[f.Kind] || mode.IsDir() {
				return fmt.Errorf("duplicate or invalid snapshot role %q", f.Kind)
			}
			kinds[f.Kind] = true
		case "workflow":
		default:
			return fmt.Errorf("invalid snapshot role %q", f.Kind)
		}
		if mode.IsDir() {
			if f.Size != 0 || f.SHA256 != "" {
				return fmt.Errorf("invalid snapshot directory")
			}
		} else {
			hash, err := hex.DecodeString(f.SHA256)
			if err != nil || len(hash) != 32 {
				return fmt.Errorf("invalid snapshot checksum")
			}
		}
	}
	for _, f := range m.Files {
		for parent := path.Dir(f.Path); parent != "."; parent = path.Dir(parent) {
			if seen[parent] {
				for _, p := range m.Files {
					if p.Path == parent && !os.FileMode(p.Mode).IsDir() {
						return fmt.Errorf("snapshot path has a file ancestor")
					}
				}
			}
		}
	}
	return nil
}
