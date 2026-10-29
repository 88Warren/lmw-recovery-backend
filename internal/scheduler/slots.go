// Package scheduler seeds the studio's default availability as start-time-only
// slots (no duration encoded). Availability for a given duration is determined
// at booking time via an overlap query.
//
// Default schedule (Europe/London):
//   Tue – Thu : 18:00  18:30  19:00  19:30  20:00  20:30
//   Sunday    : 09:00  09:30  10:00  10:30  11:00  11:30
//
// On each startup — and then once a day via StartSeedLoop — it inserts missing
// future slots using INSERT OR IGNORE so admin-deleted slots stay deleted,
// already-seeded slots are untouched, and the bookable horizon keeps rolling
// forward (always ~3 months ahead) without needing a restart.

package scheduler

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"
)

// WeeksAhead is how far into the future default availability is seeded. 13 weeks
// keeps roughly 3 months of slots open for booking at all times.
const WeeksAhead = 13
const locationName = "Europe/London"

var defaultSchedule = []struct {
	weekday time.Weekday
	starts  []string // HH:MM local time
}{
	{time.Tuesday, []string{"18:00", "18:30", "19:00", "19:30", "20:00", "20:30"}},
	{time.Wednesday, []string{"18:00", "18:30", "19:00", "19:30", "20:00", "20:30"}},
	{time.Thursday, []string{"18:00", "18:30", "19:00", "19:30", "20:00", "20:30"}},
	{time.Sunday, []string{"09:00", "09:30", "10:00", "10:30", "11:00", "11:30"}},
}

// SeedDefaultSlots inserts default start-time slots for the next WeeksAhead weeks.
// Safe to call on every startup — uses INSERT OR IGNORE.
func SeedDefaultSlots(db *sql.DB) error {
	loc, err := time.LoadLocation(locationName)
	if err != nil {
		return fmt.Errorf("load location %q: %w", locationName, err)
	}

	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	end := today.AddDate(0, 0, WeeksAhead*7)

	inserted := 0
	for d := today; d.Before(end); d = d.AddDate(0, 0, 1) {
		for _, rule := range defaultSchedule {
			if d.Weekday() != rule.weekday {
				continue
			}
			for _, startStr := range rule.starts {
				var h, m int
				if _, err := fmt.Sscanf(startStr, "%d:%d", &h, &m); err != nil {
					log.Printf("scheduler: bad time %q: %v", startStr, err)
					continue
				}

				startsAt := time.Date(d.Year(), d.Month(), d.Day(), h, m, 0, 0, loc).UTC()
				if startsAt.Before(time.Now().UTC()) {
					continue
				}

				res, err := db.Exec(
					`INSERT OR IGNORE INTO slots (starts_at) VALUES (?)`,
					startsAt.Format(time.RFC3339),
				)
				if err != nil {
					return fmt.Errorf("insert slot %s: %w", startsAt.Format(time.RFC3339), err)
				}
				n, _ := res.RowsAffected()
				inserted += int(n)
			}
		}
	}

	if inserted > 0 {
		log.Printf("scheduler: seeded %d default slots (%d weeks ahead)", inserted, WeeksAhead)
	} else {
		log.Printf("scheduler: no new default slots needed")
	}
	return nil
}

// StartSeedLoop re-runs SeedDefaultSlots on an interval until the context is
// cancelled, so the bookable horizon keeps rolling forward (~3 months ahead)
// without a restart. SeedDefaultSlots is INSERT OR IGNORE, so this only ever
// adds newly-in-range future slots and never resurrects admin-deleted ones.
// Intended to be launched in a goroutine at startup. A daily interval is plenty
// since new slots only come into range as calendar days pass.
func StartSeedLoop(ctx context.Context, db *sql.DB, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := SeedDefaultSlots(db); err != nil {
				log.Printf("ERROR periodic slot seeding: %v", err)
			}
		}
	}
}
