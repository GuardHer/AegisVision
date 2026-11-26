package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
)

// LocalStorage is a bare-metal filesystem implementation of ObjectStorage.
type LocalStorage struct {
	rootDir string
}

// NewLocal creates a LocalStorage rooted at rootDir.
func NewLocal(rootDir string) (*LocalStorage, error) {
	if strings.TrimSpace(rootDir) == "" {
		return nil, errors.New("storage: local rootDir is required")
	}
	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, fmt.Errorf("resolve local storage root: %w", err)
	}
	if err := os.MkdirAll(absRoot, 0o750); err != nil {
		return nil, fmt.Errorf("create local storage root: %w", err)
	}
	return &LocalStorage{rootDir: absRoot}, nil
}

func (l *LocalStorage) resolvePath(key string) (string, error) {
	cleanKey, err := sanitizeKey(key)
	if err != nil {
		return "", err
	}
	fullPath := filepath.Join(l.rootDir, filepath.FromSlash(cleanKey))
	rel, err := filepath.Rel(l.rootDir, fullPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", ErrInvalidKey
	}
	return fullPath, nil
}

// PutObject writes r to a file under rootDir atomically via temporary file + rename.
func (l *LocalStorage) PutObject(ctx context.Context, key string, r io.Reader, _ int64, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fullPath, err := l.resolvePath(key)
	if err != nil {
		return err
	}
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("mkdir %q: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp object file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write object %q: %w", key, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp object %q: %w", key, err)
	}
	if err := os.Rename(tmpPath, fullPath); err != nil {
		return fmt.Errorf("commit object %q: %w", key, err)
	}
	cleanupTmp = false
	return nil
}

type sectionReadCloser struct {
	io.Reader
	io.Closer
}

// GetObject opens the stored file and optionally serves a single HTTP byte range.
func (l *LocalStorage) GetObject(ctx context.Context, key string, rangeHeader string) (io.ReadCloser, ObjectMeta, error) {
	if err := ctx.Err(); err != nil {
		return nil, ObjectMeta{}, err
	}
	fullPath, err := l.resolvePath(key)
	if err != nil {
		return nil, ObjectMeta{}, err
	}
	f, err := os.Open(fullPath) // #nosec G304 -- path is validated against rootDir in resolvePath
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ObjectMeta{}, ErrNotFound
		}
		return nil, ObjectMeta{}, fmt.Errorf("open object %q: %w", key, err)
	}

	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, ObjectMeta{}, fmt.Errorf("stat object %q: %w", key, err)
	}
	totalSize := info.Size()
	contentType := mime.TypeByExtension(filepath.Ext(fullPath))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	if strings.TrimSpace(rangeHeader) == "" {
		return f, ObjectMeta{
			ContentType:   contentType,
			ContentLength: totalSize,
			TotalSize:     totalSize,
			Partial:       false,
		}, nil
	}

	start, end, err := ParseByteRange(rangeHeader, totalSize)
	if err != nil {
		_ = f.Close()
		return nil, ObjectMeta{}, err
	}
	length := end - start + 1
	section := io.NewSectionReader(f, start, length)
	return &sectionReadCloser{Reader: section, Closer: f}, ObjectMeta{
		ContentType:   contentType,
		ContentLength: length,
		TotalSize:     totalSize,
		ContentRange:  fmt.Sprintf("bytes %d-%d/%d", start, end, totalSize),
		Partial:       true,
	}, nil
}

// DeleteObject removes the stored object file.
func (l *LocalStorage) DeleteObject(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fullPath, err := l.resolvePath(key)
	if err != nil {
		return err
	}
	if err := os.Remove(fullPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete object %q: %w", key, err)
	}
	return nil
}
