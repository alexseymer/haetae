// Package storetest provides helpers for integration tests against a real PostgreSQL database.
package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

const postgresPassword = "haetae"

var (
	dockerOnce sync.Once
	adminURL   string
	setupErr   error

	urlsMu sync.Mutex
	urls   = map[string]string{}
)

// New returns a Store on a fresh, empty database that is dropped at test cleanup.
// If HAETAE_TEST_DATABASE_URL is set it is used as the admin connection; otherwise one shared
// Postgres container per test process is started with
//
//	docker run -d --rm -e POSTGRES_PASSWORD=haetae -p 127.0.0.1::5432 postgres:17-alpine
//
// (port found with `docker port <id> 5432/tcp`, wait up to 60 s until a connection succeeds),
// guarded by sync.Once. The container is left running when the process exits (--rm removes it
// once stopped). Each call creates database "haetae_test_<16 random hex>" and returns
// store.Open on it. If neither the env var nor a working docker CLI is available, t.Skip with a
// clear message.
//
// The connection URL for the created database is stored under t.Name() and can be retrieved with URL.
func New(t testing.TB) *store.Store {
	t.Helper()
	s, _ := NewWithURL(t)
	return s
}

// NewWithURL is like New but also returns the database URL. Prefer New + URL when only one is needed.
func NewWithURL(t testing.TB) (*store.Store, string) {
	t.Helper()

	admin, err := resolveAdminURL(t)
	if err != nil {
		t.Skip(err.Error())
	}

	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("storetest: rand: %v", err)
	}
	dbName := "haetae_test_" + hex.EncodeToString(b[:])

	ctx := context.Background()
	adminPool, err := pgxpool.New(ctx, admin)
	if err != nil {
		t.Fatalf("storetest: admin connect: %v", err)
	}
	defer adminPool.Close()

	if _, err := adminPool.Exec(ctx, `CREATE DATABASE `+quoteIdent(dbName)); err != nil {
		t.Fatalf("storetest: create database: %v", err)
	}

	dbURL, err := withDBName(admin, dbName)
	if err != nil {
		t.Fatalf("storetest: database url: %v", err)
	}

	s, err := store.Open(ctx, dbURL)
	if err != nil {
		_, _ = adminPool.Exec(ctx, `DROP DATABASE IF EXISTS `+quoteIdent(dbName)+` WITH (FORCE)`)
		t.Fatalf("storetest: open: %v", err)
	}

	urlsMu.Lock()
	urls[t.Name()] = dbURL
	urlsMu.Unlock()

	t.Cleanup(func() {
		s.Close()
		dropCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		pool, err := pgxpool.New(dropCtx, admin)
		if err != nil {
			t.Logf("storetest: cleanup connect: %v", err)
			return
		}
		defer pool.Close()
		if _, err := pool.Exec(dropCtx, `DROP DATABASE IF EXISTS `+quoteIdent(dbName)+` WITH (FORCE)`); err != nil {
			t.Logf("storetest: drop database: %v", err)
		}
	})

	return s, dbURL
}

// URL returns the connection URL of the database created by the most recent New/NewWithURL
// call in this test (keyed by t.Name()).
func URL(t testing.TB) string {
	t.Helper()
	urlsMu.Lock()
	defer urlsMu.Unlock()
	u, ok := urls[t.Name()]
	if !ok {
		t.Fatal("storetest.URL called without storetest.New")
	}
	return u
}

func resolveAdminURL(t testing.TB) (string, error) {
	t.Helper()
	if u := os.Getenv("HAETAE_TEST_DATABASE_URL"); u != "" {
		return u, nil
	}

	dockerOnce.Do(func() {
		adminURL, setupErr = startPostgresContainer()
	})
	if setupErr != nil {
		return "", setupErr
	}
	return adminURL, nil
}

func startPostgresContainer() (string, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return "", fmt.Errorf("storetest: HAETAE_TEST_DATABASE_URL unset and docker not available: %w", err)
	}

	cmd := exec.Command("docker", "run", "-d", "--rm",
		"-e", "POSTGRES_PASSWORD="+postgresPassword,
		"-p", "127.0.0.1::5432",
		"postgres:17-alpine",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("storetest: docker run failed (%w): %s", err, strings.TrimSpace(string(out)))
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return "", fmt.Errorf("storetest: docker run returned empty container id")
	}

	portOut, err := exec.Command("docker", "port", id, "5432/tcp").CombinedOutput()
	if err != nil {
		_ = exec.Command("docker", "stop", id).Run()
		return "", fmt.Errorf("storetest: docker port: %w (%s)", err, strings.TrimSpace(string(portOut)))
	}
	hostPort := strings.TrimSpace(string(portOut))
	// e.g. "127.0.0.1:32768"
	_, port, ok := strings.Cut(hostPort, ":")
	if !ok || port == "" {
		_ = exec.Command("docker", "stop", id).Run()
		return "", fmt.Errorf("storetest: unexpected docker port output %q", hostPort)
	}

	u := fmt.Sprintf("postgres://postgres:%s@127.0.0.1:%s/postgres?sslmode=disable", postgresPassword, port)

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		pool, err := pgxpool.New(ctx, u)
		if err == nil {
			pingErr := pool.Ping(ctx)
			pool.Close()
			cancel()
			if pingErr == nil {
				return u, nil
			}
		} else {
			cancel()
		}
		time.Sleep(200 * time.Millisecond)
	}
	_ = exec.Command("docker", "stop", id).Run()
	return "", fmt.Errorf("storetest: postgres container %s not ready within 60s", id)
}

func withDBName(base, dbName string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	u.Path = "/" + dbName
	return u.String(), nil
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
