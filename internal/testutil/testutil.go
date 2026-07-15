// Package testutil provides test-only helpers. Not imported by any
// production code.
package testutil

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// repoRoot is this file's own directory walked up to the module root
// (internal/testutil -> repo root), computed once at compile time via the
// caller's file path. `go test` runs each package with that package's own
// directory as its working directory, not the repo root, so a
// working-directory-relative ".env.test" would only ever be found by tests
// in the repo root itself.
var repoRoot = func() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}()

var (
	poolOnce sync.Once
	pool     *pgxpool.Pool
	poolErr  error
)

// OpenTestTx returns a transaction against TEST_DATABASE_URL, rolled back
// automatically via t.Cleanup so tests never need manual teardown and can
// run in parallel safely. Skips the test if TEST_DATABASE_URL isn't set (by
// env var or a local .env.test), so `go test ./...` still runs everywhere
// with zero infra by default.
func OpenTestTx(t *testing.T) pgx.Tx {
	t.Helper()
	loadDotenv(filepath.Join(repoRoot, ".env.test"))

	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping DB-backed test")
	}

	poolOnce.Do(func() {
		cfg, err := pgxpool.ParseConfig(url)
		if err != nil {
			poolErr = err
			return
		}
		// Same fix as cmd/admin/main.go: Supabase's transaction-mode pooler
		// hands out different backend sessions between statements, so pgx's
		// default cached/named prepared statements collide across them.
		cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
		pool, poolErr = pgxpool.NewWithConfig(context.Background(), cfg)
	})
	if poolErr != nil {
		t.Fatalf("opening test database pool: %v", poolErr)
	}

	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("beginning test transaction: %v", err)
	}
	t.Cleanup(func() {
		_ = tx.Rollback(context.Background())
	})
	return tx
}

// loadDotenv sets vars from a KEY=VALUE file without overriding the real
// environment. Mirrors internal/config's loader (unexported there, so
// duplicated here rather than exporting it solely for test use).
func loadDotenv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		if key == "" || os.Getenv(key) != "" {
			continue
		}
		os.Setenv(key, val)
	}
}
