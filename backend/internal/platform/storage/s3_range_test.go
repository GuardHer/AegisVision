package storage_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"

	"aegisvision/backend/internal/config"
	"aegisvision/backend/internal/platform/storage"
)

func TestS3InvalidRangeMapsToErrInvalidRange(t *testing.T) {
	endpoint := os.Getenv("AEGIS_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("AEGIS_TEST_S3_ENDPOINT not set")
	}
	bucket := os.Getenv("AEGIS_TEST_S3_BUCKET")
	if bucket == "" {
		bucket = "aegis-evidence"
	}

	ctx := context.Background()
	s3Store, err := storage.NewS3(ctx, config.S3Config{
		Endpoint:     endpoint,
		Bucket:       bucket,
		AccessKey:    os.Getenv("AEGIS_TEST_S3_ACCESS_KEY"),
		SecretKey:    os.Getenv("AEGIS_TEST_S3_SECRET_KEY"),
		Region:       "us-east-1",
		UsePathStyle: true,
	})
	if err != nil {
		t.Fatalf("NewS3: %v", err)
	}

	key := "test/range-416-" + uuid.Must(uuid.NewV7()).String() + ".bin"
	payload := []byte("0123456789")
	if err := s3Store.PutObject(ctx, key, bytes.NewReader(payload), int64(len(payload)), "application/octet-stream"); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	t.Cleanup(func() { _ = s3Store.DeleteObject(context.Background(), key) })

	rc, _, err := s3Store.GetObject(ctx, key, "bytes=999-1024")
	if rc != nil {
		_ = rc.Close()
	}
	if !errors.Is(err, storage.ErrInvalidRange) {
		t.Fatalf("expected storage.ErrInvalidRange for out-of-bounds range, got %v", err)
	}
}
