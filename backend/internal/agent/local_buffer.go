package agent

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite" // Pure Go SQLite driver (zero CGO)

	agentv1 "aegisvision/backend/gen/go/agent/v1"
)

// LocalBuffer persists offline abnormal events and JPEG snapshots in a pure-Go SQLite WAL database.
// High-frequency normal detection frames must NEVER be stored here.
type LocalBuffer struct {
	db *sql.DB
}

// NewLocalBuffer opens or creates the SQLite WAL buffer database at dbPath.
func NewLocalBuffer(dbPath string) (*LocalBuffer, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o750); err != nil {
		return nil, fmt.Errorf("create buffer parent dir: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite buffer %q: %w", dbPath, err)
	}
	// Single writer pool avoids SQLITE_BUSY under concurrent flushes.
	db.SetMaxOpenConns(1)

	pragmas := []string{
		`PRAGMA journal_mode=WAL;`,
		`PRAGMA synchronous=NORMAL;`,
		`PRAGMA busy_timeout=5000;`,
		`CREATE TABLE IF NOT EXISTS buffered_events (
			seq INTEGER PRIMARY KEY AUTOINCREMENT,
			event_id TEXT NOT NULL UNIQUE,
			payload BLOB NOT NULL,
			created_at_ms INTEGER NOT NULL
		);`,
	}
	for _, stmt := range pragmas {
		if _, err := db.Exec(stmt); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("init sqlite buffer pragma/schema: %w", err)
		}
	}

	return &LocalBuffer{db: db}, nil
}

// Close flushes WAL checkpoints and closes the SQLite handle.
func (b *LocalBuffer) Close() error {
	if b == nil || b.db == nil {
		return nil
	}
	_, _ = b.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE);`)
	return b.db.Close()
}

// Enqueue persists a BufferedEvent idempotently by event_id.
func (b *LocalBuffer) Enqueue(ctx context.Context, evt *agentv1.BufferedEvent) error {
	if evt == nil || evt.GetEventId() == "" {
		return fmt.Errorf("buffered event requires non-empty event_id")
	}
	raw, err := proto.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal buffered event: %w", err)
	}
	_, err = b.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO buffered_events (event_id, payload, created_at_ms) VALUES (?, ?, ?)`,
		evt.GetEventId(), raw, evt.GetCachedAtMs(),
	)
	if err != nil {
		return fmt.Errorf("insert buffered event: %w", err)
	}
	return nil
}

// PeekBatch returns up to limit oldest buffered events in FIFO order without removing them.
func (b *LocalBuffer) PeekBatch(ctx context.Context, limit int) ([]*agentv1.BufferedEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := b.db.QueryContext(ctx,
		`SELECT payload FROM buffered_events ORDER BY seq ASC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query buffered events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []*agentv1.BufferedEvent
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var evt agentv1.BufferedEvent
		if err := proto.Unmarshal(raw, &evt); err != nil {
			return nil, fmt.Errorf("unmarshal buffered event: %w", err)
		}
		out = append(out, &evt)
	}
	return out, rows.Err()
}

// Delete removes an event only after the platform confirms EventAck.
func (b *LocalBuffer) Delete(ctx context.Context, eventID string) error {
	_, err := b.db.ExecContext(ctx, `DELETE FROM buffered_events WHERE event_id = ?`, eventID)
	return err
}

// Count returns the number of unacknowledged abnormal events currently buffered on disk.
func (b *LocalBuffer) Count(ctx context.Context) (int, error) {
	var n int
	if err := b.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM buffered_events`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}
