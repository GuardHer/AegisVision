package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"aegisvision/backend/internal/config"
)

// S3Storage implements ObjectStorage against MinIO or any S3-compatible service.
type S3Storage struct {
	client *s3.Client
	bucket string
}

// NewS3 initializes an S3/MinIO storage client using path-style addressing when configured.
func NewS3(ctx context.Context, cfg config.S3Config) (*S3Storage, error) {
	if strings.TrimSpace(cfg.Endpoint) == "" || strings.TrimSpace(cfg.Bucket) == "" {
		return nil, errors.New("storage: s3 endpoint and bucket are required")
	}
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}

	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(region),
	}
	if cfg.AccessKey != "" || cfg.SecretKey != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load aws s3 config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(cfg.Endpoint)
		o.UsePathStyle = cfg.UsePathStyle
	})

	return &S3Storage{
		client: client,
		bucket: cfg.Bucket,
	}, nil
}

// PutObject uploads an object to the configured S3 bucket.
func (s *S3Storage) PutObject(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	cleanKey, err := sanitizeKey(key)
	if err != nil {
		return err
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	input := &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(cleanKey),
		Body:        r,
		ContentType: aws.String(contentType),
	}
	if size >= 0 {
		input.ContentLength = aws.Int64(size)
	}

	if _, err := s.client.PutObject(ctx, input); err != nil {
		return fmt.Errorf("s3 put object %q: %w", cleanKey, err)
	}
	return nil
}

// GetObject fetches an object from S3, forwarding rangeHeader when provided.
func (s *S3Storage) GetObject(ctx context.Context, key string, rangeHeader string) (io.ReadCloser, ObjectMeta, error) {
	cleanKey, err := sanitizeKey(key)
	if err != nil {
		return nil, ObjectMeta{}, err
	}

	input := &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(cleanKey),
	}
	if strings.TrimSpace(rangeHeader) != "" {
		input.Range = aws.String(rangeHeader)
	}

	out, err := s.client.GetObject(ctx, input)
	if err != nil {
		if isS3NotFound(err) {
			return nil, ObjectMeta{}, ErrNotFound
		}
		if isS3InvalidRange(err) {
			return nil, ObjectMeta{}, ErrInvalidRange
		}
		return nil, ObjectMeta{}, fmt.Errorf("s3 get object %q: %w", cleanKey, err)
	}

	meta := ObjectMeta{
		ContentType:   aws.ToString(out.ContentType),
		ContentLength: aws.ToInt64(out.ContentLength),
		ContentRange:  aws.ToString(out.ContentRange),
		Partial:       aws.ToString(out.ContentRange) != "",
	}
	if meta.ContentType == "" {
		meta.ContentType = "application/octet-stream"
	}
	if meta.Partial {
		meta.TotalSize = parseTotalFromContentRange(meta.ContentRange)
	} else {
		meta.TotalSize = meta.ContentLength
	}

	return out.Body, meta, nil
}

// DeleteObject deletes an object from S3.
func (s *S3Storage) DeleteObject(ctx context.Context, key string) error {
	cleanKey, err := sanitizeKey(key)
	if err != nil {
		return err
	}
	_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(cleanKey),
	})
	if err != nil {
		return fmt.Errorf("s3 delete object %q: %w", cleanKey, err)
	}
	return nil
}

func isS3NotFound(err error) bool {
	var noSuchKey *types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		code := apiErr.ErrorCode()
		return code == "NoSuchKey" || code == "NotFound" || code == "404"
	}
	return false
}

func isS3InvalidRange(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		code := apiErr.ErrorCode()
		if code == "InvalidRange" || code == "416" {
			return true
		}
	}
	var respErr *smithyhttp.ResponseError
	if errors.As(err, &respErr) && respErr.HTTPStatusCode() == 416 {
		return true
	}
	return false
}

func parseTotalFromContentRange(cr string) int64 {
	idx := strings.LastIndex(cr, "/")
	if idx < 0 || idx+1 >= len(cr) {
		return 0
	}
	total, err := strconv.ParseInt(strings.TrimSpace(cr[idx+1:]), 10, 64)
	if err != nil {
		return 0
	}
	return total
}
