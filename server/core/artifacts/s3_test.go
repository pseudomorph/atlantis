// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package artifacts_test

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/google/uuid"
	"github.com/runatlantis/atlantis/server/core/artifacts"
	"github.com/runatlantis/atlantis/server/core/artifacts/artifactstest"
	. "github.com/runatlantis/atlantis/testing"
)

func newS3(t *testing.T, fake *artifactstest.S3, prefix string) *artifacts.S3 {
	t.Helper()
	store, err := artifacts.NewS3(fake, fake.Bucket, prefix, filepath.Join(t.TempDir(), "uploads"))
	Ok(t, err)
	return store
}

func reader(t *testing.T) func(io.ReadCloser, error) string {
	return func(r io.ReadCloser, err error) string {
		t.Helper()
		Ok(t, err)
		data, err := io.ReadAll(r)
		Ok(t, err)
		Ok(t, r.Close())
		return string(data)
	}
}

func TestS3PutOpenDelete(t *testing.T) {
	fake := artifactstest.New("artifacts")
	store := newS3(t, fake, "")
	meta, err := store.Put("snapshots/v1/a/plan-snapshot.tar.gz", strings.NewReader("plan bytes"))
	Ok(t, err)
	Equals(t, artifacts.Metadata{Size: 10, SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("plan bytes")))}, meta)
	Equals(t, "plan bytes", reader(t)(store.Open("snapshots/v1/a/plan-snapshot.tar.gz")))
	objects, err := store.List()
	Ok(t, err)
	Equals(t, 1, len(objects))
	Equals(t, "snapshots/v1/a/plan-snapshot.tar.gz", objects[0].Key)
	Assert(t, !objects[0].ModifiedAt.IsZero(), "listing lost the modification time")
	Ok(t, store.Delete("snapshots/v1/a/plan-snapshot.tar.gz"))
	Ok(t, store.Delete("snapshots/v1/a/plan-snapshot.tar.gz"))
	_, err = store.Open("snapshots/v1/a/plan-snapshot.tar.gz")
	var missing *types.NoSuchKey
	Assert(t, errors.As(err, &missing), "missing object error lost its cause: %v", err)
	Equals(t, 0, len(fake.Keys()))
}

func TestS3FailedUploadStoresNothing(t *testing.T) {
	fake := artifactstest.New("artifacts")
	fake.FailPut = errors.New("service unavailable")
	spool := filepath.Join(t.TempDir(), "uploads")
	store, err := artifacts.NewS3(fake, fake.Bucket, "", spool)
	Ok(t, err)
	_, err = store.Put("logs/v1/job", strings.NewReader("output"))
	ErrContains(t, "service unavailable", err)
	Equals(t, 0, len(fake.Keys()))
	entries, err := os.ReadDir(spool)
	Ok(t, err)
	Equals(t, 0, len(entries))
}

func TestS3ClearsStaleUploadStaging(t *testing.T) {
	spool := filepath.Join(t.TempDir(), "uploads")
	Ok(t, os.MkdirAll(spool, 0700))
	Ok(t, os.WriteFile(filepath.Join(spool, "upload-crashed"), []byte("secret plan"), 0600))
	fake := artifactstest.New("artifacts")
	store, err := artifacts.NewS3(fake, fake.Bucket, "", spool)
	Ok(t, err)
	entries, err := os.ReadDir(spool)
	Ok(t, err)
	Equals(t, 0, len(entries))
	_, err = store.Put("logs/v1/job", strings.NewReader("output"))
	Ok(t, err)
	entries, err = os.ReadDir(spool)
	Ok(t, err)
	Equals(t, 0, len(entries))
}

func TestS3RangedReads(t *testing.T) {
	fake := artifactstest.New("artifacts")
	store := newS3(t, fake, "atlantis")
	_, err := store.Put("logs/v1/job", strings.NewReader("0123456789"))
	Ok(t, err)
	Equals(t, "345", reader(t)(store.OpenRange("logs/v1/job", 3, 3)))
	Equals(t, "89", reader(t)(store.OpenRange("logs/v1/job", 8, 10)))
	Equals(t, []string{"bytes=3-5", "bytes=8-17"}, fake.Ranges())
	for _, bad := range [][2]int64{{-1, 1}, {0, 0}, {1, -1}} {
		_, err = store.OpenRange("logs/v1/job", bad[0], bad[1])
		Assert(t, err != nil, "accepted range %v", bad)
	}
	_, err = store.OpenRange("logs/v1/job", 10, 1)
	Assert(t, err != nil, "read past the end of the object")
	fake.IgnoreRange = true
	_, err = store.OpenRange("logs/v1/job", 3, 3)
	ErrContains(t, "service returned range", err)
}

func TestS3PrefixConfinement(t *testing.T) {
	fake := artifactstest.New("shared")
	store := newS3(t, fake, "team/atlantis/")
	_, err := store.Put("snapshots/v1/job/plan-snapshot.tar.gz", strings.NewReader("bundle"))
	Ok(t, err)
	Equals(t, []string{"team/atlantis/snapshots/v1/job/plan-snapshot.tar.gz"}, fake.Keys())
	old := time.Now().Add(-time.Hour)
	for _, key := range []string{"team/atlantis-other/logs/v1/job", "team/other", "team/atlantis//double", "team/atlantis/../escape", "unrelated"} {
		fake.Set(key, []byte("foreign"), old)
	}
	objects, err := store.List()
	Ok(t, err)
	Equals(t, 1, len(objects))
	Equals(t, "snapshots/v1/job/plan-snapshot.tar.gz", objects[0].Key)
	for _, key := range []string{"", ".", "..", "../team/other", "/absolute", "a//b", "a/./b", "a\\b"} {
		_, err = store.Put(key, strings.NewReader("x"))
		Assert(t, err != nil, "Put accepted key %q", key)
		_, err = store.Open(key)
		Assert(t, err != nil, "Open accepted key %q", key)
		_, err = store.OpenRange(key, 0, 1)
		Assert(t, err != nil, "OpenRange accepted key %q", key)
		Assert(t, store.Delete(key) != nil, "Delete accepted key %q", key)
	}
	Equals(t, 6, len(fake.Keys()))
	for _, prefix := range []string{"../outside", "/absolute", "a//b", "."} {
		_, err = artifacts.NewS3(fake, fake.Bucket, prefix, t.TempDir())
		Assert(t, err != nil, "accepted prefix %q", prefix)
	}
	_, err = artifacts.NewS3(fake, "", "", t.TempDir())
	Assert(t, err != nil, "accepted an empty bucket")
	_, err = artifacts.NewS3(fake, fake.Bucket, "", "relative-uploads")
	Assert(t, err != nil, "accepted a relative staging directory")
}

func TestS3ListFollowsContinuation(t *testing.T) {
	fake := artifactstest.New("artifacts")
	fake.PageSize = 2
	store := newS3(t, fake, "p")
	want := map[string]bool{}
	for i := range 5 {
		key := "snapshots/v1/" + uuid.NewString() + "/plan-snapshot.tar.gz"
		want[key] = true
		_, err := store.Put(key, strings.NewReader(fmt.Sprint(i)))
		Ok(t, err)
	}
	objects, err := store.List()
	Ok(t, err)
	Equals(t, 5, len(objects))
	for _, o := range objects {
		Assert(t, want[o.Key], "unexpected key %q", o.Key)
	}
}

func isolateAWSConfig(t *testing.T) {
	t.Helper()
	empty := filepath.Join(t.TempDir(), "empty")
	Ok(t, os.WriteFile(empty, nil, 0600))
	t.Setenv("AWS_CONFIG_FILE", empty)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", empty)
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_PROFILE", "")
}

func TestNewS3ClientKeepsConfigurationCause(t *testing.T) {
	isolateAWSConfig(t)
	_, err := artifacts.NewS3Client(artifacts.S3Config{Bucket: "plans", Region: "us-east-1", Profile: "missing"})
	var missing awsconfig.SharedConfigProfileNotExistError
	Assert(t, errors.As(err, &missing), "expected the missing-profile cause, got %v", err)
	Assert(t, strings.HasPrefix(err.Error(), "loading AWS config: "), "unexpected message %q", err)
}

func TestNewS3ClientChecksBucket(t *testing.T) {
	isolateAWSConfig(t)
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	_, err := artifacts.NewS3Client(artifacts.S3Config{Bucket: "plans", Region: "us-east-1", Endpoint: server.URL, ForcePathStyle: true})
	Assert(t, err != nil && errors.Unwrap(err) != nil && strings.Contains(err.Error(), "403"), "expected the wrapped S3 response as the cause, got %v", err)
	Assert(t, strings.HasPrefix(err.Error(), `validating S3 bucket "plans": `), "unexpected message %q", err)
	Equals(t, "/plans", path)
	_, err = artifacts.NewS3Client(artifacts.S3Config{Region: "us-east-1"})
	Assert(t, err != nil, "accepted an empty bucket")
}

// The SDK client sends the staged upload's SHA-256 for the service to verify,
// asks for exact byte ranges, and reads the Content-Range of the response.
func TestS3ClientRequests(t *testing.T) {
	isolateAWSConfig(t)
	objects := map[string][]byte{}
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path+" "+r.Header.Get("Range"))
		switch r.Method {
		case http.MethodHead:
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			sum := sha256.Sum256(body)
			if r.Header.Get("X-Amz-Checksum-Sha256") != base64.StdEncoding.EncodeToString(sum[:]) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			objects[r.URL.Path] = body
		case http.MethodGet:
			if r.Header.Get("X-Amz-Checksum-Mode") != "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			body := objects[r.URL.Path]
			var start, end int
			if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
				_, _ = w.Write(body)
				return
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(body[start : end+1])
		case http.MethodDelete:
			delete(objects, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	client, err := artifacts.NewS3Client(artifacts.S3Config{Bucket: "plans", Region: "us-east-1", Endpoint: server.URL, ForcePathStyle: true})
	Ok(t, err)
	store, err := artifacts.NewS3(client, "plans", "team", filepath.Join(t.TempDir(), "uploads"))
	Ok(t, err)
	_, err = store.Put("logs/v1/job", strings.NewReader("0123456789"))
	Ok(t, err)
	Equals(t, "0123456789", reader(t)(store.Open("logs/v1/job")))
	Equals(t, "345", reader(t)(store.OpenRange("logs/v1/job", 3, 3)))
	Ok(t, store.Delete("logs/v1/job"))
	Equals(t, []string{"HEAD /plans ", "PUT /plans/team/logs/v1/job ", "GET /plans/team/logs/v1/job ", "GET /plans/team/logs/v1/job bytes=3-5", "DELETE /plans/team/logs/v1/job "}, requests)
}
