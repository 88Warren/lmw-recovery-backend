package db

import (
	"path/filepath"
	"testing"

	"lmwrecovery/backend/internal/scheduler"
)

// TestFreshProdLikeDB verifies that a brand-new database (which starts life with
// the legacy schema from migration 001) ends up in the start-time-only shape and
// that slot seeding succeeds — the exact production path that previously failed.
func TestFreshProdLikeDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.db")

	conn, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer conn.Close()

	// slots must be start-time-only (no legacy columns).
	for _, col := range []string{"ends_at", "available"} {
		has, err := columnExists(conn, "slots", col)
		if err != nil {
			t.Fatalf("columnExists slots.%s: %v", col, err)
		}
		if has {
			t.Errorf("slots still has legacy column %q", col)
		}
	}

	// bookings must have treatment_duration_minutes.
	has, err := columnExists(conn, "bookings", "treatment_duration_minutes")
	if err != nil {
		t.Fatalf("columnExists bookings.treatment_duration_minutes: %v", err)
	}
	if !has {
		t.Error("bookings is missing treatment_duration_minutes")
	}

	// Seeding must succeed and actually insert future slots.
	if err := scheduler.SeedDefaultSlots(conn); err != nil {
		t.Fatalf("SeedDefaultSlots: %v", err)
	}

	var count int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM slots`).Scan(&count); err != nil {
		t.Fatalf("count slots: %v", err)
	}
	if count == 0 {
		t.Error("expected seeded slots, got 0")
	}
	t.Logf("seeded %d slots on a fresh prod-like DB", count)
}

// TestReconcileIdempotent confirms that opening an already-reconciled database a
// second time performs no rebuild and leaves data untouched.
func TestReconcileIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idem.db")

	conn1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := scheduler.SeedDefaultSlots(conn1); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var before int
	if err := conn1.QueryRow(`SELECT COUNT(*) FROM slots`).Scan(&before); err != nil {
		t.Fatalf("count before: %v", err)
	}
	conn1.Close()

	// Second open must not alter the already-correct schema.
	conn2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer conn2.Close()

	var after int
	if err := conn2.QueryRow(`SELECT COUNT(*) FROM slots`).Scan(&after); err != nil {
		t.Fatalf("count after: %v", err)
	}
	if before != after {
		t.Errorf("slot count changed across reopen: before=%d after=%d", before, after)
	}

	// The /api/slots overlap query references treatment_duration_minutes — make
	// sure it is valid against the reconciled schema.
	if _, err := conn2.Exec(`
		SELECT COUNT(*) FROM bookings b
		JOIN slots sl ON sl.id = b.slot_id
		WHERE b.status IN ('confirmed','pending')
		  AND datetime(sl.starts_at, '+' || b.treatment_duration_minutes || ' minutes') > datetime('now')
	`); err != nil {
		t.Errorf("overlap-style query failed against reconciled schema: %v", err)
	}
}

// TestPaymentColumnsPresent verifies migration 004 adds the booking payment
// columns on a fresh database.
func TestPaymentColumnsPresent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pay.db")
	conn, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer conn.Close()

	for _, col := range []string{
		"amount_pence", "currency", "payment_status",
		"stripe_session_id", "stripe_payment_intent_id", "paid_at",
	} {
		has, err := columnExists(conn, "bookings", col)
		if err != nil {
			t.Fatalf("columnExists bookings.%s: %v", col, err)
		}
		if !has {
			t.Errorf("bookings is missing payment column %q", col)
		}
	}

	// A pending/unpaid insert must work with the new columns.
	if _, err := conn.Exec(`INSERT INTO slots (starts_at) VALUES ('2027-01-01T09:00:00Z')`); err != nil {
		t.Fatalf("insert slot: %v", err)
	}
	if _, err := conn.Exec(`
		INSERT INTO bookings
		  (slot_id, treatment, treatment_duration_minutes, client_name, client_email,
		   status, amount_pence, currency, payment_status)
		VALUES ((SELECT id FROM slots LIMIT 1), 'Sports Massage', 60, 'A', 'a@b.c',
		        'pending', 4500, 'gbp', 'unpaid')`); err != nil {
		t.Fatalf("insert pending booking: %v", err)
	}
}

// TestSubscriptionSchemaPresent verifies migration 005 creates the
// subscriptions table and adds bookings.subscription_id.
func TestSubscriptionSchemaPresent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub.db")
	conn, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer conn.Close()

	has, err := columnExists(conn, "bookings", "subscription_id")
	if err != nil {
		t.Fatalf("columnExists bookings.subscription_id: %v", err)
	}
	if !has {
		t.Error("bookings is missing subscription_id")
	}

	// subscriptions table must accept an insert with the expected columns.
	if _, err := conn.Exec(`
		INSERT INTO subscriptions
		  (client_email, client_name, stripe_customer_id, stripe_subscription_id, status, cancel_at_period_end)
		VALUES ('a@b.c', 'A B', 'cus_x', 'sub_x', 'active', 0)`); err != nil {
		t.Fatalf("insert subscription: %v", err)
	}

	var status string
	if err := conn.QueryRow(`SELECT status FROM subscriptions WHERE stripe_subscription_id = 'sub_x'`).Scan(&status); err != nil {
		t.Fatalf("read subscription: %v", err)
	}
	if status != "active" {
		t.Errorf("status = %q, want active", status)
	}
}
