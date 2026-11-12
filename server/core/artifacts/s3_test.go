// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package artifacts_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/runatlantis/atlantis/server/core/artifacts"
)

func isolateAWSConfig(t *testing.T) {
	t.Helper()
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(empty, nil, 0600); err != nil {
		t.Fatal(err)
	}
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
	if !errors.As(err, &missing) {
		t.Fatalf("expected the missing-profile cause, got %v", err)
	}
	if !strings.HasPrefix(err.Error(), "loading AWS config: ") {
		t.Fatalf("unexpected message %q", err)
	}
}

func TestNewS3ClientKeepsBucketCause(t *testing.T) {
	isolateAWSConfig(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	_, err := artifacts.NewS3Client(artifacts.S3Config{Bucket: "plans", Region: "us-east-1", Endpoint: server.URL, ForcePathStyle: true})
	if err == nil || errors.Unwrap(err) == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("expected the wrapped S3 response as the cause, got %v", err)
	}
	if !strings.HasPrefix(err.Error(), `validating S3 bucket "plans": `) {
		t.Fatalf("unexpected message %q", err)
	}
}
