package handlers

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"lmwrecovery/backend/internal/models"
)

// ── POST /api/admin/slots ─────────────────────────────────────────────────
// Body: { "starts_at": "2026-09-02T18:00:00Z" }
// Or bulk: { "bulk": [ { "starts_at": "..." }, ... ] }

type slotInput struct {
	StartsAt string `json:"starts_at"` // RFC3339
}

type createSlotsRequest struct {
	StartsAt string      `json:"starts_at"`
	Bulk     []slotInput `json:"bulk"`
}

func AdminCreateSlotsHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createSlotsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		var inputs []slotInput
		if len(req.Bulk) > 0 {
			inputs = req.Bulk
		} else if req.StartsAt != "" {
			inputs = []slotInput{{StartsAt: req.StartsAt}}
		} else {
			jsonError(w, "provide starts_at or a bulk array", http.StatusBadRequest)
			return
		}

		created := make([]models.Slot, 0, len(inputs))
		for _, inp := range inputs {
			starts, err := time.Parse(time.RFC3339, inp.StartsAt)
			if err != nil {
				jsonError(w, "starts_at must be RFC3339: "+inp.StartsAt, http.StatusBadRequest)
				return
			}

			var s models.Slot
			err = db.QueryRow(
				`INSERT INTO slots (starts_at)
				 VALUES (?)
				 ON CONFLICT(starts_at) DO NOTHING
				 RETURNING id, starts_at`,
				starts.UTC().Format(time.RFC3339),
			).Scan(&s.ID, &s.StartsAt)
			if err != nil && err != sql.ErrNoRows {
				log.Printf("ERROR insert slot: %v", err)
				jsonError(w, "could not create slot", http.StatusInternalServerError)
				return
			}
			if err == nil {
				created = append(created, s)
			}
		}

		jsonOK(w, map[string]any{"created": len(created), "slots": created})
	}
}

// ── DELETE /api/admin/slots/{id} ──────────────────────────────────────────

func AdminDeleteSlotHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(strings.TrimPrefix(r.PathValue("id"), ""), 10, 64)
		if err != nil || id == 0 {
			jsonError(w, "invalid slot id", http.StatusBadRequest)
			return
		}

		var active int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM bookings WHERE slot_id = ? AND status != 'cancelled'`, id,
		).Scan(&active); err != nil {
			jsonError(w, "server error", http.StatusInternalServerError)
			return
		}
		if active > 0 {
			jsonError(w, "slot has an active booking — cancel the booking first", http.StatusConflict)
			return
		}

		res, err := db.Exec(`DELETE FROM slots WHERE id = ?`, id)
		if err != nil {
			jsonError(w, "could not delete slot", http.StatusInternalServerError)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			jsonError(w, "slot not found", http.StatusNotFound)
			return
		}
		jsonOK(w, map[string]any{"deleted": id})
	}
}

// ── GET /api/admin/slots ──────────────────────────────────────────────────

type adminBookingSummary struct {
	ID          int64  `json:"id"`
	ClientName  string `json:"client_name"`
	ClientEmail string `json:"client_email"`
	Treatment   string `json:"treatment"`
	DurationMin int    `json:"duration_minutes"`
	Status      string `json:"status"`
}

func AdminListSlotsHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.Query(`
			SELECT s.id, s.starts_at,
			       b.id, b.client_name, b.client_email, b.treatment,
			       b.treatment_duration_minutes, b.status
			FROM slots s
			LEFT JOIN bookings b ON b.slot_id = s.id AND b.status != 'cancelled'
			ORDER BY s.starts_at DESC
			LIMIT 200
		`)
		if err != nil {
			log.Printf("ERROR list admin slots: %v", err)
			jsonError(w, "could not fetch slots", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		type slotRow struct {
			models.Slot
			Booking *adminBookingSummary `json:"booking,omitempty"`
		}

		result := make([]slotRow, 0)
		for rows.Next() {
			var sr slotRow
			var startsStr string
			var bID sql.NullInt64
			var bName, bEmail, bTreat, bStatus sql.NullString
			var bDur sql.NullInt64

			if err := rows.Scan(
				&sr.ID, &startsStr,
				&bID, &bName, &bEmail, &bTreat, &bDur, &bStatus,
			); err != nil {
				log.Printf("ERROR scan admin slot: %v", err)
				continue
			}
			sr.StartsAt, _ = time.Parse(time.RFC3339, startsStr)

			if bID.Valid {
				sr.Booking = &adminBookingSummary{
					ID:          bID.Int64,
					ClientName:  bName.String,
					ClientEmail: bEmail.String,
					Treatment:   bTreat.String,
					DurationMin: int(bDur.Int64),
					Status:      bStatus.String,
				}
			}
			result = append(result, sr)
		}
		jsonOK(w, result)
	}
}

// ── GET /api/admin/bookings ───────────────────────────────────────────────

func AdminListBookingsHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.Query(`
			SELECT b.id, b.treatment, b.treatment_duration_minutes,
			       b.client_name, b.client_email, b.client_phone,
			       b.notes, b.status, b.brevo_sent, b.created_at,
			       s.starts_at
			FROM bookings b
			JOIN slots s ON s.id = b.slot_id
			ORDER BY s.starts_at DESC
			LIMIT 500
		`)
		if err != nil {
			log.Printf("ERROR list bookings: %v", err)
			jsonError(w, "could not fetch bookings", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		type bookingRow struct {
			models.Booking
			StartsAt time.Time `json:"starts_at"`
		}

		result := make([]bookingRow, 0)
		for rows.Next() {
			var br bookingRow
			var createdStr, startsStr string
			var brevoInt int

			if err := rows.Scan(
				&br.ID, &br.Treatment, &br.TreatmentDurationMins,
				&br.ClientName, &br.ClientEmail, &br.ClientPhone,
				&br.Notes, &br.Status, &brevoInt, &createdStr,
				&startsStr,
			); err != nil {
				log.Printf("ERROR scan booking: %v", err)
				continue
			}
			br.BrevoSent = brevoInt == 1
			br.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
			br.StartsAt, _ = time.Parse(time.RFC3339, startsStr)
			result = append(result, br)
		}
		jsonOK(w, result)
	}
}

// ── PATCH /api/admin/bookings/{id}/cancel ─────────────────────────────────

func AdminCancelBookingHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id == 0 {
			jsonError(w, "invalid booking id", http.StatusBadRequest)
			return
		}

		res, err := db.Exec(`UPDATE bookings SET status = 'cancelled' WHERE id = ?`, id)
		if err != nil {
			jsonError(w, "server error", http.StatusInternalServerError)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			jsonError(w, "booking not found", http.StatusNotFound)
			return
		}
		jsonOK(w, map[string]any{"cancelled": id})
	}
}

// ── GET /api/admin/contacts ───────────────────────────────────────────────

func AdminListContactsHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.Query(`
			SELECT id, name, email, phone, message, created_at
			FROM contacts ORDER BY created_at DESC LIMIT 500
		`)
		if err != nil {
			log.Printf("ERROR list contacts: %v", err)
			jsonError(w, "could not fetch contacts", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		result := make([]models.Contact, 0)
		for rows.Next() {
			var c models.Contact
			var createdStr string
			if err := rows.Scan(&c.ID, &c.Name, &c.Email, &c.Phone, &c.Message, &createdStr); err != nil {
				continue
			}
			c.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
			result = append(result, c)
		}
		jsonOK(w, result)
	}
}
