// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package artifacts_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/runatlantis/atlantis/server/core/artifacts"
	. "github.com/runatlantis/atlantis/testing"
)

// TestS3AgainstService runs the S3 store against a real S3-compatible service.
// It runs only when ATLANTIS_TEST_S3_ENDPOINT is set, for example with MinIO:
//
//	docker run --rm -d -p 9000:9000 -e MINIO_ROOT_USER=minio -e MINIO_ROOT_PASSWORD=minio123 minio/minio server /data
//	ATLANTIS_TEST_S3_ENDPOINT=http://127.0.0.1:9000 AWS_ACCESS_KEY_ID=minio AWS_SECRET_ACCESS_KEY=minio123 \
//	  go test -run TestS3AgainstService ./server/core/artifacts/
func TestS3AgainstService(t *testing.T) {
	endpoint := os.Getenv("ATLANTIS_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("set ATLANTIS_TEST_S3_ENDPOINT to run against an S3-compatible service")
	}
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = "us-east-1"
	}
	bucket := "atlantis-test-" + strings.ToLower(uuid.NewString()[:8])
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	config, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	Ok(t, err)
	admin := s3.NewFromConfig(config, func(o *s3.Options) {
		o.UsePathStyle = true
		o.BaseEndpoint = aws.String(endpoint)
	})
	_, err = admin.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	Ok(t, err)
	t.Cleanup(func() {
		listed, _ := admin.ListObjectsV2(context.Background(), &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
		if listed != nil {
			for _, o := range listed.Contents {
				_, _ = admin.DeleteObject(context.Background(), &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: o.Key})
			}
		}
		_, _ = admin.DeleteBucket(context.Background(), &s3.DeleteBucketInput{Bucket: aws.String(bucket)})
	})

	client, err := artifacts.NewS3Client(artifacts.S3Config{Bucket: bucket, Region: region, Endpoint: endpoint, ForcePathStyle: true})
	Ok(t, err)
	store, err := artifacts.NewS3(client, bucket, "atlantis/", filepath.Join(t.TempDir(), "uploads"))
	Ok(t, err)
	read := reader(t)

	meta, err := store.Put("logs/v1/job", strings.NewReader("0123456789"))
	Ok(t, err)
	Equals(t, int64(10), meta.Size)
	Equals(t, "0123456789", read(store.Open("logs/v1/job")))
	Equals(t, "345", read(store.OpenRange("logs/v1/job", 3, 3)))
	Equals(t, "89", read(store.OpenRange("logs/v1/job", 8, 10)))
	_, err = store.OpenRange("logs/v1/job", 10, 1)
	Assert(t, err != nil, "read past the end of the object")

	_, err = admin.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("atlantis-other/foreign"), Body: strings.NewReader("x")})
	Ok(t, err)
	snapshots := artifacts.ObjectSnapshots{Store: store}
	id := uuid.NewString()
	_, err = snapshots.PutSnapshot(id, strings.NewReader("bundle"))
	Ok(t, err)
	objects, err := store.List()
	Ok(t, err)
	keys := map[string]bool{}
	for _, o := range objects {
		keys[o.Key] = true
	}
	Equals(t, map[string]bool{"logs/v1/job": true, "snapshots/v1/" + id + "/plan-snapshot.tar.gz": true}, keys)
	opened, err := snapshots.OpenSnapshot(id)
	Ok(t, err)
	data, err := io.ReadAll(opened)
	Ok(t, err)
	Ok(t, opened.Close())
	Equals(t, "bundle", string(data))
	// Collection uses the service's modification times; a fresh orphan survives.
	Ok(t, snapshots.CollectOrphans(map[string]bool{}, true, time.Now()))
	Equals(t, "bundle", read(snapshots.OpenSnapshot(id)))
	Ok(t, snapshots.CollectOrphans(map[string]bool{}, true, time.Now().Add(2*artifacts.OrphanGrace)))
	_, err = snapshots.OpenSnapshot(id)
	Assert(t, err != nil, "stale orphan snapshot remained")
	Ok(t, store.Delete("logs/v1/job"))
	Ok(t, store.Delete("logs/v1/job"))
	_, err = admin.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String("atlantis-other/foreign")})
	Ok(t, err)
}
