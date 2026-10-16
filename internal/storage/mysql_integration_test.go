package storage

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
)

func TestMySQLLegacyMigrationAndContract(t *testing.T) {
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TEST_MYSQL_DSN is not set")
	}
	cfg, err := gomysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	if cfg.Params == nil {
		cfg.Params = make(map[string]string)
	}
	cfg.Params["time_zone"] = "'+00:00'"
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	for _, table := range []string{"submission_events", "rate_limit_keys", "schema_migrations", "pastes"} {
		if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS "+table); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, table := range []string{"submission_events", "rate_limit_keys", "schema_migrations", "pastes"} {
			_, _ = db.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+table)
		}
	})
	_, err = db.ExecContext(ctx, `CREATE TABLE pastes (
        ident varchar(32) NOT NULL DEFAULT '', date datetime DEFAULT NULL,
        text mediumblob, mimetype varchar(255) DEFAULT NULL, ip varchar(255) DEFAULT NULL,
        expires datetime DEFAULT NULL, PRIMARY KEY (ident),
        KEY idx_pastes_ip_date (ip, date)
    ) ENGINE=MyISAM DEFAULT CHARSET=utf8`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	legacyID := "legacypasteidentifier00000"
	_, err = db.ExecContext(ctx, `INSERT INTO pastes(ident,date,text,mimetype,ip,expires) VALUES(?,?,?,?,?,?)`, legacyID, now.Add(-10*time.Minute), []byte("legacy"), "text/plain", "legacy-ip-hash", now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	store := New(db, MySQL)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := store.GetPaste(ctx, legacyID, now)
	if err != nil || string(p.Content) != "legacy" {
		t.Fatalf("legacy GetPaste() = %+v, %v", p, err)
	}
	var engine string
	if err := db.QueryRowContext(ctx, `SELECT engine FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='pastes'`).Scan(&engine); err != nil {
		t.Fatal(err)
	}
	if engine != "InnoDB" {
		t.Fatalf("pastes engine = %q", engine)
	}
	var events int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM submission_events WHERE ip=?`, "legacy-ip-hash").Scan(&events); err != nil || events != 1 {
		t.Fatalf("seeded rate-limit events = %d, %v", events, err)
	}
	assertPasteIPSchemaRemoved(t, db, MySQL)
	if err := store.CheckSchema(ctx); err != nil {
		t.Fatalf("CheckSchema() = %v", err)
	}
	created := testPaste("newpasteidentifier00000000", now)
	if err := store.CreatePaste(ctx, created, "new-ip", Limits{PerHour: 60, PerDay: 100}); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("second migration was not idempotent: %v", err)
	}
}
