// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package artifacts

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3Client is the subset of the S3 API that artifact storage uses.
type S3Client interface {
	HeadBucket(ctx context.Context, params *s3.HeadBucketInput, optFns ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

// S3Config selects an S3-compatible bucket and optional key prefix.
type S3Config struct {
	Bucket         string
	Region         string
	Prefix         string
	Endpoint       string
	ForcePathStyle bool
	Profile        string
}

const (
	// s3SetupTimeout bounds configuration loading and the bucket check at startup.
	s3SetupTimeout = 10 * time.Second
	// s3RequestTimeout bounds requests without a large body.
	s3RequestTimeout = 30 * time.Second
	// s3TransferTimeout bounds an upload or a download including its body.
	s3TransferTimeout = 10 * time.Minute
)

// NewS3Client loads the AWS SDK default credential chain and checks that the
// bucket is reachable.
func NewS3Client(c S3Config) (S3Client, error) {
	if c.Bucket == "" {
		return nil, fmt.Errorf("S3 bucket is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), s3SetupTimeout)
	defer cancel()
	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(c.Region)}
	if c.Profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(c.Profile))
	}
	config, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	client := s3.NewFromConfig(config, func(o *s3.Options) {
		// Atlantis verifies artifact checksums itself: snapshot manifests and
		// sealed log digests cover every read, including ranged reads that
		// carry no service checksum.
		o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
		o.UsePathStyle = c.ForcePathStyle
		if c.Endpoint != "" {
			o.BaseEndpoint = aws.String(c.Endpoint)
		}
	})
	if _, err = client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(c.Bucket)}); err != nil {
		return nil, fmt.Errorf("validating S3 bucket %q: %w", c.Bucket, err)
	}
	return client, nil
}

// S3 stores each artifact as one object under an optional key prefix. Objects
// are written with a single PutObject, so a key is either absent or complete;
// there are no temporary objects to collect. Uploads are spooled to a local
// directory first so the request carries a known length and SHA-256 checksum
// that the service verifies before it stores the object.
type S3 struct {
	client         S3Client
	bucket, prefix string
	spool          string
}

// NewS3 returns an S3 store. spool is a private directory for upload staging;
// NewS3 creates it and removes files a previous process left there.
func NewS3(client S3Client, bucket, prefix, spool string) (*S3, error) {
	prefix = strings.TrimSuffix(prefix, "/")
	if bucket == "" {
		return nil, fmt.Errorf("S3 bucket is required")
	}
	if prefix != "" && !ValidKey(prefix) {
		return nil, fmt.Errorf("invalid S3 artifact prefix %q", prefix)
	}
	if !filepath.IsAbs(spool) {
		return nil, fmt.Errorf("S3 upload staging directory must be an absolute path")
	}
	if err := os.RemoveAll(spool); err != nil {
		return nil, fmt.Errorf("clearing S3 upload staging directory: %w", err)
	}
	if err := os.MkdirAll(spool, 0700); err != nil {
		return nil, fmt.Errorf("creating S3 upload staging directory: %w", err)
	}
	return &S3{client: client, bucket: bucket, prefix: prefix, spool: spool}, nil
}

func (s *S3) key(key string) (string, error) {
	if !ValidKey(key) {
		return "", fmt.Errorf("invalid artifact key")
	}
	if s.prefix != "" {
		return s.prefix + "/" + key, nil
	}
	return key, nil
}

func (s *S3) listPrefix() string {
	if s.prefix == "" {
		return ""
	}
	return s.prefix + "/"
}

func (s *S3) Close() error { return nil }

func (s *S3) Put(key string, r io.Reader) (Metadata, error) {
	object, err := s.key(key)
	if err != nil {
		return Metadata{}, err
	}
	file, err := os.CreateTemp(s.spool, "upload-*")
	if err != nil {
		return Metadata{}, fmt.Errorf("staging artifact upload: %w", err)
	}
	defer os.Remove(file.Name()) // nolint: errcheck
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(file, hash), r)
	if err != nil {
		return Metadata{}, fmt.Errorf("staging artifact upload: %w", err)
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return Metadata{}, fmt.Errorf("staging artifact upload: %w", err)
	}
	sum := hash.Sum(nil)
	ctx, cancel := context.WithTimeout(context.Background(), s3TransferTimeout)
	defer cancel()
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:         aws.String(s.bucket),
		Key:            aws.String(object),
		Body:           file,
		ContentLength:  aws.Int64(size),
		ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(sum)),
	})
	if err != nil {
		return Metadata{}, fmt.Errorf("uploading artifact %q: %w", key, err)
	}
	return Metadata{Size: size, SHA256: fmt.Sprintf("%x", sum)}, nil
}

func (s *S3) Open(key string) (io.ReadCloser, error) {
	object, err := s.key(key)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), s3TransferTimeout)
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(object)})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("opening artifact %q: %w", key, err)
	}
	return &cancelReader{ReadCloser: out.Body, cancel: cancel}, nil
}

// OpenRange reads length bytes starting at offset with one ranged GetObject.
// A response that does not start at offset is rejected, so a service that
// ignores the Range header cannot return the wrong bytes.
func (s *S3) OpenRange(key string, offset, length int64) (io.ReadCloser, error) {
	if !validRange(offset, length) {
		return nil, fmt.Errorf("invalid artifact range")
	}
	object, err := s.key(key)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), s3TransferTimeout)
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(object),
		Range:  aws.String(fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)),
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("opening artifact %q range: %w", key, err)
	}
	if !strings.HasPrefix(aws.ToString(out.ContentRange), fmt.Sprintf("bytes %d-", offset)) {
		_ = out.Body.Close()
		cancel()
		return nil, fmt.Errorf("opening artifact %q range: service returned range %q", key, aws.ToString(out.ContentRange))
	}
	return &cancelReader{ReadCloser: out.Body, cancel: cancel}, nil
}

type cancelReader struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (r *cancelReader) Close() error {
	err := r.ReadCloser.Close()
	r.cancel()
	return err
}

// Delete succeeds when the object is already absent, matching S3 semantics.
func (s *S3) Delete(key string) error {
	object, err := s.key(key)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), s3RequestTimeout)
	defer cancel()
	if _, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(object)}); err != nil {
		return fmt.Errorf("deleting artifact %q: %w", key, err)
	}
	return nil
}

// List returns keys relative to the prefix. Objects whose remaining key is not
// a valid artifact key are skipped, so callers never act on them.
func (s *S3) List() ([]Object, error) {
	prefix := s.listPrefix()
	var token *string
	var objects []Object
	for {
		ctx, cancel := context.WithTimeout(context.Background(), s3RequestTimeout)
		out, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(prefix), ContinuationToken: token})
		cancel()
		if err != nil {
			return nil, fmt.Errorf("listing artifacts: %w", err)
		}
		for _, v := range out.Contents {
			key, ok := strings.CutPrefix(aws.ToString(v.Key), prefix)
			if ok && ValidKey(key) {
				objects = append(objects, Object{Key: key, ModifiedAt: aws.ToTime(v.LastModified)})
			}
		}
		if !aws.ToBool(out.IsTruncated) {
			return objects, nil
		}
		token = out.NextContinuationToken
		if token == nil {
			return nil, errors.New("listing artifacts: missing continuation token")
		}
	}
}
