package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	"aegisvision/backend/internal/config"
)

var (
	// ErrNotFound indicates the requested object key does not exist.
	ErrNotFound = errors.New("storage: object not found")
	// ErrInvalidRange indicates a malformed or unsatisfiable HTTP Range header.
	ErrInvalidRange = errors.New("storage: invalid byte range")
	// ErrInvalidKey indicates an unsafe or empty object key.
	ErrInvalidKey = errors.New("storage: invalid object key")
)

// ObjectMeta describes response metadata returned by GetObject.
type ObjectMeta struct {
	ContentType   string
	ContentLength int64
	TotalSize     int64
	ContentRange  string
	Partial       bool
}

// ObjectStorage defines the unified evidence storage abstraction (S3/MinIO or local fallback).
type ObjectStorage interface {
	PutObject(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	GetObject(ctx context.Context, key string, rangeHeader string) (io.ReadCloser, ObjectMeta, error)
	DeleteObject(ctx context.Context, key string) error
}

// New constructs an ObjectStorage driver from unified platform configuration.
func New(ctx context.Context, cfg config.StorageConfig) (ObjectStorage, error) {
	switch cfg.Driver {
	case "s3":
		return NewS3(ctx, cfg.S3)
	case "local":
		return NewLocal(cfg.Local.RootDir)
	default:
		return nil, fmt.Errorf("unsupported storage driver %q", cfg.Driver)
	}
}

func sanitizeKey(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", ErrInvalidKey
	}
	normalized := strings.ReplaceAll(raw, "\\", "/")
	if strings.HasPrefix(normalized, "/") {
		return "", ErrInvalidKey
	}
	for _, seg := range strings.Split(normalized, "/") {
		if seg == ".." {
			return "", ErrInvalidKey
		}
	}
	cleaned := path.Clean(normalized)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", ErrInvalidKey
	}
	return cleaned, nil
}

// ParseByteRange parses a single HTTP byte range header (e.g. "bytes=0-1023", "bytes=500-", "bytes=-500")
// against totalSize and returns inclusive [start, end] offsets.
func ParseByteRange(rangeHeader string, totalSize int64) (int64, int64, error) {
	if totalSize <= 0 {
		return 0, 0, ErrInvalidRange
	}
	if !strings.HasPrefix(rangeHeader, "bytes=") {
		return 0, 0, ErrInvalidRange
	}
	spec := strings.TrimSpace(strings.TrimPrefix(rangeHeader, "bytes="))
	if strings.Contains(spec, ",") {
		return 0, 0, ErrInvalidRange
	}
	parts := strings.SplitN(spec, "-", 2)
	if len(parts) != 2 {
		return 0, 0, ErrInvalidRange
	}

	startStr, endStr := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	var start, end int64
	var err error

	switch {
	case startStr == "" && endStr == "":
		return 0, 0, ErrInvalidRange
	case startStr == "":
		// Suffix range: bytes=-N
		suffixLen, parseErr := strconv.ParseInt(endStr, 10, 64)
		if parseErr != nil || suffixLen <= 0 {
			return 0, 0, ErrInvalidRange
		}
		if suffixLen > totalSize {
			suffixLen = totalSize
		}
		start = totalSize - suffixLen
		end = totalSize - 1
	case endStr == "":
		// Open-ended range: bytes=N-
		start, err = strconv.ParseInt(startStr, 10, 64)
		if err != nil || start < 0 || start >= totalSize {
			return 0, 0, ErrInvalidRange
		}
		end = totalSize - 1
	default:
		start, err = strconv.ParseInt(startStr, 10, 64)
		if err != nil || start < 0 || start >= totalSize {
			return 0, 0, ErrInvalidRange
		}
		end, err = strconv.ParseInt(endStr, 10, 64)
		if err != nil || end < start {
			return 0, 0, ErrInvalidRange
		}
		if end >= totalSize {
			end = totalSize - 1
		}
	}

	return start, end, nil
}
