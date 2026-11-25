// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

// Package artifacts stores artifact contents independently of application metadata.
package artifacts

import (
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

// Filesystem writes each object to a temporary file, syncs it, and renames it into place.
type Filesystem struct {
	root *os.Root
}

func NewFilesystem(directory string) (*Filesystem, error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	return &Filesystem{root: root}, nil
}
func (s *Filesystem) Close() error { return s.root.Close() }
func (s *Filesystem) Open(key string) (io.ReadCloser, error) {
	if !ValidKey(key) {
		return nil, fmt.Errorf("invalid artifact key")
	}
	return s.root.Open(key)
}
func (s *Filesystem) Delete(key string) error {
	if !ValidKey(key) {
		return fmt.Errorf("invalid artifact key")
	}
	err := s.root.Remove(key)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
func (s *Filesystem) Put(key string, content io.Reader) (Metadata, error) {
	if !ValidKey(key) {
		return Metadata{}, fmt.Errorf("invalid artifact key")
	}
	if err := s.root.MkdirAll(filepath.Dir(key), 0700); err != nil {
		return Metadata{}, err
	}
	tmp := key + "." + uuid.NewString() + ".tmp"
	f, err := s.root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return Metadata{}, err
	}
	defer s.root.Remove(tmp) // nolint: errcheck
	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(f, h), content)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return Metadata{}, err
	}
	if err := s.root.Rename(tmp, key); err != nil {
		return Metadata{}, err
	}
	dir, err := s.root.Open(filepath.Dir(key))
	if err != nil {
		return Metadata{}, err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return Metadata{}, err
	}
	return Metadata{size, fmt.Sprintf("%x", h.Sum(nil))}, nil
}
func (s *Filesystem) List() ([]Object, error) {
	var objects []Object
	err := fs.WalkDir(s.root.FS(), ".", func(key string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		objects = append(objects, Object{Key: key, ModifiedAt: info.ModTime()})
		return nil
	})
	return objects, err
}
