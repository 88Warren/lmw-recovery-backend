package handlers

import (
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// setupFreeTestDB creates a minimal start-time-only schema for freeMinutesAt tests.
func setupFreeTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=on")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	stmts := []string{
		`CREATE TABLE slots (id INTEGER PRIMARY KEY AUTOINCREMENT, starts_at DATETIME NOT NULL, UNIQUE(starts_at))`,
		`CREATE TABLE bookings (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			slot_id INTEGER NOT NULL REFERENCES slots(id),
			treatment TEXT NOT NULL,
			treatment_duration_minutes INTEGER NOT NULL DEFAULT 60,
			client_name TEXT NOT NULL,
			client_email TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'confirmed'
		)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	return db
}

func addSlot(t *testing.T, db *sql.DB, iso string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO slots (starts_at) VALUES (?)`, iso)
	if err != nil {
		t.Fatalf("insert slot %s: %v", iso, err)
	}
	id, _ := res.LastInsertId()
	return id
}

func book(t *testing.T, db *sql.DB, slotID int64, dur int) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO bookings (slot_id, treatment, treatment_duration_minutes, client_name, client_email, status)
		 VALUES (?, 'Test', ?, 'A', 'a@b.c', 'confirmed')`, slotID, dur,
	); err != nil {
		t.Fatalf("book slot %d: %v", slotID, err)
	}
}

func mustParse(t *testing.T, iso string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		t.Fatalf("parse %s: %v", iso, err)
	}
	return ts
}

// TestFreeMinutesPartialGap reproduces the reported scenario:
// Sunday 09:00 booked for 60 min, 10:30 booked for 60 min. The 10:00 slot has
// only 30 free minutes (10:00 -> 10:30), 09:00 is fully taken (0), and 10:30 is
// taken (0).
func TestFreeMinutesPartialGap(t *testing.T) {
	db := setupFreeTestDB(t)

	addSlot(t, db, "2026-10-18T09:00:00Z")
	s0930 := addSlot(t, db, "2026-10-18T09:30:00Z")
	s1000 := addSlot(t, db, "2026-10-18T10:00:00Z")
	s1030 := addSlot(t, db, "2026-10-18T10:30:00Z")
	addSlot(t, db, "2026-10-18T11:00:00Z")

	s0900Row := int64(1)
	book(t, db, s0900Row, 60) // 09:00–10:00
	book(t, db, s1030, 60)    // 10:30–11:30

	cases := []struct {
		name string
		iso  string
		want int
	}{
		{"09:00 fully taken", "2026-10-18T09:00:00Z", 0},
		{"09:30 inside first booking", "2026-10-18T09:30:00Z", 0},
		{"10:00 only 30 min free", "2026-10-18T10:00:00Z", 30},
		{"10:30 taken by second booking", "2026-10-18T10:30:00Z", 0},
		{"11:00 inside second booking", "2026-10-18T11:00:00Z", 0},
	}
	_ = s0930
	_ = s1000

	for _, c := range cases {
		got, err := freeMinutesAt(db, mustParse(t, c.iso))
		if err != nil {
			t.Fatalf("%s: freeMinutesAt: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: free_minutes = %d, want %d", c.name, got, c.want)
		}
	}
}

// TestFreeMinutesNoBookings: a slot with nothing booked ahead reports the cap.
func TestFreeMinutesNoBookings(t *testing.T) {
	db := setupFreeTestDB(t)
	addSlot(t, db, "2026-10-18T09:00:00Z")

	got, err := freeMinutesAt(db, mustParse(t, "2026-10-18T09:00:00Z"))
	if err != nil {
		t.Fatalf("freeMinutesAt: %v", err)
	}
	if got != maxFreeMinutes {
		t.Errorf("free_minutes = %d, want %d (cap)", got, maxFreeMinutes)
	}
}
