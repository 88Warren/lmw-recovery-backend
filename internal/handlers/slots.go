package handlers

import (
	"database/sql"
	"log"
	"net/http"
	"strconv"
	"time"

	"lmwrecovery/backend/internal/models"
)

// maxFreeMinutes caps the reported free_minutes for a slot. No treatment is
// longer than an hour, so there is no need to report larger gaps; capping keeps
// the number meaningful ("plenty of time") rather than reflecting the distance
// to a booking days away.
const maxFreeMinutes = 120

// SlotsHandler handles GET /api/slots
//
// Query params:
//
//	date     = YYYY-MM-DD  (optional) filter to a single calendar day
//	duration = minutes     (optional) if set, only slots with at least this many
//	                        free minutes are returned; otherwise every slot in the
//	                        window is returned.
//
// Every returned slot carries free_minutes: the number of minutes free starting
// at that slot before the next confirmed/pending booking begins (capped at
// maxFreeMinutes). free_minutes == 0 means the start time itself is already
// taken. This lets the UI show taken/partial slots instead of hiding them.
//
// A booking B (start BStart, duration BDur) occupies [BStart, BStart+BDur). A
// candidate start S is blocked when S falls inside an existing booking; the free
// window at S runs until the start of the earliest booking that begins at or
// after S (bounded by maxFreeMinutes).
func SlotsHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Optional minimum-duration filter.
		minDuration := 0
		if d := r.URL.Query().Get("duration"); d != "" {
			v, err := strconv.Atoi(d)
			if err != nil || v <= 0 {
				jsonError(w, "duration must be a positive integer (minutes)", http.StatusBadRequest)
				return
			}
			minDuration = v
		}

		dateStr := r.URL.Query().Get("date")

		// Build the time window to query
		var windowStart, windowEnd time.Time
		if dateStr != "" {
			date, err := time.Parse("2006-01-02", dateStr)
			if err != nil {
				jsonError(w, "date must be YYYY-MM-DD", http.StatusBadRequest)
				return
			}
			windowStart = date.UTC().Truncate(24 * time.Hour)
			windowEnd = windowStart.Add(24 * time.Hour)
		} else {
			// No date filter: return the full seeded horizon (~3 months ahead,
			// matching scheduler.WeeksAhead) so the booking calendar can page
			// through every open week. A few days of slack past 13 weeks keeps
			// the last seeded week fully visible.
			windowStart = time.Now().UTC()
			windowEnd = windowStart.Add(95 * 24 * time.Hour)
		}

		// Fetch all slots in the window
		rows, err := db.Query(
			`SELECT id, starts_at FROM slots
			 WHERE starts_at >= ? AND starts_at < ?
			 ORDER BY starts_at`,
			windowStart.Format(time.RFC3339),
			windowEnd.Format(time.RFC3339),
		)
		if err != nil {
			log.Printf("ERROR query slots: %v", err)
			jsonError(w, "could not fetch slots", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		type rawSlot struct {
			id       int64
			startsAt time.Time
		}

		var raw []rawSlot
		for rows.Next() {
			var s rawSlot
			var startsStr string
			if err := rows.Scan(&s.id, &startsStr); err != nil {
				log.Printf("ERROR scan slot: %v", err)
				continue
			}
			s.startsAt, _ = time.Parse(time.RFC3339, startsStr)
			raw = append(raw, s)
		}
		if err := rows.Err(); err != nil {
			log.Printf("ERROR iterate slots: %v", err)
			jsonError(w, "could not fetch slots", http.StatusInternalServerError)
			return
		}
		rows.Close()

		if len(raw) == 0 {
			jsonOK(w, []models.Slot{})
			return
		}

		result := make([]models.Slot, 0, len(raw))
		for _, s := range raw {
			free, err := freeMinutesAt(db, s.startsAt)
			if err != nil {
				log.Printf("ERROR free-minutes check for slot %d: %v", s.id, err)
				continue
			}
			if minDuration > 0 && free < minDuration {
				continue
			}
			result = append(result, models.Slot{
				ID:          s.id,
				StartsAt:    s.startsAt,
				FreeMinutes: free,
			})
		}

		jsonOK(w, result)
	}
}

// freeMinutesAt returns how many minutes are free starting at s before the next
// confirmed/pending booking begins, capped at maxFreeMinutes.
//
//	0                        => the start time itself falls inside an existing booking
//	1..maxFreeMinutes        => a limited gap (only shorter treatments fit)
//	maxFreeMinutes           => effectively open
func freeMinutesAt(db *sql.DB, s time.Time) (int, error) {
	start := s.Format(time.RFC3339)

	// 0) Has Laura blocked this start time? A blocked slot is unavailable to
	//    clients even though no booking exists, so report it as fully taken.
	var blocked int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM slots WHERE datetime(starts_at) = datetime(?) AND blocked = 1`,
		start,
	).Scan(&blocked); err != nil {
		return 0, err
	}
	if blocked > 0 {
		return 0, nil
	}

	// 1) Is the start time itself inside an existing booking?
	//    An existing booking B occupies [B.start, B.start + B.dur). S is taken
	//    when B.start <= S < B.start + B.dur.
	var covering int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM bookings b
		JOIN slots sl ON sl.id = b.slot_id
		WHERE b.status IN ('confirmed','pending')
		  AND datetime(sl.starts_at) <= datetime(?)
		  AND datetime(?) < datetime(sl.starts_at, '+' || b.treatment_duration_minutes || ' minutes')
	`, start, start).Scan(&covering); err != nil {
		return 0, err
	}
	if covering > 0 {
		return 0, nil
	}

	// 2) Find the earliest booking that starts at or after S. The free window is
	//    the gap between S and that booking's start.
	var nextStartStr sql.NullString
	if err := db.QueryRow(`
		SELECT MIN(sl.starts_at) FROM bookings b
		JOIN slots sl ON sl.id = b.slot_id
		WHERE b.status IN ('confirmed','pending')
		  AND datetime(sl.starts_at) >= datetime(?)
	`, start).Scan(&nextStartStr); err != nil {
		return 0, err
	}

	if !nextStartStr.Valid {
		return maxFreeMinutes, nil // nothing booked ahead
	}

	nextStart, err := time.Parse(time.RFC3339, nextStartStr.String)
	if err != nil {
		return maxFreeMinutes, nil
	}

	gap := int(nextStart.Sub(s).Minutes())
	if gap < 0 {
		gap = 0
	}
	if gap > maxFreeMinutes {
		gap = maxFreeMinutes
	}
	return gap, nil
}

// TreatmentsHandler handles GET /api/treatments
func TreatmentsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, models.Treatments)
	}
}
