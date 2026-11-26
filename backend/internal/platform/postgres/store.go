package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"aegisvision/backend/internal/platform/postgres/sqlcgen"
	"aegisvision/backend/migrations"
)

// Store wraps a pgxpool.Pool and generated sqlc queries with transaction helpers.
type Store struct {
	pool    *pgxpool.Pool
	queries *sqlcgen.Queries
}

// New initializes a pgxpool.Pool and verifies connectivity to PostgreSQL.
func New(ctx context.Context, dsn string) (*Store, error) {
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse postgres dsn: %w", err)
	}
	if poolCfg.MaxConns == 0 {
		poolCfg.MaxConns = 20
	}
	if poolCfg.MaxConnLifetime == 0 {
		poolCfg.MaxConnLifetime = 30 * time.Minute
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	return &Store{
		pool:    pool,
		queries: sqlcgen.New(pool),
	}, nil
}

// Close releases all pooled database connections.
func (s *Store) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

// Pool exposes the underlying pgxpool.Pool for advanced/health operations.
func (s *Store) Pool() *pgxpool.Pool {
	return s.pool
}

// Queries returns the sqlc-generated query runner bound to the pool.
func (s *Store) Queries() *sqlcgen.Queries {
	return s.queries
}

// Ping verifies database reachability.
func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// WithTx executes fn inside a single database transaction, rolling back on error or panic.
func (s *Store) WithTx(ctx context.Context, fn func(q *sqlcgen.Queries) error) (retErr error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	defer func() {
		if r := recover(); r != nil {
			_ = tx.Rollback(ctx)
			panic(r)
		}
		if retErr != nil {
			_ = tx.Rollback(ctx)
		}
	}()

	if err := fn(s.queries.WithTx(tx)); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// RunMigrations applies all pending embedded goose SQL migrations against dsn.
func RunMigrations(ctx context.Context, dsn string) error {
	connCfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("parse migration dsn: %w", err)
	}

	sqlDB := stdlib.OpenDB(*connCfg)
	defer func() {
		_ = sqlDB.Close()
	}()

	if err := sqlDB.PingContext(ctx); err != nil {
		return fmt.Errorf("ping postgres for migration: %w", err)
	}

	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
	if err != nil {
		return fmt.Errorf("init goose provider: %w", err)
	}

	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply goose migrations: %w", err)
	}
	return nil
}
