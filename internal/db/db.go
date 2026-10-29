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

	if err := reconcileSchema(conn); err != nil {
		return nil, fmt.Errorf("reconcile schema: %w", err)
	}

	return conn, nil
}

// reconcileSchema brings the slots/bookings tables into the start-time-only
// shape the application code expects, regardless of whether the database was
// created fresh from migration 001 (legacy schema) or hand-patched earlier.
//
// It is idempotent: on an already-correct database it does nothing.
//
// Three drifts are repaired:
//  1. slots still carries the legacy `ends_at`/`available` columns — the
//     scheduler inserts start-time-only rows, so `ends_at NOT NULL` makes
//     seeding fail. We rebuild slots to (id, starts_at UNIQUE, blocked).
//  2. bookings is missing `treatment_duration_minutes` — the overlap queries
//     reference it. We add it with a sensible default (60) while preserving
//     any existing values.
//  3. slots is missing the `blocked` flag (migration 009) — e.g. on a legacy
//     database that migration 009 could not patch because the slots rebuild in
//     step 1 ran in the same boot and recreated the table without it. We add it
//     idempotently with a default of 0 (open).
func reconcileSchema(conn *sql.DB) error {
	// ── bookings.treatment_duration_minutes ─────────────────────────────────
	hasDuration, err := columnExists(conn, "bookings", "treatment_duration_minutes")
	if err != nil {
		return err
	}
	if !hasDuration {
		if _, err := conn.Exec(
			`ALTER TABLE bookings ADD COLUMN treatment_duration_minutes INTEGER NOT NULL DEFAULT 60`,
		); err != nil {
			return fmt.Errorf("add bookings.treatment_duration_minutes: %w", err)
		}
		log.Printf("schema: added bookings.treatment_duration_minutes")
	}

	// ── slots start-time-only rebuild ────────────────────────────────────────
	hasEndsAt, err := columnExists(conn, "slots", "ends_at")
	if err != nil {
		return err
	}
	hasAvailable, err := columnExists(conn, "slots", "available")
	if err != nil {
		return err
	}
	if !hasEndsAt && !hasAvailable {
		// Already start-time-only; just make sure the blocked flag is present.
		return ensureSlotsBlockedColumn(conn)
	}

	// Rebuild slots via the standard SQLite table-alter recipe: create new, copy,
	// drop old, rename. Foreign-key enforcement must be disabled around a schema
	// change like this (SQLite re-validates the bookings.slot_id reference when
	// the referenced table is dropped/renamed). The PRAGMA cannot be changed
	// inside a transaction, so it is toggled on the connection around the tx.
	//
	// We intentionally only copy existing rows — including any that are already
	// orphaned by pre-existing data — rather than deleting them, so this repair
	// never destroys data a human may still want to inspect.
	// Preserve an existing `blocked` flag if the legacy table happened to have
	// one; otherwise default every copied row to 0 (open). This must be checked
	// BEFORE opening the transaction below: the pool is capped to a single
	// connection (SetMaxOpenConns(1)), so a query issued while a tx holds that
	// connection would deadlock.
	hadBlocked, err := columnExists(conn, "slots", "blocked")
	if err != nil {
		return err
	}
	copyBlocked := "0"
	if hadBlocked {
		copyBlocked = "blocked"
	}

	if _, err := conn.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable fks: %w", err)
	}
	defer func() {
		if _, err := conn.Exec(`PRAGMA foreign_keys = ON`); err != nil {
			log.Printf("schema: WARNING failed to re-enable foreign_keys: %v", err)
		}
	}()

	tx, err := conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	stmts := []string{
		`CREATE TABLE slots_new (
			id        INTEGER PRIMARY KEY AUTOINCREMENT,
			starts_at DATETIME NOT NULL,
			blocked   INTEGER  NOT NULL DEFAULT 0,
			UNIQUE(starts_at)
		)`,
		`INSERT OR IGNORE INTO slots_new (id, starts_at, blocked) SELECT id, starts_at, ` + copyBlocked + ` FROM slots`,
		`DROP TABLE slots`,
		`ALTER TABLE slots_new RENAME TO slots`,
		`CREATE INDEX IF NOT EXISTS idx_slots_starts_at ON slots(starts_at)`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return fmt.Errorf("rebuild slots: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit slots rebuild: %w", err)
	}
	log.Printf("schema: rebuilt slots as start-time-only")
	return nil
}

// ensureSlotsBlockedColumn adds slots.blocked if it is missing. Idempotent: a
// no-op once the column exists. Covers databases whose slots table was created
// (or rebuilt) without the blocked flag migration 009 adds.
func ensureSlotsBlockedColumn(conn *sql.DB) error {
	has, err := columnExists(conn, "slots", "blocked")
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	if _, err := conn.Exec(
		`ALTER TABLE slots ADD COLUMN blocked INTEGER NOT NULL DEFAULT 0`,
	); err != nil {
		return fmt.Errorf("add slots.blocked: %w", err)
	}
	log.Printf("schema: added slots.blocked")
	return nil
}

// columnExists reports whether the given table has a column with the given name.
func columnExists(conn *sql.DB, table, column string) (bool, error) {
	rows, err := conn.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, fmt.Errorf("table_info(%s): %w", table, err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid       int
			name      string
			ctype     string
			notnull   int
			dfltValue sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return false, fmt.Errorf("scan table_info(%s): %w", table, err)
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func migrate(conn *sql.DB) error {
	// Track which migrations have already run so each file executes exactly once.
	// Previously every file was Exec'd on every startup, which is only safe for
	// idempotent CREATE TABLE IF NOT EXISTS scripts — a destructive/rewriting
	// migration (e.g. rebuilding a table) would re-run on every boot.
	if _, err := conn.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		name       TEXT PRIMARY KEY,
		applied_at DATETIME DEFAULT (datetime('now'))
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied := make(map[string]bool)
	rows, err := conn.Query(`SELECT name FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("read schema_migrations: %w", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return fmt.Errorf("scan schema_migrations: %w", err)
		}
		applied[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate schema_migrations: %w", err)
	}
	rows.Close()

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
		name := e.Name()
		if applied[name] {
			continue
		}

		data, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}

		tx, err := conn.Begin()
		if err != nil {
			return fmt.Errorf("begin tx for %s: %w", name, err)
		}
		if _, err := tx.Exec(string(data)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("exec %s: %w", name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (name) VALUES (?)`, name); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit %s: %w", name, err)
		}

		log.Printf("migration applied: %s", name)
	}
	return nil
}
