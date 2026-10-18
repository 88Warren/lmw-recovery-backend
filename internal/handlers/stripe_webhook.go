package handlers

import (
	"database/sql"
	"io"
	"log"
	"net/http"
	"time"

	"lmwrecovery/backend/internal/models"
	"lmwrecovery/backend/internal/payments"
)

// maxWebhookBody caps the webhook body we read (Stripe events are small).
const maxWebhookBody = 1 << 20 // 1 MiB

// StripeWebhookHandler handles POST /api/stripe/webhook.
//
// It verifies the Stripe signature on the RAW request body (which is why this
// route must not pass through any JSON body parsing) and, on a paid
// checkout.session.completed event, flips the matching booking to
// confirmed/paid and fires the confirmation emails + owner alert exactly once.
//
// It always responds 200 for events it understands or ignores, so Stripe does
// not retry; only signature/verification failures return 400.
func StripeWebhookHandler(db *sql.DB, deps BookingDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.Stripe == nil || !deps.Stripe.Enabled() {
			http.Error(w, "payments not configured", http.StatusServiceUnavailable)
			return
		}

		payload, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody))
		if err != nil {
			http.Error(w, "could not read body", http.StatusBadRequest)
			return
		}

		sig := r.Header.Get("Stripe-Signature")

		// checkout.session.completed → one-off paid or subscription signup.
		evt, err := deps.Stripe.ParseWebhook(payload, sig)
		if err != nil {
			log.Printf("ERROR stripe webhook verify: %v", err)
			http.Error(w, "invalid signature", http.StatusBadRequest)
			return
		}

		switch evt.Kind {
		case payments.WebhookOneOffPaid:
			if err := confirmPaidBooking(db, deps, evt.OneOff); err != nil {
				log.Printf("ERROR confirm paid booking %d: %v", evt.OneOff.BookingID, err)
				http.Error(w, "could not confirm booking", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
			return

		case payments.WebhookSubscriptionStarted:
			if err := startSubscription(db, deps, evt.Subscription); err != nil {
				log.Printf("ERROR start subscription %s: %v", evt.Subscription.SubscriptionID, err)
				http.Error(w, "could not start subscription", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}

		// Not a checkout event we act on. Try subscription-lifecycle events
		// (renewal/cancellation) so our mirror stays in sync.
		info, ok, serr := deps.Stripe.ParseSubscriptionEvent(payload, sig)
		if serr != nil {
			log.Printf("ERROR stripe webhook verify (sub): %v", serr)
			http.Error(w, "invalid signature", http.StatusBadRequest)
			return
		}
		if ok {
			if err := syncSubscription(db, deps, info); err != nil {
				log.Printf("ERROR sync subscription %s: %v", info.SubscriptionID, err)
				http.Error(w, "could not sync subscription", http.StatusInternalServerError)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	}
}

// confirmPaidBooking marks a booking confirmed/paid and fires notifications.
// It is idempotent: Stripe may deliver the same event more than once, so if the
// booking is already paid we do nothing (and send no duplicate emails).
func confirmPaidBooking(db *sql.DB, deps BookingDeps, res payments.CheckoutResult) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	// Lock-in via status check: only transition a still-unpaid booking.
	var (
		b         models.Booking
		startsStr string
		payStatus string
	)
	err = tx.QueryRow(`
		SELECT b.id, b.slot_id, b.treatment, b.treatment_duration_minutes,
		       b.client_name, b.client_email, b.client_phone, b.notes,
		       b.payment_status, sl.starts_at
		FROM bookings b JOIN slots sl ON sl.id = b.slot_id
		WHERE b.id = ?`, res.BookingID,
	).Scan(&b.ID, &b.SlotID, &b.Treatment, &b.TreatmentDurationMins,
		&b.ClientName, &b.ClientEmail, &b.ClientPhone, &b.Notes,
		&payStatus, &startsStr)
	if err == sql.ErrNoRows {
		log.Printf("stripe webhook: booking %d not found (ignoring)", res.BookingID)
		return nil // nothing to do; acknowledge
	}
	if err != nil {
		return err
	}

	if payStatus == "paid" {
		return nil // already processed — idempotent no-op, no duplicate emails
	}

	now := time.Now().UTC().Format(time.RFC3339)
	// Record the actual amount paid (reflects any promotion code applied at
	// Checkout), not the list price.
	if _, err := tx.Exec(`
		UPDATE bookings
		SET status = 'confirmed', payment_status = 'paid',
		    stripe_payment_intent_id = ?, amount_pence = ?, paid_at = ?
		WHERE id = ?`,
		res.PaymentIntentID, res.AmountPence, now, res.BookingID,
	); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	startsAt, _ := time.Parse(time.RFC3339, startsStr)
	b.Status = "confirmed"
	go deps.fireBookingConfirmed(db, b, startsAt)
	return nil
}
