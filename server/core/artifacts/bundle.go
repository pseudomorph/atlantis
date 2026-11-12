// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package artifacts

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"time"

	"github.com/google/uuid"
	"github.com/runatlantis/atlantis/server/events/models"
)

func sourceInfo(root *os.Root, name string) (os.FileInfo, error) {
	for p := name; p != "."; p = filepath.Dir(p) {
		info, e := root.Lstat(p)
		if e != nil {
			return nil, e
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("snapshot symlink %q is unsupported", p)
		}
	}
	info, e := root.Lstat(name)
	if e != nil {
		return nil, e
	}
	if !info.Mode().IsRegular() && !info.IsDir() {
		return nil, fmt.Errorf("unsupported snapshot member %q", name)
	}
	// Unix link counts are unavailable on some platforms; archive links are always rejected.
	if raw := reflect.ValueOf(info.Sys()); raw.IsValid() && raw.Kind() == reflect.Pointer && !raw.IsNil() {
		if links := raw.Elem().FieldByName("Nlink"); links.IsValid() && links.CanUint() && links.Uint() > 1 && !info.IsDir() {
			return nil, fmt.Errorf("snapshot hardlink %q is unsupported", name)
		}
	}
	return info, nil
}
func readSnapshotSource(root *os.Root, name string) ([]byte, error) {
	info, e := sourceInfo(root, name)
	if e != nil {
		return nil, e
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("snapshot member must be regular")
	}
	f, e := root.Open(name)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	actual, e := f.Stat()
	if e != nil {
		return nil, e
	}
	if !os.SameFile(info, actual) {
		return nil, fmt.Errorf("snapshot source changed")
	}
	return io.ReadAll(f)
}
func archiveName(f models.SnapshotFile) string { return path.Join(f.Kind, f.Path) }
func (s *Service) publishSnapshot(jobID string, root *os.Root, dataDir string, sources []snapshotSource) error {
	staging, e := os.MkdirTemp("", "atlantis-snapshot-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(staging)
	manifest := models.SnapshotManifest{Version: 1, DataDirectory: dataDir}
	for index, source := range sources {
		file := models.SnapshotFile{Kind: source.kind, Path: source.path, Mode: 0600}
		var input io.ReadCloser
		if source.generated {
			input = io.NopCloser(bytes.NewReader(source.data))
		} else {
			info, e := sourceInfo(root, source.path)
			if e != nil {
				return e
			}
			file.Mode = uint32(info.Mode().Perm() &^ 0022)
			if info.IsDir() {
				file.Mode |= uint32(os.ModeDir)
				manifest.Files = append(manifest.Files, file)
				continue
			}
			input, e = root.Open(source.path)
			if e != nil {
				return e
			}
			actual, e := input.(*os.File).Stat()
			if e != nil {
				input.Close()
				return e
			}
			if !os.SameFile(info, actual) {
				input.Close()
				return fmt.Errorf("snapshot source changed")
			}
		}
		staged, e := os.Create(filepath.Join(staging, fmt.Sprint(index)))
		if e != nil {
			input.Close()
			return e
		}
		hash := sha256.New()
		size, e := io.Copy(io.MultiWriter(staged, hash), input)
		inputClose := input.Close()
		closeErr := staged.Close()
		if e != nil {
			return e
		}
		if inputClose != nil {
			return inputClose
		}
		if closeErr != nil {
			return closeErr
		}
		file.Size = size
		file.SHA256 = fmt.Sprintf("%x", hash.Sum(nil))
		manifest.Files = append(manifest.Files, file)
	}
	if e := manifest.Validate(); e != nil {
		return e
	}
	archive, e := os.CreateTemp(staging, "bundle-")
	if e != nil {
		return e
	}
	defer archive.Close()
	hash := sha256.New()
	gz := gzip.NewWriter(io.MultiWriter(archive, hash))
	tw := tar.NewWriter(gz)
	content, e := json.Marshal(manifest)
	if e != nil {
		return e
	}
	if e = tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Size: int64(len(content)), Typeflag: tar.TypeReg}); e != nil {
		return e
	}
	if _, e = tw.Write(content); e != nil {
		return e
	}
	for index, file := range manifest.Files {
		header := &tar.Header{Name: archiveName(file), Mode: int64(os.FileMode(file.Mode).Perm()), Size: file.Size, Typeflag: tar.TypeReg}
		if os.FileMode(file.Mode).IsDir() {
			header.Typeflag = tar.TypeDir
		}
		if e = tw.WriteHeader(header); e != nil {
			return e
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		f, e := os.Open(filepath.Join(staging, fmt.Sprint(index)))
		if e != nil {
			return e
		}
		_, e = io.Copy(tw, f)
		closeErr := f.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if e = tw.Close(); e != nil {
		return e
	}
	if e = gz.Close(); e != nil {
		return e
	}
	size, e := archive.Seek(0, io.SeekCurrent)
	if e != nil {
		return e
	}
	if _, e = archive.Seek(0, io.SeekStart); e != nil {
		return e
	}
	snapshot := models.PlanSnapshot{JobID: jobID, Manifest: manifest, Size: size, SHA256: fmt.Sprintf("%x", hash.Sum(nil)), CreatedAt: time.Now().UTC()}
	// Reserve the sole publication only after every source and byte is staged.
	if e = s.Records.BeginSnapshot(snapshot); e != nil {
		return e
	}
	metadata, e := s.Blobs.PutSnapshot(jobID, archive)
	if e != nil {
		return fmt.Errorf("writing plan snapshot: %w", e)
	}
	if metadata.Size != snapshot.Size || metadata.SHA256 != snapshot.SHA256 {
		return fmt.Errorf("published plan snapshot checksum mismatch")
	}
	return s.Records.CommitSnapshot(snapshot)
}

// rejectDestinationLinks prevents a pre-existing link from redirecting a member.
func rejectDestinationLinks(root *os.Root, relative string) error {
	for p := relative; p != "."; p = filepath.Dir(p) {
		info, err := root.Lstat(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("snapshot destination contains symlink %q", p)
		}
	}
	return nil
}

// restoreSnapshot verifies the complete bundle before changing any checkout files.
func (s *Service) restoreSnapshot(snapshot models.PlanSnapshot, planPath, showPath, root, workflowRoot string, selective bool) error {
	if err := snapshot.Manifest.Validate(); err != nil {
		return err
	}
	staging, err := os.MkdirTemp("", "atlantis-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	archive, err := os.CreateTemp(staging, "bundle-")
	if err != nil {
		return err
	}
	defer archive.Close()
	input, err := s.Blobs.OpenSnapshot(snapshot.JobID)
	if err != nil {
		return err
	}
	hash := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(archive, hash), io.LimitReader(input, snapshot.Size+1))
	closeErr := input.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if size != snapshot.Size || fmt.Sprintf("%x", hash.Sum(nil)) != snapshot.SHA256 {
		return fmt.Errorf("plan snapshot checksum mismatch")
	}
	if _, err = archive.Seek(0, io.SeekStart); err != nil {
		return err
	}
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	first, err := reader.Next()
	if err != nil {
		return err
	}
	if first.Name != "manifest.json" || first.Typeflag != tar.TypeReg {
		return fmt.Errorf("snapshot manifest is missing")
	}
	expected, err := json.Marshal(snapshot.Manifest)
	if err != nil {
		return err
	}
	if first.Size != int64(len(expected)) {
		return fmt.Errorf("snapshot manifest differs from database")
	}
	manifest, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	if !bytes.Equal(manifest, expected) {
		return fmt.Errorf("snapshot manifest differs from database")
	}
	for index, file := range snapshot.Manifest.Files {
		header, err := reader.Next()
		if err != nil {
			return err
		}
		kind := byte(tar.TypeReg)
		if os.FileMode(file.Mode).IsDir() {
			kind = tar.TypeDir
		}
		if header.Name != archiveName(file) || header.Typeflag != kind || header.Linkname != "" || header.Size != file.Size || header.Mode != int64(os.FileMode(file.Mode).Perm()) {
			return fmt.Errorf("invalid snapshot archive member %q", header.Name)
		}
		if kind == tar.TypeDir {
			continue
		}
		output, err := os.Create(filepath.Join(staging, fmt.Sprint(index)))
		if err != nil {
			return err
		}
		hash := sha256.New()
		size, err := io.Copy(io.MultiWriter(output, hash), reader)
		closeErr := output.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if size != file.Size || fmt.Sprintf("%x", hash.Sum(nil)) != file.SHA256 {
			return fmt.Errorf("snapshot member %q checksum mismatch", file.Path)
		}
	}
	if _, err = reader.Next(); err != io.EOF {
		return fmt.Errorf("unexpected snapshot archive member")
	}
	// Drain gzip to verify its checksum, including any trailing compressed data.
	if _, err = io.Copy(io.Discard, gz); err != nil {
		return err
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return err
	}
	destination, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer destination.Close()
	for index, file := range snapshot.Manifest.Files {
		if selective && file.Kind != "plan" && file.Kind != "show" {
			continue
		}
		target := filepath.Join(workflowRoot, filepath.FromSlash(file.Path))
		if file.Kind == "plan" {
			target = planPath
		}
		if file.Kind == "show" {
			target = showPath
		}
		relative, err := filepath.Rel(root, target)
		if err != nil || !filepath.IsLocal(relative) || relative == "." {
			return fmt.Errorf("snapshot destination is outside assigned checkout")
		}
		if err := rejectDestinationLinks(destination, relative); err != nil {
			return err
		}
		if os.FileMode(file.Mode).IsDir() {
			if err = destination.MkdirAll(relative, os.FileMode(file.Mode).Perm()); err != nil {
				return err
			}
			continue
		}
		if err = destination.MkdirAll(filepath.Dir(relative), 0700); err != nil {
			return err
		}
		temporary := filepath.Join(filepath.Dir(relative), ".snapshot-"+uuid.NewString())
		output, err := destination.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(file.Mode).Perm())
		if err != nil {
			return err
		}
		input, err := os.Open(filepath.Join(staging, fmt.Sprint(index)))
		if err == nil {
			_, err = io.Copy(output, input)
			errClose := input.Close()
			if err == nil {
				err = errClose
			}
		}
		if err == nil {
			err = output.Sync()
		}
		errClose := output.Close()
		if err == nil {
			err = errClose
		}
		if err == nil {
			err = destination.Rename(temporary, relative)
		}
		if err != nil {
			_ = destination.Remove(temporary)
			return err
		}
	}
	return nil
}
