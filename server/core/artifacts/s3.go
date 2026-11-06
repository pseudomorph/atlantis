// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package artifacts

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3Client is the object API shared by plan and artifact storage.
type S3Client interface {
	HeadBucket(ctx context.Context, params *s3.HeadBucketInput, optFns ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	ListObjectsV2(ctx context.Context, params *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

// S3Config holds configuration for connecting to S3.
type S3Config struct {
	Bucket         string
	Region         string
	Prefix         string
	Endpoint       string
	ForcePathStyle bool
	Profile        string
}

type S3 struct {
	client         S3Client
	bucket, prefix string
}

func NewS3Client(c S3Config) (S3Client, error) {
	if c.Bucket == "" {
		return nil, fmt.Errorf("S3 bucket is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(c.Region)}
	if c.Profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(c.Profile))
	}
	config, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("loading artifact AWS configuration")
	}
	client := s3.NewFromConfig(config, func(o *s3.Options) {
		o.UsePathStyle = c.ForcePathStyle
		if c.Endpoint != "" {
			o.BaseEndpoint = aws.String(c.Endpoint)
		}
	})
	if _, err = client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(c.Bucket)}); err != nil {
		return nil, fmt.Errorf("opening artifact bucket %q", c.Bucket)
	}
	return client, nil
}
func NewS3WithClient(client S3Client, bucket, prefix string) *S3 {
	return &S3{client: client, bucket: bucket, prefix: strings.TrimSuffix(prefix, "/")}
}
func (s *S3) key(key string) (string, error) {
	if !ValidKey(key) || (s.prefix != "" && !ValidKey(s.prefix)) {
		return "", fmt.Errorf("invalid artifact key")
	}
	if s.prefix != "" {
		return s.prefix + "/" + key, nil
	}
	return key, nil
}
func (s *S3) Close() error { return nil }
func (s *S3) Put(key string, r io.Reader) (Metadata, error) {
	key, err := s.key(key)
	if err != nil {
		return Metadata{}, err
	}
	file, err := os.CreateTemp("", "atlantis-artifact-*")
	if err != nil {
		return Metadata{}, err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(file, hash), r)
	if err != nil {
		return Metadata{}, err
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return Metadata{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), Body: file, ContentLength: aws.Int64(size)})
	return Metadata{size, fmt.Sprintf("%x", hash.Sum(nil))}, err
}
func (s *S3) Open(key string) (io.ReadCloser, error) {
	key, err := s.key(key)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		cancel()
		return nil, err
	}
	return &cancelReader{ReadCloser: out.Body, cancel: cancel}, nil
}

type cancelReader struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (r *cancelReader) Close() error { err := r.ReadCloser.Close(); r.cancel(); return err }
func (s *S3) Delete(key string) error {
	key, err := s.key(key)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	return err
}
func (s *S3) List() ([]Object, error) {
	prefix := ""
	if s.prefix != "" {
		prefix = s.prefix + "/"
	}
	var token *string
	var objects []Object
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		out, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(prefix), ContinuationToken: token})
		cancel()
		if err != nil {
			return nil, err
		}
		for _, v := range out.Contents {
			key := strings.TrimPrefix(aws.ToString(v.Key), prefix)
			if ValidKey(key) {
				objects = append(objects, Object{key, aws.ToTime(v.LastModified)})
			}
		}
		if !aws.ToBool(out.IsTruncated) {
			return objects, nil
		}
		token = out.NextContinuationToken
		if token == nil {
			return nil, fmt.Errorf("missing S3 listing continuation token")
		}
	}
}
