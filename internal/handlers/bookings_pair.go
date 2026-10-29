package handlers

import (
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

type bookingPairRequest struct {
	SlotID1               int64  `json:"slot_id_1"`
	SlotID2               int64  `json:"slot_id_2"`
	Treatment             string `json:"treatment"`
	TreatmentDurationMins int    `json:"treatment_duration_minutes"`
	ClientName            string `json:"client_name"`
	ClientEmail           string `json:"client_email"`
	ClientPhone           string `json:"client_phone"`
	Notes                 string `json:"notes"`

	// MemberToken is a magic-link token proving the client controls ClientEmail.
	// Required to book member sessions without paying; without a valid token the
	// client is treated as a new signup and sent to subscription Checkout.
	MemberToken string `json:"member_token"`
}

// BookingPairHandler handles POST /api/bookings/pair
// Books two slots atomically for the Maintenance Plan (2 sessions/month).
// Uses the same overlap check as BookingHandler.
func BookingPairHandler(db *sql.DB, deps BookingDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req bookingPairRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		req.ClientName = strings.TrimSpace(req.ClientName)
		req.ClientEmail = strings.TrimSpace(req.ClientEmail)
		req.Treatment = strings.TrimSpace(req.Treatment)

		if req.SlotID1 == 0 || req.SlotID2 == 0 {
			jsonError(w, "slot_id_1 and slot_id_2 are required", http.StatusBadRequest)
			return
		}
		if req.SlotID1 == req.SlotID2 {
			jsonError(w, "both sessions must be different slots", http.StatusBadRequest)
			return
		}
		if req.ClientName == "" || req.ClientEmail == "" {
			jsonError(w, "client_name and client_email are required", http.StatusBadRequest)
			return
		}
		if !strings.Contains(req.ClientEmail, "@") {
			jsonError(w, "invalid email address", http.StatusBadRequest)
			return
		}
		if req.TreatmentDurationMins <= 0 {
			req.TreatmentDurationMins = 60
		}

		// Check membership BEFORE opening the transaction. SQLite runs on a
		// single connection (SetMaxOpenConns(1)); querying via the pool while a
		// transaction holds that one connection would deadlock.
		//
		// A client only gets the free member path if they BOTH have an active
		// subscription AND present a valid magic-link token proving they control
		// this email. Knowing a subscriber's email is not enough — the token
		// must have been delivered to that inbox. Without a valid token we treat
		// them as a new signup (they go to Checkout and pay).
		hasActiveSub, memberSubRowID, _ := activeSubscriptionFor(db, req.ClientEmail)
		tokenValid := false
		if req.MemberToken != "" && deps.MemberSigner != nil && deps.MemberSigner.Enabled() {
			if tokEmail, verr := deps.MemberSigner.Verify(req.MemberToken); verr == nil {
				tokenValid = strings.EqualFold(strings.TrimSpace(tokEmail), req.ClientEmail)
			}
		}
		memberActive := hasActiveSub && tokenValid

		// Hard limit: a member gets MaxSessionsPerPeriod (2) sessions per billing
		// period. This pair books 2 at once, so an existing member must have 0
		// used this period. (A brand-new signup always starts at 0.)
		if memberActive {
			used, cerr := memberSessionsThisPeriod(db, memberSubRowID)
			if cerr != nil {
				log.Printf("ERROR count member sessions: %v", cerr)
				jsonError(w, "server error", http.StatusInternalServerError)
				return
			}
			if used+2 > MaxSessionsPerPeriod {
				remaining := MaxSessionsPerPeriod - used
				if remaining < 0 {
					remaining = 0
				}
				jsonError(w, fmt.Sprintf(
					"your plan covers %d sessions per month and you have %d left this period — please book after your plan renews or contact us",
					MaxSessionsPerPeriod, remaining,
				), http.StatusConflict)
				return
			}
		}

		tx, err := db.Begin()
		if err != nil {
			log.Printf("ERROR begin tx: %v", err)
			jsonError(w, "server error", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback() //nolint:errcheck

		slots := make([]models.Slot, 2)
		for i, slotID := range []int64{req.SlotID1, req.SlotID2} {
			var s models.Slot
			var startsStr string
			var blocked int
			err := tx.QueryRow(`SELECT id, starts_at, blocked FROM slots WHERE id = ?`, slotID).
				Scan(&s.ID, &startsStr, &blocked)
			if err == sql.ErrNoRows {
				jsonError(w, "slot not found", http.StatusNotFound)
				return
			}
			if err != nil {
				log.Printf("ERROR query slot %d: %v", slotID, err)
				jsonError(w, "server error", http.StatusInternalServerError)
				return
			}
			if blocked == 1 {
				jsonError(w, "one or more selected times are no longer available", http.StatusConflict)
				return
			}
			s.StartsAt, _ = time.Parse(time.RFC3339, startsStr)
			slots[i] = s

			endsAt := s.StartsAt.Add(time.Duration(req.TreatmentDurationMins) * time.Minute)

			var conflicts int
			err = tx.QueryRow(`
				SELECT COUNT(*) FROM bookings b
				JOIN slots sl ON sl.id = b.slot_id
				WHERE b.status IN ('confirmed','pending')
				  AND datetime(sl.starts_at) < datetime(?)
				  AND datetime(?) < datetime(sl.starts_at, '+' || b.treatment_duration_minutes || ' minutes')
			`,
				endsAt.Format(time.RFC3339),
				s.StartsAt.Format(time.RFC3339),
			).Scan(&conflicts)
			if err != nil {
				log.Printf("ERROR overlap check slot %d: %v", slotID, err)
				jsonError(w, "server error", http.StatusInternalServerError)
				return
			}
			if conflicts > 0 {
				jsonError(w, "one or more selected times are no longer available", http.StatusConflict)
				return
			}
		}

		// Active member? Their two sessions are covered by the running
		// subscription — confirm now, no new payment. Otherwise they sign up
		// (pay) via subscription Checkout, confirmed by the webhook.
		// (membership was resolved before the transaction opened — see above.)
		insertStatus := "pending"
		var subIDArg any // NULL unless an active member
		if memberActive {
			insertStatus = "confirmed"
			subIDArg = memberSubRowID
		}

		bookings := make([]models.Booking, 2)
		bookingIDs := make([]int64, 2)
		for i, s := range slots {
			var b models.Booking
			row := tx.QueryRow(`
				INSERT INTO bookings
				  (slot_id, treatment, treatment_duration_minutes, client_name, client_email, client_phone, notes,
				   status, amount_pence, currency, payment_status, subscription_id)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, 'gbp', ?, ?)
				RETURNING id, created_at`,
				s.ID, req.Treatment, req.TreatmentDurationMins,
				req.ClientName, req.ClientEmail, req.ClientPhone, req.Notes,
				insertStatus, map[bool]string{true: "paid", false: "unpaid"}[memberActive], subIDArg,
			)
			if err := row.Scan(&b.ID, &b.CreatedAt); err != nil {
				log.Printf("ERROR insert booking %d: %v", i+1, err)
				jsonError(w, "could not save booking", http.StatusInternalServerError)
				return
			}
			b.SlotID = s.ID
			b.Treatment = req.Treatment
			b.TreatmentDurationMins = req.TreatmentDurationMins
			b.ClientName = req.ClientName
			b.ClientEmail = req.ClientEmail
			b.ClientPhone = req.ClientPhone
			b.Notes = req.Notes
			b.Status = insertStatus
			b.Slot = &slots[i]
			bookings[i] = b
			bookingIDs[i] = b.ID
		}

		// New member: create the subscription Checkout session before committing,
		// so a Stripe failure rolls back the held slots.
		var checkoutURL string
		if !memberActive {
			if deps.Stripe == nil || !deps.Stripe.Enabled() || deps.MaintenancePriceID == "" {
				jsonError(w, "subscriptions are not available right now", http.StatusServiceUnavailable)
				return
			}
			base := strings.TrimRight(deps.PublicBaseURL, "/")
			sessionID, url, serr := deps.Stripe.CreateSubscriptionCheckout(payments.SubscriptionCheckoutParams{
				PriceID:       deps.MaintenancePriceID,
				BookingIDs:    bookingIDs,
				CustomerEmail: req.ClientEmail,
				ClientName:    req.ClientName,
				SuccessURL:    base + "/booking/success?session_id={CHECKOUT_SESSION_ID}",
				CancelURL:     base + "/booking/cancelled",
			})
			if serr != nil {
				log.Printf("ERROR create subscription checkout: %v", serr)
				jsonError(w, "could not start subscription", http.StatusBadGateway)
				return
			}
			checkoutURL = url
			for _, id := range bookingIDs {
				if _, err := tx.Exec(`UPDATE bookings SET stripe_session_id = ? WHERE id = ?`, sessionID, id); err != nil {
					log.Printf("ERROR store session id: %v", err)
					jsonError(w, "server error", http.StatusInternalServerError)
					return
				}
			}
		}

		if err := tx.Commit(); err != nil {
			log.Printf("ERROR commit tx: %v", err)
			jsonError(w, "server error", http.StatusInternalServerError)
			return
		}

		// New member pays first — notifications fire from the webhook on success.
		if !memberActive {
			jsonOK(w, map[string]any{
				"message":      "Sessions reserved. Complete subscription to confirm.",
				"booking_ids":  bookingIDs,
				"checkout_url": checkoutURL,
				"subscription": true,
			})
			return
		}

		// Existing member: sessions are confirmed now; fire notifications.
		go func() {
			starts := []time.Time{slots[0].StartsAt, slots[1].StartsAt}
			fireSubscriptionConfirmed(deps, req.ClientName, req.ClientEmail, bookings, starts)
			if deps.BrevoAPIKey != "" && deps.BrevoTemplateID > 0 {
				if err := triggerBrevoQuestionnaire(deps.BrevoAPIKey, deps.BrevoTemplateID, bookings[0]); err != nil {
					log.Printf("ERROR brevo questionnaire: %v", err)
				} else {
					for _, b := range bookings {
						if _, err := db.Exec(`UPDATE bookings SET brevo_sent = 1 WHERE id = ?`, b.ID); err != nil {
							log.Printf("ERROR update brevo_sent: %v", err)
						}
					}
				}
			}
		}()

		jsonOK(w, map[string]any{
			"message":     "Both sessions confirmed. Check your email for details.",
			"booking_ids": []int64{bookings[0].ID, bookings[1].ID},
			"slots":       slots,
		})
	}
}
