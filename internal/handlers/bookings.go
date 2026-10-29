package handlers

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"lmwrecovery/backend/internal/models"
	"lmwrecovery/backend/internal/payments"
)

type bookingRequest struct {
	SlotID                int64  `json:"slot_id"`
	Treatment             string `json:"treatment"`
	TreatmentDurationMins int    `json:"treatment_duration_minutes"`
	ClientName            string `json:"client_name"`
	ClientEmail           string `json:"client_email"`
	ClientPhone           string `json:"client_phone"`
	Notes                 string `json:"notes"`
}

// BookingHandler handles POST /api/bookings.
//
// Availability is determined by an overlap query rather than an `available` flag:
//
//	A slot S with treatment duration D is free if no confirmed/pending booking B exists where
//	B.starts_at < S.starts_at + D  AND  S.starts_at < B.starts_at + B.treatment_duration_minutes
func BookingHandler(db *sql.DB, deps BookingDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req bookingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		req.ClientName = strings.TrimSpace(req.ClientName)
		req.ClientEmail = strings.TrimSpace(req.ClientEmail)
		req.Treatment = strings.TrimSpace(req.Treatment)

		if req.SlotID == 0 || req.Treatment == "" || req.ClientName == "" || req.ClientEmail == "" {
			jsonError(w, "slot_id, treatment, client_name and client_email are required", http.StatusBadRequest)
			return
		}
		if req.TreatmentDurationMins <= 0 {
			req.TreatmentDurationMins = 60 // safe default
		}
		if !strings.Contains(req.ClientEmail, "@") {
			jsonError(w, "invalid email address", http.StatusBadRequest)
			return
		}

		tx, err := db.Begin()
		if err != nil {
			log.Printf("ERROR begin tx: %v", err)
			jsonError(w, "server error", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback() //nolint:errcheck

		// Fetch the slot
		var slot models.Slot
		var startsStr string
		var blocked int
		err = tx.QueryRow(`SELECT id, starts_at, blocked FROM slots WHERE id = ?`, req.SlotID).
			Scan(&slot.ID, &startsStr, &blocked)
		if err == sql.ErrNoRows {
			jsonError(w, "slot not found", http.StatusNotFound)
			return
		}
		if err != nil {
			log.Printf("ERROR query slot: %v", err)
			jsonError(w, "server error", http.StatusInternalServerError)
			return
		}
		if blocked == 1 {
			jsonError(w, "this time is no longer available", http.StatusConflict)
			return
		}
		slot.StartsAt, _ = time.Parse(time.RFC3339, startsStr)
		endsAt := slot.StartsAt.Add(time.Duration(req.TreatmentDurationMins) * time.Minute)

		// Overlap check — same logic as SlotsHandler
		var conflicts int
		err = tx.QueryRow(`
			SELECT COUNT(*) FROM bookings b
			JOIN slots sl ON sl.id = b.slot_id
			WHERE b.status IN ('confirmed','pending')
			  AND datetime(sl.starts_at) < datetime(?)
			  AND datetime(?) < datetime(sl.starts_at, '+' || b.treatment_duration_minutes || ' minutes')
		`,
			endsAt.Format(time.RFC3339),
			slot.StartsAt.Format(time.RFC3339),
		).Scan(&conflicts)
		if err != nil {
			log.Printf("ERROR overlap check: %v", err)
			jsonError(w, "server error", http.StatusInternalServerError)
			return
		}
		if conflicts > 0 {
			jsonError(w, "this time is no longer available", http.StatusConflict)
			return
		}

		// Resolve the canonical price server-side from the treatment — never
		// trust a client-sent amount. Match by name (what the client sends) and
		// fall back to duration-based lookup is not needed since names are fixed.
		treatment, ok := models.TreatmentByName(req.Treatment)
		if !ok {
			jsonError(w, "unknown treatment", http.StatusBadRequest)
			return
		}
		if treatment.AmountPence <= 0 {
			// e.g. the Maintenance Plan, which is a subscription, not one-off.
			jsonError(w, "this treatment cannot be booked as a one-off payment", http.StatusBadRequest)
			return
		}

		// Insert as pending/unpaid. 'pending' holds the slot via the overlap
		// query so nobody else can check out the same time while this client
		// pays. It becomes 'confirmed'/'paid' on the Stripe webhook.
		var booking models.Booking
		row := tx.QueryRow(`
			INSERT INTO bookings
			  (slot_id, treatment, treatment_duration_minutes, client_name, client_email, client_phone, notes,
			   status, amount_pence, currency, payment_status)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?, 'gbp', 'unpaid')
			RETURNING id, created_at`,
			slot.ID, req.Treatment, req.TreatmentDurationMins,
			req.ClientName, req.ClientEmail, req.ClientPhone, req.Notes,
			treatment.AmountPence,
		)
		if err := row.Scan(&booking.ID, &booking.CreatedAt); err != nil {
			log.Printf("ERROR insert booking: %v", err)
			jsonError(w, "could not save booking", http.StatusInternalServerError)
			return
		}

		// Create the Stripe Checkout session for this booking.
		if deps.Stripe == nil || !deps.Stripe.Enabled() {
			log.Printf("ERROR stripe not configured")
			jsonError(w, "payments are not available right now", http.StatusServiceUnavailable)
			return
		}
		base := strings.TrimRight(deps.PublicBaseURL, "/")
		sessionID, checkoutURL, err := deps.Stripe.CreateCheckoutSession(payments.CheckoutParams{
			BookingID:     booking.ID,
			AmountPence:   int64(treatment.AmountPence),
			Currency:      "gbp",
			ProductName:   treatment.Name,
			Description:   slot.StartsAt.In(studioLocation).Format("Monday 2 January 2006, 3:04pm"),
			CustomerEmail: req.ClientEmail,
			SuccessURL:    base + "/booking/success?session_id={CHECKOUT_SESSION_ID}",
			CancelURL:     base + "/booking/cancelled",
		})
		if err != nil {
			log.Printf("ERROR create checkout session: %v", err)
			jsonError(w, "could not start payment", http.StatusBadGateway)
			return
		}

		if _, err := tx.Exec(`UPDATE bookings SET stripe_session_id = ? WHERE id = ?`, sessionID, booking.ID); err != nil {
			log.Printf("ERROR store session id: %v", err)
			jsonError(w, "server error", http.StatusInternalServerError)
			return
		}

		if err := tx.Commit(); err != nil {
			log.Printf("ERROR commit tx: %v", err)
			jsonError(w, "server error", http.StatusInternalServerError)
			return
		}

		// No emails yet — those fire from the webhook once payment succeeds.
		jsonOK(w, map[string]any{
			"message":      "Booking reserved. Complete payment to confirm.",
			"booking_id":   booking.ID,
			"checkout_url": checkoutURL,
		})
	}
}

// ── Brevo helpers (shared with bookings_pair.go) ──────────────────────────

func triggerBrevoQuestionnaire(apiKey string, templateID int, b models.Booking) error {
	contactPayload := map[string]any{
		"email": b.ClientEmail,
		"attributes": map[string]any{
			"FIRSTNAME": firstWord(b.ClientName),
			"LASTNAME":  lastWords(b.ClientName),
		},
		"updateEnabled": true,
	}
	if err := brevoPost(apiKey, "https://api.brevo.com/v3/contacts", contactPayload); err != nil {
		return fmt.Errorf("brevo upsert contact: %w", err)
	}

	emailPayload := map[string]any{
		"templateId": templateID,
		"to":         []map[string]string{{"email": b.ClientEmail, "name": b.ClientName}},
		"params":     map[string]any{"CLIENT_NAME": b.ClientName, "TREATMENT": b.Treatment},
	}
	if err := brevoPost(apiKey, "https://api.brevo.com/v3/smtp/email", emailPayload); err != nil {
		return fmt.Errorf("brevo send template: %w", err)
	}
	return nil
}

func brevoPost(apiKey, url string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("api-key", apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("brevo returned status %d", resp.StatusCode)
	}
	return nil
}

func firstWord(s string) string {
	parts := strings.Fields(s)
	if len(parts) == 0 {
		return s
	}
	return parts[0]
}

func lastWords(s string) string {
	parts := strings.Fields(s)
	if len(parts) <= 1 {
		return ""
	}
	return strings.Join(parts[1:], " ")
}
