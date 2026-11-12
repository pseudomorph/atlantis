// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package artifacts_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/runatlantis/atlantis/server/core/artifacts"
	. "github.com/runatlantis/atlantis/testing"
)

type objectClient struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func (c *objectClient) HeadBucket(context.Context, *s3.HeadBucketInput, ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	return &s3.HeadBucketOutput{}, nil
}

func (c *objectClient) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	c.objects[aws.ToString(in.Key)] = data
	return &s3.PutObjectOutput{}, nil
}

func (c *objectClient) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, ok := c.objects[aws.ToString(in.Key)]
	if !ok {
		return nil, os.ErrNotExist
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(data))}, nil
}

func (c *objectClient) DeleteObject(_ context.Context, in *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.objects, aws.ToString(in.Key))
	return &s3.DeleteObjectOutput{}, nil
}

func (c *objectClient) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := &s3.ListObjectsV2Output{}
	for key := range c.objects {
		if strings.HasPrefix(key, aws.ToString(in.Prefix)) {
			out.Contents = append(out.Contents, types.Object{Key: aws.String(key), LastModified: aws.Time(time.Now().UTC())})
		}
	}
	return out, nil
}

func TestArtifactBackendConformance(t *testing.T) {
	factories := map[string]func(*testing.T) artifacts.Store{
		"filesystem": func(t *testing.T) artifacts.Store {
			s, e := artifacts.NewFilesystem(t.TempDir())
			Ok(t, e)
			t.Cleanup(func() { _ = s.Close() })
			return s
		},

		"s3 API": func(t *testing.T) artifacts.Store {
			return artifacts.NewS3WithClient(&objectClient{objects: map[string][]byte{}}, "test-bucket", "configured-prefix")
		},
	}
	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			backend := factory(t)
			scope := artifacts.Scoped{Store: backend, Prefix: "repository/pr/job"}
			content := []byte{0, 1, 2, 3, 255}
			metadata, err := scope.Put("plan", bytes.NewReader(content))
			Ok(t, err)
			Equals(t, int64(len(content)), metadata.Size)
			Equals(t, fmt.Sprintf("%x", sha256.Sum256(content)), metadata.SHA256)
			r, err := scope.Open("plan")
			Ok(t, err)
			got, err := io.ReadAll(r)
			Ok(t, err)
			Ok(t, r.Close())
			Equals(t, content, got)
			for _, bad := range []string{"../other", "/absolute", "a/../plan", "a\\plan", "a\x00plan", "."} {
				_, err := scope.Put(bad, strings.NewReader("bad"))
				Assert(t, err != nil, "write escaped scope %q", bad)
				_, err = scope.Open(bad)
				Assert(t, err != nil, "read escaped scope %q", bad)
				Assert(t, scope.Delete(bad) != nil, "delete escaped scope %q", bad)
			}
			objects, err := scope.List()
			Ok(t, err)
			Equals(t, 1, len(objects))
			Equals(t, "plan", objects[0].Key)
			other := artifacts.Scoped{Store: backend, Prefix: "repository/pr/other-job"}
			_, err = other.Open("plan")
			Assert(t, err != nil, "other job read artifact")
			Ok(t, scope.Delete("plan"))
			Ok(t, scope.Delete("plan"))
			_, err = scope.Open("plan")
			Assert(t, err != nil, "deleted object remained readable")
		})
	}
}
