// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package artifacts

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/runatlantis/atlantis/server/core/db"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
)

type Service struct {
	WorkflowFiles []string
	Records       db.ArtifactStore
	Blobs         Store
}

func component(s string) string {
	if s == "" {
		return "_"
	}
	if s == "." || s == ".." {
		return strings.ReplaceAll(s, ".", "%2E")
	}
	return url.PathEscape(s)
}
func artifactPath(j models.Job, filename string) string {
	instance := j.Pull.BaseRepo.StorageIdentity()
	identity := fmt.Sprintf("%s|%s|%s|%s", instance.Host, instance.Port, instance.BasePath, j.Pull.BaseRepo.FullName)
	namespace := fmt.Sprintf("%x", sha256.Sum256([]byte(identity+"|"+j.Scope)))
	return path.Join(namespace, strconv.Itoa(j.Pull.Num), component(j.ID), filename)
}
func artifactName(j models.Job, kind string) string {
	name := j.Project.ProjectName
	if name == "" {
		name = j.Workspace
	}
	extension := map[string]string{"plan": "tfplan", "show": "json", "log": "log"}[kind]
	return component(name) + "." + extension
}

func (s *Service) SaveFile(ctx command.ProjectContext, kind, filename string) error {
	root := ctx.ArtifactRoot
	if root == "" {
		return fmt.Errorf("artifact source scope is missing")
	}

	relative, err := filepath.Rel(root, filename)
	if err != nil || !filepath.IsLocal(relative) {
		return fmt.Errorf("artifact source is outside assigned working directory")
	}
	f, err := os.OpenInRoot(root, relative)
	if err != nil {
		return err
	}
	defer f.Close()
	j, err := s.Records.Job(ctx.JobID)
	if err != nil {
		return err
	}
	if j == nil {
		return fmt.Errorf("job %q is missing", ctx.JobID)
	}
	return s.Publish(*j, kind, relative, f)
}

// Publish records an intent before writing bytes and exposes only completed objects.
func (s *Service) Publish(j models.Job, kind, relative string, content io.Reader) error {
	id := uuid.NewString()
	key := path.Join(id, artifactName(j, kind))
	if kind == "log" {
		key = "logs/" + id
	}
	scope := Scoped{Store: s.Blobs, Prefix: artifactPath(j, "")}
	a := models.Artifact{ID: id, JobID: j.ID, Kind: kind, RelativePath: relative, Location: scope.Prefix + "/" + key}
	if err := s.Records.BeginArtifact(a); err != nil {
		return err
	}
	meta, err := scope.Put(key, content)
	if err != nil {
		return fmt.Errorf("writing %s artifact: %w", kind, err)
	}
	a.Size, a.SHA256 = meta.Size, meta.SHA256
	return s.Records.CommitArtifact(a)
}

func validArtifactLocation(j models.Job, location string) bool {
	prefix := artifactPath(j, "") + "/"
	return ValidKey(location) && strings.HasPrefix(location, prefix)
}
func (s *Service) openArtifact(a models.Artifact) (io.ReadCloser, error) {
	j, err := s.Records.Job(a.JobID)
	if err != nil {
		return nil, err
	}
	if j == nil {
		return nil, fmt.Errorf("artifact job is missing")
	}
	if !validArtifactLocation(*j, a.Location) {
		return nil, fmt.Errorf("artifact is outside its assigned job scope")
	}
	scope := Scoped{Store: s.Blobs, Prefix: artifactPath(*j, "")}
	return scope.Open(strings.TrimPrefix(a.Location, scope.Prefix+"/"))
}
func (s *Service) ReadLog(id string) ([]byte, error) {
	j, err := s.Records.Job(id)
	if err != nil {
		return nil, err
	}
	if j == nil {
		return nil, fmt.Errorf("job is missing")
	}
	data, _, err := s.ReadLogAfter(*j, 0)
	return data, err
}

// ReadLogAfter verifies each immutable chunk once per subscriber.
func (s *Service) ReadLogAfter(j models.Job, after int64) ([]byte, int64, error) {
	records, err := s.Records.LogArtifactsAfter(j.ID, after)
	if err != nil {
		return nil, after, err
	}
	var out []byte
	scope := Scoped{Store: s.Blobs, Prefix: artifactPath(j, "")}
	for _, a := range records {
		if a.JobID != j.ID || !validArtifactLocation(j, a.Location) {
			return nil, after, fmt.Errorf("log artifact is outside its assigned job scope")
		}
		reader, err := scope.Open(strings.TrimPrefix(a.Location, scope.Prefix+"/"))
		if err != nil {
			return nil, after, err
		}
		data, err := io.ReadAll(io.LimitReader(reader, a.Size+1))
		closeErr := reader.Close()
		if err != nil {
			return nil, after, err
		}
		if closeErr != nil {
			return nil, after, closeErr
		}
		if int64(len(data)) != a.Size || fmt.Sprintf("%x", sha256.Sum256(data)) != a.SHA256 {
			return nil, after, fmt.Errorf("log artifact checksum mismatch")
		}
		out = append(out, data...)
		after = a.Sequence
	}
	return out, after, nil
}
func (s *Service) Restore(a models.Artifact, destination, root string) error {
	r, err := s.openArtifact(a)
	if err != nil {
		return err
	}
	defer r.Close()
	relative, err := filepath.Rel(root, destination)
	if err != nil || !filepath.IsLocal(relative) {
		return fmt.Errorf("artifact destination is outside its assigned directory")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	directory, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := directory.MkdirAll(filepath.Dir(relative), 0700); err != nil {
		return err
	}
	temporary := filepath.Join(filepath.Dir(relative), ".artifact-"+uuid.NewString())
	f, err := directory.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer directory.Remove(temporary) // nolint: errcheck
	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(f, h), r)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if size != a.Size || fmt.Sprintf("%x", h.Sum(nil)) != a.SHA256 {
		return fmt.Errorf("artifact %q checksum mismatch", a.ID)
	}
	return directory.Rename(temporary, relative)
}

// SnapshotWorkflow captures operator-declared files passed between workflow stages.
func (s *Service) SnapshotWorkflow(ctx command.ProjectContext, projectDirectory string) error {
	for _, file := range s.WorkflowFiles {
		if !filepath.IsLocal(file) || file == "." {
			return fmt.Errorf("invalid workflow artifact path")
		}
		name := filepath.Join(projectDirectory, file)
		if _, err := os.Stat(name); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		if err := s.SaveFile(ctx, "workflow", name); err != nil {
			return err
		}
	}
	return nil
}

// SnapshotPlan captures the final workflow output once, before the plan job can become pending.
func (s *Service) SnapshotPlan(ctx command.ProjectContext, directory, planPath string) error {
	files := []struct{ kind, name string }{{"plan", planPath}, {"show", filepath.Join(directory, ctx.GetShowResultFileName())}}
	for _, file := range files {
		if _, err := os.Stat(file.name); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		if err := s.SaveFile(ctx, file.kind, file.name); err != nil {
			return err
		}
	}
	return s.SnapshotWorkflow(ctx, directory)
}
