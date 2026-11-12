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
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hashicorp/go-version"
	"github.com/runatlantis/atlantis/server/core/db"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
)

type snapshotRecords struct {
	jobs  map[string]models.Job
	plans []models.Job
	db.SnapshotStore
	pending, ready *models.PlanSnapshot
	begins         int
}

func (r *snapshotRecords) BeginSnapshot(s models.PlanSnapshot) error {
	r.begins++
	if r.pending != nil {
		return fmt.Errorf("already published")
	}
	r.pending = &s
	return nil
}
func (r *snapshotRecords) CommitSnapshot(s models.PlanSnapshot) error         { r.ready = &s; return nil }
func (r *snapshotRecords) InputSnapshot(string) (*models.PlanSnapshot, error) { return r.ready, nil }
func (r *snapshotRecords) Snapshot(string) (*models.PlanSnapshot, error)      { return r.ready, nil }

func (r *snapshotRecords) Job(id string) (*models.Job, error) {
	j, ok := r.jobs[id]
	if !ok {
		return nil, nil
	}
	return &j, nil
}
func (r *snapshotRecords) PendingPlans(models.Repo, int) ([]models.Job, error) { return r.plans, nil }

type snapshotBytes struct {
	SnapshotBackend
	content []byte
	puts    int
	fail    bool
}

func (b *snapshotBytes) PutSnapshot(_ string, r io.Reader) (Metadata, error) {
	b.puts++
	data, err := io.ReadAll(r)
	if err != nil {
		return Metadata{}, err
	}
	if b.fail {
		return Metadata{}, fmt.Errorf("injected upload error")
	}
	b.content = data
	return Metadata{Size: int64(len(data)), SHA256: fmt.Sprintf("%x", sha256.Sum256(data))}, nil
}
func (b *snapshotBytes) OpenSnapshot(string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(b.content)), nil
}
func bundleFixture(t *testing.T) (*Service, *snapshotRecords, *snapshotBytes, command.ProjectContext, string) {
	t.Helper()
	root := t.TempDir()
	records := &snapshotRecords{}
	blobs := &snapshotBytes{}
	service := &Service{Records: records, Blobs: blobs, WorkflowFiles: []string{".artifacts"}}
	ctx := command.ProjectContext{ArtifactRoot: root, JobID: uuid.NewString()}
	for name, data := range map[string]string{"project.tfplan": "raw saved plan", ".terraform.lock.hcl": "provider lock", ".artifacts/nested/result": "nested workflow file", ".artifacts/empty/": ""} {
		if name[len(name)-1] == '/' {
			if err := os.MkdirAll(filepath.Join(root, name), 0700); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return service, records, blobs, ctx, root
}
func TestPlanBundleNestedRoundTripAndSelectiveRestore(t *testing.T) {
	service, records, _, ctx, root := bundleFixture(t)
	if err := service.SnapshotPlan(ctx, root, filepath.Join(root, "project.tfplan")); err != nil {
		t.Fatal(err)
	}
	plan, ok := records.ready.Manifest.File("plan")
	if !ok || plan.SHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte("raw saved plan"))) {
		t.Fatal("raw plan checksum was not retained")
	}
	destination := t.TempDir()
	if err := service.restoreSnapshot(*records.ready, filepath.Join(destination, "restored.tfplan"), filepath.Join(destination, "show.json"), destination, destination, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(destination, ".artifacts/nested/result"))
	if err != nil || string(data) != "nested workflow file" {
		t.Fatalf("nested restore: %q %v", data, err)
	}
	if info, err := os.Stat(filepath.Join(destination, ".artifacts/empty")); err != nil || !info.IsDir() {
		t.Fatalf("empty directory restore: %v", err)
	}
	if err := os.WriteFile(filepath.Join(destination, DependencyLockFile), []byte("initialized lock"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := service.restoreSnapshot(*records.ready, filepath.Join(destination, "restored.tfplan"), filepath.Join(destination, "show.json"), destination, destination, true); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(destination, DependencyLockFile))
	if err != nil || string(data) != "initialized lock" {
		t.Fatalf("selective restore overwrote initialized lock: %q %v", data, err)
	}
	if err := service.SnapshotPlan(ctx, root, filepath.Join(root, "project.tfplan")); err == nil {
		t.Fatal("second publication succeeded")
	}
}
func TestPlanBundleCaptureRejectsUnsafeSourcesBeforePublication(t *testing.T) {
	for _, attack := range []string{"symlink", "hardlink", "traversal", "state", "cache"} {
		t.Run(attack, func(t *testing.T) {
			service, records, blobs, ctx, root := bundleFixture(t)
			switch attack {
			case "symlink":
				if err := os.Symlink(filepath.Join(root, "project.tfplan"), filepath.Join(root, ".artifacts/link")); err != nil {
					t.Skip(err)
				}
			case "hardlink":
				if err := os.Link(filepath.Join(root, "project.tfplan"), filepath.Join(root, ".artifacts/link")); err != nil {
					t.Skip(err)
				}
			case "traversal":
				service.WorkflowFiles = []string{"../outside"}
			case "state":
				if err := os.WriteFile(filepath.Join(root, ".artifacts/terraform.tfstate"), []byte("state"), 0600); err != nil {
					t.Fatal(err)
				}
			case "cache":
				service.WorkflowFiles = []string{".terraform"}
				if err := os.Mkdir(filepath.Join(root, ".terraform"), 0700); err != nil {
					t.Fatal(err)
				}
			case "duplicate":
				service.WorkflowFiles = []string{"project.tfplan"}
			}
			if err := service.SnapshotPlan(ctx, root, filepath.Join(root, "project.tfplan")); err == nil {
				t.Fatal("unsafe capture succeeded")
			}
			if records.begins != 0 || blobs.puts != 0 || records.ready != nil {
				t.Fatal("incomplete capture was published")
			}
		})
	}
}
func TestPlanBundleUploadFailureIsNotReady(t *testing.T) {
	service, records, blobs, ctx, root := bundleFixture(t)
	blobs.fail = true
	if err := service.SnapshotPlan(ctx, root, filepath.Join(root, "project.tfplan")); err == nil {
		t.Fatal("upload succeeded")
	}
	if records.ready != nil {
		t.Fatal("partial snapshot became ready")
	}
}
func TestPlanBundleRejectsArchiveLinksTraversalAndCorruptionBeforeRestore(t *testing.T) {
	for _, attack := range []string{"symlink", "hardlink", "traversal", "duplicate", "checksum", "manifest"} {
		t.Run(attack, func(t *testing.T) {
			service, records, blobs, ctx, root := bundleFixture(t)
			if err := service.SnapshotPlan(ctx, root, filepath.Join(root, "project.tfplan")); err != nil {
				t.Fatal(err)
			}
			if attack == "checksum" {
				blobs.content[len(blobs.content)/2] ^= 1
			} else {
				var output bytes.Buffer
				gz := gzip.NewWriter(&output)
				writer := tar.NewWriter(gz)
				manifest, _ := json.Marshal(records.ready.Manifest)
				if attack == "manifest" {
					manifest = bytes.ReplaceAll(manifest, []byte("raw"), []byte("bad"))
					manifest = append(manifest, ' ')
				}
				if err := writer.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Typeflag: tar.TypeReg, Size: int64(len(manifest))}); err != nil {
					t.Fatal(err)
				}
				_, _ = writer.Write(manifest)
				f := records.ready.Manifest.Files[0]
				header := &tar.Header{Name: archiveName(f), Mode: 0600, Typeflag: tar.TypeReg, Size: f.Size}
				switch attack {
				case "symlink":
					header.Typeflag = tar.TypeSymlink
					header.Linkname = "/outside"
					header.Size = 0
				case "hardlink":
					header.Typeflag = tar.TypeLink
					header.Linkname = "/outside"
					header.Size = 0
				case "traversal":
					header.Name = "../outside"
					header.Size = 0
				case "duplicate":
					header.Name = "manifest.json"
					header.Size = 0
				}
				if err := writer.WriteHeader(header); err != nil {
					t.Fatal(err)
				}
				if header.Size > 0 {
					_, _ = writer.Write(make([]byte, header.Size))
				}
				_ = writer.Close()
				_ = gz.Close()
				blobs.content = output.Bytes()
				records.ready.Size = int64(len(blobs.content))
				records.ready.SHA256 = fmt.Sprintf("%x", sha256.Sum256(blobs.content))
			}
			destination := t.TempDir()
			if err := service.restoreSnapshot(*records.ready, filepath.Join(destination, "restored.tfplan"), filepath.Join(destination, "show.json"), destination, destination, false); err == nil {
				t.Fatal("unsafe restore succeeded")
			}
			entries, err := os.ReadDir(destination)
			if err != nil || len(entries) != 0 {
				t.Fatal("invalid archive changed destination")
			}
		})
	}
}

func TestPlanBundleLoadChecksRawPlanChecksum(t *testing.T) {
	service, records, _, ctx, root := bundleFixture(t)
	if err := service.SnapshotPlan(ctx, root, filepath.Join(root, "project.tfplan")); err != nil {
		t.Fatal(err)
	}
	producer := models.Job{ID: ctx.JobID, Revision: "revision", ManagedPlan: true}
	consumerID := uuid.NewString()
	records.plans = []models.Job{producer}
	records.jobs = map[string]models.Job{consumerID: {ID: consumerID, PlanJobID: producer.ID}}
	ctx.JobID = consumerID
	ctx.Pull.HeadCommit = "revision"
	ctx.ExpectedPlanHash = "wrong checksum"
	destination := filepath.Join(root, "restored.tfplan")
	if err := service.Load(ctx, destination); err == nil {
		t.Fatal("wrong raw plan checksum accepted")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("mismatched plan changed checkout")
	}
	file, _ := records.ready.Manifest.File("plan")
	ctx.ExpectedPlanHash = file.SHA256
	if err := service.Load(ctx, destination); err != nil {
		t.Fatal(err)
	}
}
func TestPlanBundleInitializationMetadataStagesWithPlan(t *testing.T) {
	for _, fixture := range []string{"invalid", "valid", "no provider lock", "resources", "outputs", "check_results"} {
		t.Run(fixture, func(t *testing.T) {
			valid := fixture == "valid" || fixture == "no provider lock"
			service, records, blobs, ctx, root := bundleFixture(t)
			ctx.RequiresAtlantisManagedPlanFile = true
			ctx.TerraformVersion = version.Must(version.NewVersion("1.1.0"))
			distribution := "terraform"
			ctx.TerraformDistribution = &distribution
			ctx.Workspace = "default"
			if err := os.Mkdir(filepath.Join(root, ".terraform"), 0700); err != nil {
				t.Fatal(err)
			}
			metadata := `{"resources":[{"sensitive":"actual state"}]}`
			if valid {
				metadata = `{"version":3,"serial":1,"lineage":"init-metadata","backend":{"type":"s3","config":{"bucket":"state"},"hash":123}}`
			}
			if fixture != "invalid" && !valid {
				metadata = fmt.Sprintf(`{"backend":{"type":"s3"},%q:[]}`, fixture)
			}
			if err := os.WriteFile(filepath.Join(root, ".terraform/terraform.tfstate"), []byte(metadata), 0600); err != nil {
				t.Fatal(err)
			}
			if fixture == "no provider lock" {
				if err := os.Remove(filepath.Join(root, DependencyLockFile)); err != nil {
					t.Fatal(err)
				}
			}
			err := service.SnapshotPlanWithEnvironment(ctx, root, filepath.Join(root, "project.tfplan"), map[string]string{"TF_WORKSPACE": "default"})
			if !valid {
				if err == nil || records.begins != 0 || blobs.puts != 0 {
					t.Fatal("invalid backend published part of a snapshot")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if records.ready.Manifest.DataDirectory != ".terraform" {
				t.Fatal("lost runtime directory")
			}
			if _, ok := records.ready.Manifest.File("backend"); !ok {
				t.Fatal("lost backend metadata")
			}
			if _, ok := records.ready.Manifest.File("workspace"); !ok {
				t.Fatal("lost workspace")
			}
		})
	}
}

func TestPlanBundleNormalizesPermissionsAndOverlappingDeclarations(t *testing.T) {
	service, records, _, ctx, root := bundleFixture(t)
	plan := filepath.Join(root, "project.tfplan")
	if err := os.Chmod(plan, 0666); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, ".artifacts", "nested")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "custom"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	service.WorkflowFiles = []string{"project.tfplan", ".artifacts", ".artifacts/nested"}
	if err := service.SnapshotPlan(ctx, root, plan); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, file := range records.ready.Manifest.Files {
		if seen[file.Path] {
			t.Fatalf("duplicate member %q", file.Path)
		}
		seen[file.Path] = true
		if os.FileMode(file.Mode).Perm()&0022 != 0 {
			t.Fatal("writable permissions persisted")
		}
	}
	if !seen["project.tfplan"] || !seen[".artifacts/nested/custom"] {
		t.Fatalf("missing members: %#v", records.ready.Manifest)
	}
}

func TestCustomPlanWithoutDeclaredOutputFailsBeforePublication(t *testing.T) {
	service, records, blobs, ctx, root := bundleFixture(t)
	ctx.RequiresAtlantisManagedPlanFile = false
	service.WorkflowFiles = nil
	if err := os.Remove(filepath.Join(root, "project.tfplan")); err != nil {
		t.Fatal(err)
	}
	if err := service.SnapshotPlan(ctx, root, filepath.Join(root, "missing.tfplan")); err == nil || !strings.Contains(err.Error(), "storage.workflow_files") {
		t.Fatalf("unexpected error %v", err)
	}
	if records.begins != 0 || blobs.puts != 0 {
		t.Fatal("failed custom plan published a snapshot")
	}
}
