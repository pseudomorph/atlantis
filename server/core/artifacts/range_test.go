// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package artifacts_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/runatlantis/atlantis/server/core/artifacts"
	. "github.com/runatlantis/atlantis/testing"
)

type rangeClient struct {
	*objectClient
	key, byteRange string
}

func (c *rangeClient) GetObject(_ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	c.key, c.byteRange = aws.ToString(input.Key), aws.ToString(input.Range)
	return &s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader("3456"))}, nil
}
func TestScopedRangeReads(t *testing.T) {
	fs, err := artifacts.NewFilesystem(t.TempDir())
	Ok(t, err)
	defer fs.Close()
	scope := artifacts.Scoped{Store: fs, Prefix: "logs/v1"}
	_, err = scope.Put("job", strings.NewReader("0123456789"))
	Ok(t, err)
	reader, err := scope.OpenRange("job", 3, 4)
	Ok(t, err)
	data, err := io.ReadAll(reader)
	Ok(t, err)
	Ok(t, reader.Close())
	Equals(t, "3456", string(data))
	for _, key := range []string{"../other", "/absolute", "a/../job"} {
		_, err := scope.OpenRange(key, 0, 1)
		Assert(t, err != nil, "range escaped scope")
	}
	client := &rangeClient{objectClient: &objectClient{objects: map[string][]byte{}}}
	cloud := artifacts.Scoped{Store: artifacts.NewS3WithClient(client, "bucket", "configured"), Prefix: "logs/v1"}
	reader, err = cloud.OpenRange("job", 3, 4)
	Ok(t, err)
	Ok(t, reader.Close())
	Equals(t, "configured/logs/v1/job", client.key)
	Equals(t, "bytes=3-6", client.byteRange)
	_, err = cloud.OpenRange("job", -1, 4)
	Assert(t, err != nil, "negative offset accepted")
}
