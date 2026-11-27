package postgres_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"aegisvision/backend/internal/platform/postgres"
)

func TestEnterpriseMigration00002AppliesCleanly(t *testing.T) {
	dsn := os.Getenv("AEGIS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("AEGIS_TEST_POSTGRES_DSN not set")
	}

	ctx := context.Background()
	if err := postgres.RunMigrations(ctx, dsn); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	store, err := postgres.New(ctx, dsn)
	if err != nil {
		t.Fatalf("postgres.New: %v", err)
	}
	defer store.Close()

	for _, tbl := range []string{
		"zones",
		"user_zones",
		"arming_schedules",
		"sop_cycles",
		"monitor_presets",
		"audit_logs",
		"system_settings",
	} {
		var reg pgtype.Text
		if err := store.Pool().QueryRow(ctx, `SELECT to_regclass($1)::text`, tbl).Scan(&reg); err != nil || !reg.Valid {
			t.Fatalf("expected table %q to exist after migration 00002, err=%v", tbl, err)
		}
	}
}
