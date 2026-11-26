package storage_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"
	"time"

	"aegisvision/backend/internal/config"
	"aegisvision/backend/internal/platform/storage"
)

func TestLocalStoragePutGetRangeAndDelete(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.NewLocal(dir)
	if err != nil {
		t.Fatalf("NewLocal() failed: %v", err)
	}

	ctx := context.Background()
	payload := []byte("0123456789abcdefghijklmnopqrstuvwxyz")
	key := "videos/2026/11/26/cam1/alarm1.mp4"

	if err := store.PutObject(ctx, key, bytes.NewReader(payload), int64(len(payload)), "video/mp4"); err != nil {
		t.Fatalf("PutObject() failed: %v", err)
	}

	// Full read
	rc, meta, err := store.GetObject(ctx, key, "")
	if err != nil {
		t.Fatalf("GetObject(full) failed: %v", err)
	}
	gotFull, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !bytes.Equal(gotFull, payload) {
		t.Fatalf("full content mismatch: got %q, want %q", gotFull, payload)
	}
	if meta.Partial || meta.TotalSize != int64(len(payload)) {
		t.Fatalf("unexpected full meta: %+v", meta)
	}

	// Partial Range read (bytes=5-14 -> 10 bytes)
	rc2, meta2, err := store.GetObject(ctx, key, "bytes=5-14")
	if err != nil {
		t.Fatalf("GetObject(range) failed: %v", err)
	}
	gotRange, _ := io.ReadAll(rc2)
	_ = rc2.Close()
	if string(gotRange) != "56789abcde" {
		t.Fatalf("range content = %q, want 56789abcde", gotRange)
	}
	if !meta2.Partial || meta2.ContentRange != "bytes 5-14/36" || meta2.ContentLength != 10 {
		t.Fatalf("unexpected range meta: %+v", meta2)
	}

	// Reject path traversal
	if err := store.PutObject(ctx, "../escape.txt", bytes.NewReader([]byte("bad")), 3, "text/plain"); err == nil {
		t.Fatal("expected path traversal key to be rejected, got nil")
	}

	// Delete
	if err := store.DeleteObject(ctx, key); err != nil {
		t.Fatalf("DeleteObject() failed: %v", err)
	}
	if _, _, err := store.GetObject(ctx, key, ""); err == nil {
		t.Fatal("expected GetObject after DeleteObject to fail, got nil")
	}
}

func TestS3StorageLiveMinIORange(t *testing.T) {
	endpoint := os.Getenv("AEGIS_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("AEGIS_TEST_S3_ENDPOINT not set; skipping live MinIO test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	store, err := storage.NewS3(ctx, config.S3Config{
		Endpoint:     endpoint,
		Bucket:       envOrDefault("AEGIS_TEST_S3_BUCKET", "aegis-evidence"),
		AccessKey:    envOrDefault("AEGIS_TEST_S3_ACCESS_KEY", "aegis"),
		SecretKey:    envOrDefault("AEGIS_TEST_S3_SECRET_KEY", "aegis_minio_2026"),
		Region:       "us-east-1",
		UsePathStyle: true,
	})
	if err != nil {
		t.Fatalf("NewS3() failed: %v", err)
	}

	key := "images/test/s3_range_check.bin"
	data := bytes.Repeat([]byte("ABCDEFGH"), 128) // 1024 bytes
	t.Cleanup(func() {
		_ = store.DeleteObject(context.Background(), key)
	})

	if err := store.PutObject(ctx, key, bytes.NewReader(data), int64(len(data)), "application/octet-stream"); err != nil {
		t.Fatalf("S3 PutObject() failed: %v", err)
	}

	rc, meta, err := store.GetObject(ctx, key, "bytes=0-15")
	if err != nil {
		t.Fatalf("S3 GetObject(range) failed: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()

	if string(got) != "ABCDEFGHABCDEFGH" {
		t.Fatalf("S3 range content = %q, want ABCDEFGHABCDEFGH", got)
	}
	if !meta.Partial || meta.ContentLength != 16 {
		t.Fatalf("unexpected S3 range meta: %+v", meta)
	}
}

func envOrDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
