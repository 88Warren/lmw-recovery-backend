package handlers

import (
	"database/sql"
	"log"
	"net/http"
	"strconv"
	"time"

	"lmwrecovery/backend/internal/models"
)

// SlotsHandler handles GET /api/slots
//
// Query params:
//
//	date     = YYYY-MM-DD  (optional) filter to a single calendar day
//	duration = minutes     (optional, default 60) treatment duration in minutes
//
// Returns all start times that have no overlapping confirmed/pending booking
// for a window of [starts_at, starts_at + duration).
//
// Overlap condition:
//
//	An existing booking blocks a candidate start time S with duration D if:
//	  booking.starts_at < S + D   AND   S < booking.starts_at + booking.treatment_duration_minutes
func SlotsHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Parse duration (default 60)
		durationMins := 60
		if d := r.URL.Query().Get("duration"); d != "" {
			v, err := strconv.Atoi(d)
			if err != nil || v <= 0 {
				jsonError(w, "duration must be a positive integer (minutes)", http.StatusBadRequest)
				return
			}
			durationMins = v
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
			windowStart = time.Now().UTC()
			windowEnd = windowStart.Add(60 * 24 * time.Hour)
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
		rows.Close()

		if len(raw) == 0 {
			jsonOK(w, []models.Slot{})
			return
		}

		// For each candidate slot check whether any confirmed/pending booking overlaps.
		//
		// A booking with start B and duration BD overlaps candidate start S with duration D when:
		//   B < S+D  AND  S < B+BD
		//
		// We pass S and D as parameters; the DB computes S+D in seconds via datetime().
		available := make([]models.Slot, 0, len(raw))
		for _, s := range raw {
			endsAt := s.startsAt.Add(time.Duration(durationMins) * time.Minute)

			var conflicts int
			err := db.QueryRow(`
				SELECT COUNT(*) FROM bookings b
				JOIN slots sl ON sl.id = b.slot_id
				WHERE b.status IN ('confirmed','pending')
				  AND datetime(sl.starts_at) < datetime(?)
				  AND datetime(?) < datetime(sl.starts_at, '+' || b.treatment_duration_minutes || ' minutes')
			`,
				endsAt.Format(time.RFC3339),
				s.startsAt.Format(time.RFC3339),
			).Scan(&conflicts)

			if err != nil {
				log.Printf("ERROR overlap check for slot %d: %v", s.id, err)
				continue
			}

			if conflicts == 0 {
				available = append(available, models.Slot{
					ID:       s.id,
					StartsAt: s.startsAt,
				})
			}
		}

		jsonOK(w, available)
	}
}

// TreatmentsHandler handles GET /api/treatments
func TreatmentsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, models.Treatments)
	}
}
