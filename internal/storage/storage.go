package storage

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/HextetSystems/6paster-go/internal/config"
	gomysql "github.com/go-sql-driver/mysql"
	_ "modernc.org/sqlite"
)

type Dialect string

const (
	SQLite Dialect = "sqlite"
	MySQL  Dialect = "mysql"

	latestSchemaVersion = 1
)

var migrationNames = []string{
	"001_initial.sql",
}

var (
	ErrNotFound    = errors.New("paste not found")
	ErrRateLimit   = errors.New("rate limit exceeded")
	ErrIDCollision = errors.New("paste identifier collision")
)

//go:embed migrations/*/*.sql
var migrationFiles embed.FS

type Store struct {
	db      *sql.DB
	dialect Dialect
}

type Paste struct {
	ID        string
	CreatedAt time.Time
	Content   []byte
	MIMEType  string
	ExpiresAt time.Time
}

type Limits struct {
	PerHour int
	PerDay  int
}

type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string { return ErrRateLimit.Error() }
func (e *RateLimitError) Unwrap() error { return ErrRateLimit }

func New(db *sql.DB, dialect Dialect) *Store { return &Store{db: db, dialect: dialect} }

func Open(cfg config.DatabaseConfig) (*sql.DB, Dialect, error) {
	var driver, dsn string
	var dialect Dialect
	switch cfg.Driver {
	case "sqlite":
		driver, dialect = "sqlite", SQLite
		dsn = sqliteDSN(cfg.SQLite.Path)
	case "mysql":
		driver, dialect = "mysql", MySQL
		tls := map[string]string{"disabled": "false", "preferred": "preferred", "required": "true", "skip-verify": "skip-verify"}[cfg.MySQL.TLSMode]
		mc := gomysql.NewConfig()
		mc.User = cfg.MySQL.User
		mc.Passwd = cfg.MySQL.Password
		mc.Net = "tcp"
		mc.Addr = net.JoinHostPort(cfg.MySQL.Host, strconv.Itoa(cfg.MySQL.Port))
		mc.DBName = cfg.MySQL.Database
		mc.ParseTime = true
		mc.Loc = time.UTC
		mc.TLSConfig = tls
		mc.Params = map[string]string{"time_zone": "'+00:00'", "charset": "utf8mb4"}
		dsn = mc.FormatDSN()
	default:
		return nil, "", fmt.Errorf("unsupported database driver %q", cfg.Driver)
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, "", fmt.Errorf("open database: %w", err)
	}
	if dialect == SQLite {
		db.SetMaxOpenConns(1)
	} else {
		db.SetMaxOpenConns(20)
		db.SetMaxIdleConns(5)
		db.SetConnMaxLifetime(5 * time.Minute)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, "", fmt.Errorf("connect to database: %w", err)
	}
	return db, dialect, nil
}

func sqliteDSN(filename string) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	return "file:" + filename + "?" + q.Encode()
}

func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER NOT NULL PRIMARY KEY, applied_at DATETIME NOT NULL)`); err != nil {
		return err
	}
	for index, name := range migrationNames {
		version := index + 1
		applied, err := s.migrationApplied(ctx, version)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		contents, err := migrationFiles.ReadFile("migrations/" + string(s.dialect) + "/" + name)
		if err != nil {
			return err
		}
		if err := s.applyMigration(ctx, version, string(contents)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) migrationApplied(ctx context.Context, version int) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&count); err != nil {
		return false, err
	}
	return count != 0, nil
}

func (s *Store) applyMigration(ctx context.Context, version int, contents string) error {
	if s.dialect == MySQL {
		for _, statement := range migrationStatements(contents) {
			if _, err := s.db.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migration %d: %w", version, err)
			}
		}
		if version == 1 {
			if err := s.finalizeMySQLInitialSchema(ctx); err != nil {
				return fmt.Errorf("migration %d: %w", version, err)
			}
		}
		return s.recordMigration(ctx, version)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range migrationStatements(contents) {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migration %d: %w", version, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`, version, time.Now().UTC()); err != nil {
		return err
	}
	return tx.Commit()
}

func migrationStatements(contents string) []string {
	var statements []string
	for _, statement := range strings.Split(contents, "-- statement") {
		if strings.TrimSpace(statement) != "" {
			statements = append(statements, statement)
		}
	}
	return statements
}

func (s *Store) recordMigration(ctx context.Context, version int) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`, version, time.Now().UTC())
	return err
}

// finalizeMySQLInitialSchema reconciles the original, unversioned PHP schema
// with the application's initial schema. MySQL DDL commits implicitly, so each
// operation is deliberately safe to retry before the migration is recorded.
func (s *Store) finalizeMySQLInitialSchema(ctx context.Context) error {
	var engine string
	if err := s.db.QueryRowContext(ctx, `SELECT engine FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='pastes'`).Scan(&engine); err != nil {
		return fmt.Errorf("inspect pastes storage engine: %w", err)
	}
	if !strings.EqualFold(engine, "InnoDB") {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE pastes ENGINE=InnoDB`); err != nil {
			return fmt.Errorf("convert pastes to InnoDB: %w", err)
		}
	}
	if err := s.ensureMySQLIndex(ctx, "pastes", "idx_pastes_expires", "expires"); err != nil {
		return err
	}

	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='pastes' AND column_name='ip'`).Scan(&count); err != nil {
		return fmt.Errorf("inspect legacy paste IP column: %w", err)
	}
	if count == 0 {
		return nil
	}

	if _, err := s.db.ExecContext(ctx, `INSERT INTO submission_events(ip, date)
		SELECT p.ip, p.date FROM pastes p
		WHERE p.date >= UTC_TIMESTAMP() - INTERVAL 1 DAY
		  AND p.ip IS NOT NULL AND p.date IS NOT NULL
		  AND NOT EXISTS (SELECT 1 FROM submission_events e WHERE e.ip = p.ip AND e.date = p.date)`); err != nil {
		return fmt.Errorf("preserve legacy rate-limit events: %w", err)
	}
	if err := s.dropMySQLIndexIfExists(ctx, "pastes", "idx_pastes_ip_date"); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE pastes DROP COLUMN ip`); err != nil {
		return fmt.Errorf("drop legacy paste IP column: %w", err)
	}
	return nil
}

func (s *Store) ensureMySQLIndex(ctx context.Context, table, name, columns string) error {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?`, table, name).Scan(&count)
	if err != nil {
		return err
	}
	if count == 0 {
		if _, err := s.db.ExecContext(ctx, "CREATE INDEX "+name+" ON "+table+" ("+columns+")"); err != nil {
			return fmt.Errorf("create index %s: %w", name, err)
		}
	}
	return nil
}

func (s *Store) dropMySQLIndexIfExists(ctx context.Context, table, name string) error {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?`, table, name).Scan(&count)
	if err != nil {
		return err
	}
	if count != 0 {
		if _, err := s.db.ExecContext(ctx, "DROP INDEX "+name+" ON "+table); err != nil {
			return fmt.Errorf("drop index %s: %w", name, err)
		}
	}
	return nil
}

func (s *Store) CheckSchema(ctx context.Context) error {
	var version int
	return s.db.QueryRowContext(ctx, `SELECT version FROM schema_migrations WHERE version = ?`, latestSchemaVersion).Scan(&version)
}

func (s *Store) CreatePaste(ctx context.Context, p Paste, ipHash string, limits Limits) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if s.dialect == MySQL {
		_, err = tx.ExecContext(ctx, `INSERT INTO rate_limit_keys(ip, last_seen) VALUES (?, ?) ON DUPLICATE KEY UPDATE last_seen=VALUES(last_seen)`, ipHash, p.CreatedAt)
		if err == nil {
			var ignored string
			err = tx.QueryRowContext(ctx, `SELECT ip FROM rate_limit_keys WHERE ip=? FOR UPDATE`, ipHash).Scan(&ignored)
		}
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO rate_limit_keys(ip, last_seen) VALUES (?, ?) ON CONFLICT(ip) DO UPDATE SET last_seen=excluded.last_seen`, ipHash, p.CreatedAt)
	}
	if err != nil {
		return err
	}

	hourStart, dayStart := p.CreatedAt.Add(-time.Hour), p.CreatedAt.Add(-24*time.Hour)
	var hourly, daily int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM submission_events WHERE ip=? AND date>?`, ipHash, hourStart).Scan(&hourly); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM submission_events WHERE ip=? AND date>?`, ipHash, dayStart).Scan(&daily); err != nil {
		return err
	}
	if hourly >= limits.PerHour || daily >= limits.PerDay {
		window, start := time.Hour, hourStart
		if daily >= limits.PerDay {
			window, start = 24*time.Hour, dayStart
		}
		var earliest time.Time
		if err := tx.QueryRowContext(ctx, `SELECT date FROM submission_events WHERE ip=? AND date>? ORDER BY date ASC LIMIT 1`, ipHash, start).Scan(&earliest); err != nil {
			return err
		}
		retry := earliest.Add(window).Sub(p.CreatedAt)
		if retry < time.Second {
			retry = time.Second
		}
		return &RateLimitError{RetryAfter: retry}
	}

	_, err = tx.ExecContext(ctx, `INSERT INTO pastes(ident, date, text, mimetype, expires) VALUES (?, ?, ?, ?, ?)`, p.ID, p.CreatedAt, p.Content, p.MIMEType, p.ExpiresAt)
	if err != nil {
		if isDuplicate(err) {
			return ErrIDCollision
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO submission_events(ip, date) VALUES (?, ?)`, ipHash, p.CreatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

func isDuplicate(err error) bool {
	var me *gomysql.MySQLError
	if errors.As(err, &me) && me.Number == 1062 {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "unique constraint failed") || strings.Contains(text, "constraint failed: unique")
}

func (s *Store) GetPaste(ctx context.Context, id string, now time.Time) (Paste, error) {
	var p Paste
	err := s.db.QueryRowContext(ctx, `SELECT ident, date, text, mimetype, expires FROM pastes WHERE ident=? AND expires>?`, id, now).Scan(
		&p.ID, &p.CreatedAt, &p.Content, &p.MIMEType, &p.ExpiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Paste{}, ErrNotFound
	}
	return p, err
}

func (s *Store) Cleanup(ctx context.Context, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, query := range []struct {
		q string
		t time.Time
	}{
		{`DELETE FROM pastes WHERE expires <= ?`, now},
		{`DELETE FROM submission_events WHERE date <= ?`, now.Add(-24 * time.Hour)},
		{`DELETE FROM rate_limit_keys WHERE last_seen <= ?`, now.Add(-24 * time.Hour)},
	} {
		if _, err := tx.ExecContext(ctx, query.q, query.t); err != nil {
			return err
		}
	}
	return tx.Commit()
}
