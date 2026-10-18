package scheduler

import (
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newPayTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	stmts := []string{
		`CREATE TABLE slots (id INTEGER PRIMARY KEY AUTOINCREMENT, starts_at DATETIME NOT NULL)`,
		`CREATE TABLE bookings (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			slot_id INTEGER NOT NULL,
			treatment TEXT NOT NULL,
			treatment_duration_minutes INTEGER NOT NULL DEFAULT 60,
			client_name TEXT NOT NULL, client_email TEXT NOT NULL,
			client_phone TEXT, notes TEXT,
			status TEXT NOT NULL DEFAULT 'pending',
			amount_pence INTEGER NOT NULL DEFAULT 0,
			currency TEXT NOT NULL DEFAULT 'gbp',
			payment_status TEXT NOT NULL DEFAULT 'unpaid',
			created_at DATETIME
		)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	if _, err := db.Exec(`INSERT INTO slots (id, starts_at) VALUES (1, '2026-10-20T17:00:00Z')`); err != nil {
		t.Fatalf("seed slot: %v", err)
	}
	return db
}

func insertBooking(t *testing.T, db *sql.DB, status, payStatus string, createdAt time.Time) int64 {
	t.Helper()
	res, err := db.Exec(`
		INSERT INTO bookings (slot_id, treatment, client_name, client_email, status, payment_status, created_at)
		VALUES (1, 'Sports Massage', 'A', 'a@b.c', ?, ?, ?)`,
		status, payStatus, createdAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("insert booking: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func statusOf(t *testing.T, db *sql.DB, id int64) string {
	t.Helper()
	var s string
	if err := db.QueryRow(`SELECT status FROM bookings WHERE id = ?`, id).Scan(&s); err != nil {
		t.Fatalf("status query: %v", err)
	}
	return s
}

// TestExpireCancelsOldUnpaidPending: a pending/unpaid booking older than the
// hold window is cancelled; a recent one and a paid one are left alone.
func TestExpireCancelsOldUnpaidPending(t *testing.T) {
	db := newPayTestDB(t)

	old := insertBooking(t, db, "pending", "unpaid", time.Now().Add(-PendingHoldWindow-time.Minute))
	recent := insertBooking(t, db, "pending", "unpaid", time.Now())
	paid := insertBooking(t, db, "confirmed", "paid", time.Now().Add(-2*time.Hour))

	n, err := ExpirePendingBookings(db)
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	if n != 1 {
		t.Errorf("expired %d, want 1", n)
	}
	if got := statusOf(t, db, old); got != "cancelled" {
		t.Errorf("old booking status = %q, want cancelled", got)
	}
	if got := statusOf(t, db, recent); got != "pending" {
		t.Errorf("recent booking status = %q, want pending", got)
	}
	if got := statusOf(t, db, paid); got != "confirmed" {
		t.Errorf("paid booking status = %q, want confirmed", got)
	}
}
