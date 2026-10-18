package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"lmwrecovery/backend/internal/mailer"
	"lmwrecovery/backend/internal/models"
	"lmwrecovery/backend/internal/payments"
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
			ID       int64                `json:"id"`
			StartsAt time.Time            `json:"starts_at"`
			Booking  *adminBookingSummary `json:"booking,omitempty"`
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
		if err := rows.Err(); err != nil {
			log.Printf("ERROR iterate admin slots: %v", err)
			jsonError(w, "could not fetch slots", http.StatusInternalServerError)
			return
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
			       s.starts_at,
			       b.payment_status, b.refund_status, b.cancellation_requested_at,
			       b.subscription_id
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
			StartsAt              time.Time `json:"starts_at"`
			RefundStatus          string    `json:"refund_status"`
			CancellationRequested bool      `json:"cancellation_requested"`
			IsMember              bool      `json:"is_member"`
		}

		result := make([]bookingRow, 0)
		for rows.Next() {
			var br bookingRow
			var createdStr, startsStr string
			var brevoInt int
			var cancelReqAt sql.NullString
			var subID sql.NullInt64

			if err := rows.Scan(
				&br.ID, &br.Treatment, &br.TreatmentDurationMins,
				&br.ClientName, &br.ClientEmail, &br.ClientPhone,
				&br.Notes, &br.Status, &brevoInt, &createdStr,
				&startsStr,
				&br.PaymentStatus, &br.RefundStatus, &cancelReqAt,
				&subID,
			); err != nil {
				log.Printf("ERROR scan booking: %v", err)
				continue
			}
			br.BrevoSent = brevoInt == 1
			br.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
			br.StartsAt, _ = time.Parse(time.RFC3339, startsStr)
			br.CancellationRequested = cancelReqAt.Valid
			br.IsMember = subID.Valid
			result = append(result, br)
		}
		if err := rows.Err(); err != nil {
			log.Printf("ERROR iterate bookings: %v", err)
			jsonError(w, "could not fetch bookings", http.StatusInternalServerError)
			return
		}
		jsonOK(w, result)
	}
}

// ── PATCH /api/admin/bookings/{id}/cancel ─────────────────────────────────
//
// Cancels a booking and, for a one-off paid booking, issues a FULL Stripe
// refund. Enforces the 24-hour rule: a session less than 24h away cannot be
// cancelled here (contact the client directly for late changes). Member
// (subscription) sessions are not individually paid, so they cancel with no
// refund — the monthly plan is managed separately via the customer portal.
func AdminCancelBookingHandler(db *sql.DB, deps BookingDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id == 0 {
			jsonError(w, "invalid booking id", http.StatusBadRequest)
			return
		}

		var (
			status        string
			paymentStatus string
			paymentIntent sql.NullString
			refundStatus  string
			subID         sql.NullInt64
			startsStr     string
			clientName    string
			clientEmail   string
			treatment     string
		)
		err = db.QueryRow(`
			SELECT b.status, b.payment_status, b.stripe_payment_intent_id, b.refund_status,
			       b.subscription_id, b.client_name, b.client_email, b.treatment, sl.starts_at
			FROM bookings b JOIN slots sl ON sl.id = b.slot_id
			WHERE b.id = ?`, id,
		).Scan(&status, &paymentStatus, &paymentIntent, &refundStatus, &subID,
			&clientName, &clientEmail, &treatment, &startsStr)
		if err == sql.ErrNoRows {
			jsonError(w, "booking not found", http.StatusNotFound)
			return
		}
		if err != nil {
			jsonError(w, "server error", http.StatusInternalServerError)
			return
		}
		if status == "cancelled" {
			jsonError(w, "booking is already cancelled", http.StatusConflict)
			return
		}

		starts, _ := time.Parse(time.RFC3339, startsStr)
		if time.Until(starts) <= MinChangeNotice {
			jsonError(w, "this session is within 24 hours — cancellation with refund is not allowed; handle directly with the client", http.StatusConflict)
			return
		}

		// Refund one-off paid bookings (not member/subscription sessions).
		refunded := false
		isMember := subID.Valid
		if !isMember && paymentStatus == "paid" && refundStatus != "refunded" && paymentIntent.Valid && paymentIntent.String != "" {
			if deps.Stripe == nil || !deps.Stripe.Enabled() {
				jsonError(w, "payments not configured — cannot refund", http.StatusServiceUnavailable)
				return
			}
			refundID, rerr := deps.Stripe.Refund(paymentIntent.String)
			if rerr != nil && !errors.Is(rerr, payments.ErrAlreadyRefunded) {
				log.Printf("ERROR refund booking %d: %v", id, rerr)
				jsonError(w, "refund failed — booking not cancelled; check Stripe and retry", http.StatusBadGateway)
				return
			}
			// refundID is empty when the charge was already refunded upstream.
			if _, err := db.Exec(
				`UPDATE bookings SET refund_status = 'refunded', refund_id = ? WHERE id = ?`,
				refundID, id,
			); err != nil {
				log.Printf("CRITICAL refund %s succeeded but DB update failed for booking %d: %v", refundID, id, err)
			}
			refunded = true
		}

		if _, err := db.Exec(`UPDATE bookings SET status = 'cancelled' WHERE id = ?`, id); err != nil {
			jsonError(w, "server error", http.StatusInternalServerError)
			return
		}

		// Notify the client their booking was cancelled (+ refunded if so).
		go func() {
			if err := deps.Mailer.SendBookingCancellation(clientEmail, mailer.BookingCancellationData{
				ClientName: clientName,
				Treatment:  treatment,
				StartsAt:   starts,
				Refunded:   refunded,
				BookURL:    strings.TrimRight(deps.PublicBaseURL, "/") + "/",
			}); err != nil {
				log.Printf("ERROR send cancellation email: %v", err)
			}
		}()

		jsonOK(w, map[string]any{"cancelled": id, "refunded": refunded})
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
		if err := rows.Err(); err != nil {
			log.Printf("ERROR iterate contacts: %v", err)
			jsonError(w, "could not fetch contacts", http.StatusInternalServerError)
			return
		}
		jsonOK(w, result)
	}
}
