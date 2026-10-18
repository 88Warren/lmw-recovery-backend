package scheduler

import (
	"context"
	"database/sql"
	"log"
	"time"
)

// PendingHoldWindow is how long an unpaid 'pending' booking holds its slot
// before being swept to 'cancelled'. Stripe Checkout sessions expire after
// ~24h, but we release the slot sooner so abandoned checkouts don't block
// others for long.
const PendingHoldWindow = 30 * time.Minute

// ExpirePendingBookings cancels unpaid 'pending' bookings older than the hold
// window, freeing their slots. Returns the number cancelled.
func ExpirePendingBookings(db *sql.DB) (int64, error) {
	cutoff := time.Now().UTC().Add(-PendingHoldWindow).Format(time.RFC3339)
	res, err := db.Exec(`
		UPDATE bookings
		SET status = 'cancelled'
		WHERE status = 'pending'
		  AND payment_status = 'unpaid'
		  AND datetime(created_at) < datetime(?)`,
		cutoff,
	)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// StartPendingExpiryLoop runs ExpirePendingBookings on an interval until the
// context is cancelled. Intended to be launched in a goroutine at startup.
func StartPendingExpiryLoop(ctx context.Context, db *sql.DB, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := ExpirePendingBookings(db)
			if err != nil {
				log.Printf("ERROR expire pending bookings: %v", err)
				continue
			}
			if n > 0 {
				log.Printf("scheduler: expired %d unpaid pending booking(s)", n)
			}
		}
	}
}
