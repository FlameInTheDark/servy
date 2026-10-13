// Package database opens the SQLite database, runs migrations, and exposes the
// sqlc-generated query layer. WAL pragmas match the legacy panel (WAL, busy_timeout 5s, NORMAL).
package database

import (
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "modernc.org/sqlite" // pure-Go sqlite driver

	"github.com/FlameInTheDark/servy/internal/repo"
	"github.com/FlameInTheDark/servy/internal/repo/gen"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Open opens (creating if needed) the SQLite database at path and runs all
// pending migrations. The returned DB is tuned for the single-writer pattern.
func Open(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// modernc sqlite: single write connection avoids SQLITE_BUSY storms
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	if err := Migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// Migrate applies all embedded up migrations.
func Migrate(db *sql.DB) error {
	src, err := iofs.New(migrationFS, "migrations")
	if err != nil {
		return err
	}
	driver, err := sqlite.WithInstance(db, &sqlite.Config{})
	if err != nil {
		return err
	}
	m, err := migrate.NewWithInstance("iofs", src, "sqlite", driver)
	if err != nil {
		return err
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// Now returns RFC3339 UTC timestamp (convenience used by every repo call site).
func Now() string { return time.Now().UTC().Format(time.RFC3339) }

// NowString is an alias of Now used by hot loops.
func NowString() string { return Now() }

// Queries wraps the ctx-free query layer for dependency injection.
type Queries struct {
	repo.Q
	DB *sql.DB
}

// NewQueries builds the query wrapper.
func NewQueries(db *sql.DB) *Queries {
	return &Queries{Q: repo.NewQ(gen.New(db)), DB: db}
}

// Tx runs fn inside a transaction, rolling back on error.
func (q *Queries) Tx(fn func(txgen *gen.Queries) error) error {
	tx, err := q.DB.Begin()
	if err != nil {
		return err
	}
	if err := fn(gen.New(tx)); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
