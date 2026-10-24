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
// If HAETAE_TEST_DATABASE_URL is set it is used as the admin connection; otherwise the shared
// container "haetae-test-postgres" (postgres:17-alpine) is reused, or started or created if
// needed, once per process. It stays running between test runs; remove it with
// `docker rm -f haetae-test-postgres`. Each call creates database "haetae_test_<16 random hex>"
// and returns store.Open on it. If neither the env var nor a working docker CLI is available,
// t.Skip with a clear message.
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

// containerName is shared by every test process, so repeated test runs reuse
// one PostgreSQL container instead of leaving a new one behind each time.
const containerName = "haetae-test-postgres"

// startPostgresContainer returns the admin URL of the shared test container,
// starting or creating it if needed. Parallel test processes may race to
// create it; the loser retries and finds the winner's container.
func startPostgresContainer() (string, error) {
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		running, exists := containerState()
		switch {
		case running:
		case exists:
			if out, err := exec.Command("docker", "start", containerName).CombinedOutput(); err != nil {
				lastErr = fmt.Errorf("storetest: docker start: %w (%s)", err, strings.TrimSpace(string(out)))
				continue
			}
		default:
			out, err := exec.Command("docker", "run", "-d", "--name", containerName,
				"--label", "haetae.test=postgres",
				"-e", "POSTGRES_PASSWORD="+postgresPassword,
				"-p", "127.0.0.1::5432",
				"postgres:17-alpine",
			).CombinedOutput()
			if err != nil {
				lastErr = fmt.Errorf("storetest: docker run: %w (%s)", err, strings.TrimSpace(string(out)))
				time.Sleep(time.Second)
				continue
			}
		}

		u, err := containerURL()
		if err != nil {
			lastErr = err
			time.Sleep(time.Second)
			continue
		}
		if err := waitReady(u, 60*time.Second); err != nil {
			return "", err
		}
		return u, nil
	}
	return "", lastErr
}

func containerState() (running, exists bool) {
	out, err := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", containerName).Output()
	if err != nil {
		return false, false
	}
	return strings.TrimSpace(string(out)) == "true", true
}

func containerURL() (string, error) {
	out, err := exec.Command("docker", "port", containerName, "5432/tcp").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("storetest: docker port: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	// e.g. "127.0.0.1:32768", possibly followed by an IPv6 line.
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	_, port, ok := strings.Cut(first, ":")
	if !ok || port == "" {
		return "", fmt.Errorf("storetest: unexpected docker port output %q", first)
	}
	return fmt.Sprintf("postgres://postgres:%s@127.0.0.1:%s/postgres?sslmode=disable", postgresPassword, port), nil
}

func waitReady(u string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		pool, err := pgxpool.New(ctx, u)
		if err == nil {
			err = pool.Ping(ctx)
			pool.Close()
		}
		cancel()
		if err == nil {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("storetest: postgres at %s not ready after %s", u, timeout)
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
