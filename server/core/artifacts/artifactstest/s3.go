// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

// Package artifactstest provides an in-memory S3 service for artifact tests.
package artifactstest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type object struct {
	data     []byte
	modified time.Time
}

// S3 implements artifacts.S3Client for one bucket. It checks the request
// fields that a real service enforces: content length, SHA-256 checksum, and
// byte ranges.
type S3 struct {
	Bucket string
	// PageSize limits each ListObjectsV2 page; zero means 1000.
	PageSize int
	// IgnoreRange returns whole objects for ranged reads, as a service that
	// does not support Range would.
	IgnoreRange bool
	// FailPut, when set, fails every PutObject before storing anything.
	FailPut error

	mu      sync.Mutex
	objects map[string]object
	ranges  []string
	gets    int
}

func New(bucket string) *S3 {
	return &S3{Bucket: bucket, objects: map[string]object{}}
}

func (f *S3) bucket(name *string) error {
	if aws.ToString(name) != f.Bucket {
		return &types.NoSuchBucket{Message: aws.String("no such bucket " + aws.ToString(name))}
	}
	return nil
}

func (f *S3) HeadBucket(_ context.Context, in *s3.HeadBucketInput, _ ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	return &s3.HeadBucketOutput{}, f.bucket(in.Bucket)
}

func (f *S3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	if err := f.bucket(in.Bucket); err != nil {
		return nil, err
	}
	if f.FailPut != nil {
		return nil, f.FailPut
	}
	data, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	if in.ContentLength != nil && *in.ContentLength != int64(len(data)) {
		return nil, fmt.Errorf("content length %d differs from body length %d", *in.ContentLength, len(data))
	}
	if in.ChecksumSHA256 != nil {
		sum := sha256.Sum256(data)
		if *in.ChecksumSHA256 != base64.StdEncoding.EncodeToString(sum[:]) {
			return nil, fmt.Errorf("BadDigest: checksum mismatch")
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[aws.ToString(in.Key)] = object{data: data, modified: time.Now()}
	return &s3.PutObjectOutput{}, nil
}

func (f *S3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if err := f.bucket(in.Bucket); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	o, ok := f.objects[aws.ToString(in.Key)]
	if !ok {
		return nil, &types.NoSuchKey{Message: aws.String("no such key " + aws.ToString(in.Key))}
	}
	if in.Range == nil || f.IgnoreRange {
		return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(o.data)), ContentLength: aws.Int64(int64(len(o.data)))}, nil
	}
	f.ranges = append(f.ranges, *in.Range)
	start, end, err := parseRange(*in.Range)
	if err != nil {
		return nil, err
	}
	size := int64(len(o.data))
	if start >= size {
		return nil, fmt.Errorf("InvalidRange: %s beyond %d bytes", *in.Range, size)
	}
	end = min(end, size-1)
	return &s3.GetObjectOutput{
		Body:          io.NopCloser(bytes.NewReader(o.data[start : end+1])),
		ContentLength: aws.Int64(end - start + 1),
		ContentRange:  aws.String(fmt.Sprintf("bytes %d-%d/%d", start, end, size)),
	}, nil
}

func parseRange(header string) (int64, int64, error) {
	spec, ok := strings.CutPrefix(header, "bytes=")
	first, last, found := strings.Cut(spec, "-")
	if !ok || !found {
		return 0, 0, fmt.Errorf("unsupported range %q", header)
	}
	start, err := strconv.ParseInt(first, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("unsupported range %q", header)
	}
	end, err := strconv.ParseInt(last, 10, 64)
	if err != nil || end < start {
		return 0, 0, fmt.Errorf("unsupported range %q", header)
	}
	return start, end, nil
}

func (f *S3) DeleteObject(_ context.Context, in *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	if err := f.bucket(in.Bucket); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, aws.ToString(in.Key))
	return &s3.DeleteObjectOutput{}, nil
}

func (f *S3) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	if err := f.bucket(in.Bucket); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	keys := f.keys(aws.ToString(in.Prefix))
	if token := aws.ToString(in.ContinuationToken); token != "" {
		i, found := slices.BinarySearch(keys, token)
		if !found {
			return nil, fmt.Errorf("invalid continuation token %q", token)
		}
		keys = keys[i:]
	}
	page := f.PageSize
	if page <= 0 {
		page = 1000
	}
	out := &s3.ListObjectsV2Output{IsTruncated: aws.Bool(false)}
	if len(keys) > page {
		out.IsTruncated = aws.Bool(true)
		out.NextContinuationToken = aws.String(keys[page])
		keys = keys[:page]
	}
	for _, key := range keys {
		out.Contents = append(out.Contents, types.Object{Key: aws.String(key), LastModified: aws.Time(f.objects[key].modified), Size: aws.Int64(int64(len(f.objects[key].data)))})
	}
	return out, nil
}

func (f *S3) keys(prefix string) []string {
	var keys []string
	for key := range f.objects {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys
}

// Keys returns every stored object key in order.
func (f *S3) Keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.keys("")
}

// Object returns a copy of the stored bytes for key.
func (f *S3) Object(key string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.objects[key]
	return slices.Clone(o.data), ok
}

// Set stores data under key with the given modification time, as another
// writer or an earlier process would have.
func (f *S3) Set(key string, data []byte, modified time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = object{data: slices.Clone(data), modified: modified}
}

// Ranges returns the Range headers of ranged reads in order.
func (f *S3) Ranges() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.ranges)
}

// Gets returns how many GetObject calls were made.
func (f *S3) Gets() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gets
}
