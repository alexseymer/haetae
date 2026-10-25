//go:build integration

package store_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/store/storetest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMuteDedupeBeforeUniqueIndexMigration(t *testing.T) {
	// Apply migrations 0001–0007, seed duplicate mutes (incl. NULL host_id),
	// then apply 0008 and assert the unique index + single row per key.
	admin := mustAdminURL(t)
	ctx := context.Background()
	adminPool, err := pgxpool.New(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	defer adminPool.Close()

	dbName := "haetae_mute_mig_" + strings.ReplaceAll(uuid.New().String(), "-", "")[:16]
	if _, err := adminPool.Exec(ctx, `CREATE DATABASE `+quoteIdentMig(dbName)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = adminPool.Exec(context.Background(), `DROP DATABASE IF EXISTS `+quoteIdentMig(dbName)+` WITH (FORCE)`)
	})

	dbURL := withDBMig(t, admin, dbName)
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	migDir := migrationsDir(t)
	applyUpTo(t, pool, migDir, 7)

	var orgID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO orgs (name) VALUES ('mute-mig') RETURNING id`).Scan(&orgID); err != nil {
		t.Fatal(err)
	}
	until1 := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	until2 := until1.Add(48 * time.Hour)
	until3 := until1.Add(12 * time.Hour)

	for _, until := range []time.Time{until1, until2, until3} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO mutes (org_id, host_id, rule, until) VALUES ($1, NULL, 'oom_kill', $2)`,
			orgID, until); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM mutes WHERE org_id=$1 AND host_id IS NULL AND rule='oom_kill'`, orgID).Scan(&n); err != nil || n != 3 {
		t.Fatalf("seed duplicates: n=%d err=%v", n, err)
	}

	applyOne(t, pool, migDir, 8)

	if err := pool.QueryRow(ctx, `SELECT count(*) FROM mutes WHERE org_id=$1 AND host_id IS NULL AND rule='oom_kill'`, orgID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("after 0008 want 1 mute, got %d", n)
	}
	var until time.Time
	if err := pool.QueryRow(ctx, `SELECT until FROM mutes WHERE org_id=$1`, orgID).Scan(&until); err != nil {
		t.Fatal(err)
	}
	if !until.Equal(until2) {
		t.Fatalf("until=%v want GREATEST=%v", until, until2)
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO mutes (org_id, host_id, rule, until) VALUES ($1, NULL, 'oom_kill', now() + interval '1 day')`, orgID)
	if err == nil {
		t.Fatal("expected unique violation for duplicate NULL-host mute")
	}
}

func mustAdminURL(t *testing.T) string {
	t.Helper()
	if env := os.Getenv("HAETAE_TEST_DATABASE_URL"); env != "" {
		return env
	}
	s, url := storetest.NewWithURL(t)
	s.Close()
	idx := strings.LastIndex(url, "/")
	if idx < 0 {
		t.Fatal(url)
	}
	return url[:idx+1] + "postgres"
}

func withDBMig(t *testing.T, admin, dbName string) string {
	t.Helper()
	idx := strings.LastIndex(admin, "/")
	if idx < 0 {
		t.Fatal(admin)
	}
	return admin[:idx+1] + dbName
}

func quoteIdentMig(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func migrationsDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return filepath.Join(filepath.Dir(file), "migrations")
}

func applyUpTo(t *testing.T, pool *pgxpool.Pool, dir string, maxVer int) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version int PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	type mig struct {
		ver  int
		name string
	}
	var migs []mig
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		parts := strings.SplitN(e.Name(), "_", 2)
		ver, err := strconv.Atoi(parts[0])
		if err != nil {
			t.Fatal(err)
		}
		if ver <= maxVer {
			migs = append(migs, mig{ver, e.Name()})
		}
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].ver < migs[j].ver })
	for _, m := range migs {
		applyNamed(t, pool, dir, m.ver, m.name)
	}
}

func applyOne(t *testing.T, pool *pgxpool.Pool, dir string, ver int) {
	t.Helper()
	prefix := fmt.Sprintf("%04d_", ver)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			applyNamed(t, pool, dir, ver, e.Name())
			return
		}
	}
	t.Fatalf("migration %d not found", ver)
}

func applyNamed(t *testing.T, pool *pgxpool.Pool, dir string, ver int, name string) {
	t.Helper()
	ctx := context.Background()
	body, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var applied bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, ver).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied {
		_ = tx.Commit(ctx)
		return
	}
	if _, err := tx.Exec(ctx, string(body)); err != nil {
		t.Fatalf("apply %s: %v", name, err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, ver); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}
