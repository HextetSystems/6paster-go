package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HextetSystems/6paster-go/internal/config"
)

func sqliteStore(t *testing.T) (*Store, func()) {
	t.Helper()
	db, dialect, err := Open(config.DatabaseConfig{Driver: "sqlite", SQLite: config.SQLiteConfig{Path: filepath.Join(t.TempDir(), "test.db")}})
	if err != nil {
		t.Fatal(err)
	}
	store := New(db, dialect)
	if err := store.Migrate(context.Background()); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return store, func() { _ = db.Close() }
}

func testPaste(id string, now time.Time) Paste {
	return Paste{ID: id, CreatedAt: now, ExpiresAt: now.Add(time.Hour), MIMEType: "text/plain; charset=utf-8", Content: []byte("hello")}
}

func TestSQLiteFreshSchema(t *testing.T) {
	store, closeDB := sqliteStore(t)
	defer closeDB()
	ctx := context.Background()
	if err := store.CheckSchema(ctx); err != nil {
		t.Fatalf("CheckSchema() = %v", err)
	}
	assertPasteIPSchemaRemoved(t, store.db, SQLite)
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("second migration was not idempotent: %v", err)
	}
	var versions int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&versions); err != nil || versions != latestSchemaVersion {
		t.Fatalf("migration versions = %d, %v", versions, err)
	}
}

func assertPasteIPSchemaRemoved(t *testing.T, db *sql.DB, dialect Dialect) {
	t.Helper()
	ctx := context.Background()
	var columns, indexes int
	if dialect == SQLite {
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('pastes') WHERE name='ip'`).Scan(&columns); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_index_list('pastes') WHERE name='idx_pastes_ip_date'`).Scan(&indexes); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='pastes' AND column_name='ip'`).Scan(&columns); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='pastes' AND index_name='idx_pastes_ip_date'`).Scan(&indexes); err != nil {
			t.Fatal(err)
		}
	}
	if columns != 0 || indexes != 0 {
		t.Fatalf("pastes IP schema remains: columns=%d indexes=%d", columns, indexes)
	}
}

func TestSQLitePasteLifecycle(t *testing.T) {
	store, closeDB := sqliteStore(t)
	defer closeDB()
	ctx := context.Background()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	p := testPaste("abcdefghijklmnopqrstuvwx", now)
	if err := store.CreatePaste(ctx, p, "hash", Limits{PerHour: 60, PerDay: 100}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetPaste(ctx, p.ID, now)
	if err != nil || string(got.Content) != "hello" {
		t.Fatalf("GetPaste() = %+v, %v", got, err)
	}
	if _, err := store.GetPaste(ctx, p.ID, p.ExpiresAt); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired GetPaste error = %v", err)
	}
	if err := store.Cleanup(ctx, p.ExpiresAt); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteRateLimitSurvivesPasteCleanup(t *testing.T) {
	store, closeDB := sqliteStore(t)
	defer closeDB()
	ctx := context.Background()
	now := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	p := testPaste("aaaaaaaaaaaaaaaaaaaaaaaa", now)
	p.ExpiresAt = now.Add(time.Hour)
	if err := store.CreatePaste(ctx, p, "same-ip", Limits{PerHour: 1, PerDay: 2}); err != nil {
		t.Fatal(err)
	}
	if err := store.Cleanup(ctx, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	p.ID = "bbbbbbbbbbbbbbbbbbbbbbbb"
	p.CreatedAt = now.Add(2 * time.Hour)
	p.ExpiresAt = p.CreatedAt.Add(time.Hour)
	if err := store.CreatePaste(ctx, p, "same-ip", Limits{PerHour: 1, PerDay: 2}); err != nil {
		t.Fatalf("second paste should pass hourly limit: %v", err)
	}
	p.ID = "cccccccccccccccccccccccc"
	p.CreatedAt = now.Add(3 * time.Hour)
	p.ExpiresAt = p.CreatedAt.Add(time.Hour)
	err := store.CreatePaste(ctx, p, "same-ip", Limits{PerHour: 1, PerDay: 2})
	if !errors.Is(err, ErrRateLimit) {
		t.Fatalf("expected daily rate limit, got %v", err)
	}
}

func TestSQLiteCollision(t *testing.T) {
	store, closeDB := sqliteStore(t)
	defer closeDB()
	now := time.Now().UTC().Truncate(time.Second)
	p := testPaste("aaaaaaaaaaaaaaaaaaaaaaaa", now)
	if err := store.CreatePaste(context.Background(), p, "ip-a", Limits{60, 100}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreatePaste(context.Background(), p, "ip-b", Limits{60, 100}); !errors.Is(err, ErrIDCollision) {
		t.Fatalf("expected collision, got %v", err)
	}
}

func TestSQLiteConcurrentRateLimit(t *testing.T) {
	store, closeDB := sqliteStore(t)
	defer closeDB()
	now := time.Now().UTC().Truncate(time.Second)
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := []byte("aaaaaaaaaaaaaaaaaaaaaaaa")
			id[0] = byte('A' + i)
			err := store.CreatePaste(context.Background(), testPaste(string(id), now), "shared-ip", Limits{PerHour: 5, PerDay: 10})
			if err == nil {
				accepted.Add(1)
			} else if !errors.Is(err, ErrRateLimit) {
				t.Errorf("submission %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if got := accepted.Load(); got != 5 {
		t.Fatalf("accepted %d submissions, want 5", got)
	}
}
