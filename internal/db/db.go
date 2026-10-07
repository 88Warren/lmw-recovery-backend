package db

import (
	"database/sql"
	"embed"
	"fmt"
	"log"
	"sort"

	_ "modernc.org/sqlite" // CGO-free SQLite driver
)

// Embed the migrations directory relative to this file (internal/db/ → ../../migrations).
// Go's embed requires the path to be within the module, so we embed from a
// sibling directory by placing a copy at internal/db/migrations via a symlink
// — instead, we embed directly and reference via the migrations sub-package.
//
// Simplest approach: embed the SQL inline here and avoid path issues entirely.

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open opens (or creates) the SQLite database at path and runs all migrations.
func Open(path string) (*sql.DB, error) {
	conn, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// SQLite performs best with a single writer connection.
	conn.SetMaxOpenConns(1)

	if err := migrate(conn); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return conn, nil
}

func migrate(conn *sql.DB) error {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}

	// Sort by filename so migrations run in order.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return fmt.Errorf("read %s: %w", e.Name(), err)
		}
		if _, err := conn.Exec(string(data)); err != nil {
			return fmt.Errorf("exec %s: %w", e.Name(), err)
		}
		log.Printf("migration applied: %s", e.Name())
	}
	return nil
}
