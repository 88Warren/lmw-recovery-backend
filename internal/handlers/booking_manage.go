package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"lmwrecovery/backend/internal/mailer"
	"lmwrecovery/backend/internal/models"
)

// MinChangeNotice is how far in advance of a session a client may reschedule or
// request a cancellation. Inside this window they must contact the studio.
const MinChangeNotice = 24 * time.Hour

// manageSession is one bookable session shown on the manage page, each with its
// own reschedule token so a member can reschedule whichever one they choose.
type manageSession struct {
	Treatment   string    `json:"treatment"`
	StartsAt    time.Time `json:"starts_at"`
	Status      string    `json:"status"`
	DurationMin int       `json:"treatment_duration_minutes"`
	Changeable  bool      `json:"changeable"` // > MinChangeNotice away and not cancelled
	Token       string    `json:"token"`      // per-session token for reschedule/cancel
}

// manageView is the client-facing payload for the self-service manage page.
//
// For a one-off booking, Sessions holds the single session and CancelMode is
// "session" (request a cancellation → studio refunds). For a member, Sessions
// holds all their upcoming plan sessions (reschedule any), and CancelMode is
// "subscription" — cancelling means cancelling the whole plan via the portal.
type manageView struct {
	ClientName string          `json:"client_name"`
	IsMember   bool            `json:"is_member"`
	CancelMode string          `json:"cancel_mode"` // "session" | "subscription"
	Sessions   []manageSession `json:"sessions"`
}

// buildManageView verifies a booking token and builds the manage payload.
func buildManageView(db *sql.DB, deps BookingDeps, token string) (manageView, error) {
	if deps.MemberSigner == nil || !deps.MemberSigner.Enabled() {
		return manageView{}, fmt.Errorf("disabled")
	}
	id, err := deps.MemberSigner.VerifyBooking(token)
	if err != nil {
		return manageView{}, err
	}

	var (
		clientName string
		subID      sql.NullInt64
	)
	err = db.QueryRow(`SELECT client_name, subscription_id FROM bookings WHERE id = ?`, id).
		Scan(&clientName, &subID)
	if err != nil {
		return manageView{}, err
	}

	view := manageView{ClientName: clientName, IsMember: subID.Valid}

	// Collect the sessions to show. For a member, show all their active sessions
	// on the same subscription; for a one-off, just this booking.
	var (
		rows *sql.Rows
		qerr error
	)
	if subID.Valid {
		view.CancelMode = "subscription"
		rows, qerr = db.Query(`
			SELECT b.id, b.treatment, b.treatment_duration_minutes, b.status, sl.starts_at
			FROM bookings b JOIN slots sl ON sl.id = b.slot_id
			WHERE b.subscription_id = ? AND b.status != 'cancelled'
			ORDER BY sl.starts_at`, subID.Int64)
	} else {
		view.CancelMode = "session"
		rows, qerr = db.Query(`
			SELECT b.id, b.treatment, b.treatment_duration_minutes, b.status, sl.starts_at
			FROM bookings b JOIN slots sl ON sl.id = b.slot_id
			WHERE b.id = ?`, id)
	}
	if qerr != nil {
		return manageView{}, qerr
	}
	defer rows.Close()

	for rows.Next() {
		var (
			sID       int64
			s         manageSession
			startsStr string
		)
		if err := rows.Scan(&sID, &s.Treatment, &s.DurationMin, &s.Status, &startsStr); err != nil {
			continue
		}
		s.StartsAt, _ = time.Parse(time.RFC3339, startsStr)
		s.Changeable = s.Status != "cancelled" && time.Until(s.StartsAt) > MinChangeNotice
		// Each session carries its own token so reschedule targets the right one.
		tok, terr := deps.MemberSigner.IssueBooking(sID, 24*time.Hour)
		if terr != nil {
			continue
		}
		s.Token = tok
		view.Sessions = append(view.Sessions, s)
	}
	return view, rows.Err()
}

// ManageBookingHandler handles GET /api/bookings/manage?token=…
func ManageBookingHandler(db *sql.DB, deps BookingDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := buildManageView(db, deps, r.URL.Query().Get("token"))
		if err != nil {
			jsonError(w, "this link is invalid or has expired", http.StatusUnauthorized)
			return
		}
		jsonOK(w, v)
	}
}

// RescheduleHandler handles POST /api/bookings/reschedule
// Body: { "token": "...", "new_slot_id": 123 }
//
// Self-service: the client moves their own booking to another open slot,
// provided the CURRENT session is more than MinChangeNotice away. The new slot
// is availability-checked with the same overlap logic as booking. No payment
// changes. The client gets an updated confirmation; the owner is alerted.
func RescheduleHandler(db *sql.DB, deps BookingDeps) http.HandlerFunc {
	type reqBody struct {
		Token     string `json:"token"`
		NewSlotID int64  `json:"new_slot_id"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var req reqBody
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if deps.MemberSigner == nil || !deps.MemberSigner.Enabled() {
			jsonError(w, "rescheduling is not available", http.StatusServiceUnavailable)
			return
		}
		bookingID, verr := deps.MemberSigner.VerifyBooking(req.Token)
		if verr != nil {
			jsonError(w, "this link is invalid or has expired", http.StatusUnauthorized)
			return
		}
		if req.NewSlotID == 0 {
			jsonError(w, "new_slot_id is required", http.StatusBadRequest)
			return
		}

		tx, err := db.Begin()
		if err != nil {
			jsonError(w, "server error", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback() //nolint:errcheck

		// Load the booking + its current start time and duration.
		var (
			b            models.Booking
			curStartsStr string
			oldSlotID    int64
		)
		err = tx.QueryRow(`
			SELECT b.id, b.slot_id, b.treatment, b.treatment_duration_minutes,
			       b.client_name, b.client_email, b.client_phone, b.notes, b.status, sl.starts_at
			FROM bookings b JOIN slots sl ON sl.id = b.slot_id
			WHERE b.id = ?`, bookingID,
		).Scan(&b.ID, &oldSlotID, &b.Treatment, &b.TreatmentDurationMins,
			&b.ClientName, &b.ClientEmail, &b.ClientPhone, &b.Notes, &b.Status, &curStartsStr)
		if err == sql.ErrNoRows {
			jsonError(w, "booking not found", http.StatusNotFound)
			return
		}
		if err != nil {
			jsonError(w, "server error", http.StatusInternalServerError)
			return
		}
		if b.Status == "cancelled" {
			jsonError(w, "this booking has been cancelled", http.StatusConflict)
			return
		}
		curStarts, _ := time.Parse(time.RFC3339, curStartsStr)
		if time.Until(curStarts) <= MinChangeNotice {
			jsonError(w, "changes must be made more than 24 hours before your session — please contact us", http.StatusConflict)
			return
		}

		// Load the target slot.
		var newStartsStr string
		err = tx.QueryRow(`SELECT starts_at FROM slots WHERE id = ?`, req.NewSlotID).Scan(&newStartsStr)
		if err == sql.ErrNoRows {
			jsonError(w, "that time is no longer available", http.StatusNotFound)
			return
		}
		if err != nil {
			jsonError(w, "server error", http.StatusInternalServerError)
			return
		}
		newStarts, _ := time.Parse(time.RFC3339, newStartsStr)
		if time.Until(newStarts) <= MinChangeNotice {
			jsonError(w, "please choose a time more than 24 hours away", http.StatusConflict)
			return
		}

		// Overlap check on the NEW slot, excluding this booking itself.
		endsAt := newStarts.Add(time.Duration(b.TreatmentDurationMins) * time.Minute)
		var conflicts int
		err = tx.QueryRow(`
			SELECT COUNT(*) FROM bookings bk
			JOIN slots s2 ON s2.id = bk.slot_id
			WHERE bk.status IN ('confirmed','pending')
			  AND bk.id != ?
			  AND datetime(s2.starts_at) < datetime(?)
			  AND datetime(?) < datetime(s2.starts_at, '+' || bk.treatment_duration_minutes || ' minutes')`,
			bookingID, endsAt.Format(time.RFC3339), newStarts.Format(time.RFC3339),
		).Scan(&conflicts)
		if err != nil {
			jsonError(w, "server error", http.StatusInternalServerError)
			return
		}
		if conflicts > 0 {
			jsonError(w, "that time is no longer available", http.StatusConflict)
			return
		}

		if _, err := tx.Exec(`UPDATE bookings SET slot_id = ? WHERE id = ?`, req.NewSlotID, bookingID); err != nil {
			jsonError(w, "could not reschedule", http.StatusInternalServerError)
			return
		}
		if err := tx.Commit(); err != nil {
			jsonError(w, "server error", http.StatusInternalServerError)
			return
		}

		// Notify: updated confirmation to client, alert to owner.
		go func(b models.Booking, from, to time.Time) {
			if err := deps.Mailer.SendBookingConfirmation(b.ClientEmail, mailer.BookingConfirmationData{
				ClientName:       b.ClientName,
				Treatment:        b.Treatment + " (rescheduled)",
				StartsAt:         to,
				Notes:            b.Notes,
				StudioAddress:    deps.StudioAddress,
				StudioWhat3Words: deps.StudioWhat3Words,
				// Same booking ID, so the original manage link still works — but
				// include it again so the client can reschedule or cancel from
				// this email too.
				ManageURL: deps.manageURL(b.ID),
			}); err != nil {
				log.Printf("ERROR send reschedule confirmation: %v", err)
			}
			msg := "Booking rescheduled\n" + b.ClientName + " — " + b.Treatment +
				"\nFrom: " + from.In(studioLocation).Format("Mon 2 Jan, 3:04pm") +
				"\nTo:   " + to.In(studioLocation).Format("Mon 2 Jan, 3:04pm")
			deps.notifyOwner(msg)
		}(b, curStarts, newStarts)

		jsonOK(w, map[string]any{"message": "Your session has been moved.", "starts_at": newStarts})
	}
}

// RequestCancellationHandler handles POST /api/bookings/request-cancellation
// Body: { "token": "..." }
//
// The client cannot cancel/refund themselves — this records the request and
// alerts the owner (Telegram + email) so they can action it in the admin area
// within the 24h window. Blocked inside MinChangeNotice (too late to action).
func RequestCancellationHandler(db *sql.DB, deps BookingDeps) http.HandlerFunc {
	type reqBody struct {
		Token  string `json:"token"`
		Reason string `json:"reason"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var req reqBody
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if deps.MemberSigner == nil || !deps.MemberSigner.Enabled() {
			jsonError(w, "this is not available right now", http.StatusServiceUnavailable)
			return
		}
		bookingID, verr := deps.MemberSigner.VerifyBooking(req.Token)
		if verr != nil {
			jsonError(w, "this link is invalid or has expired", http.StatusUnauthorized)
			return
		}

		var (
			b         models.Booking
			startsStr string
		)
		err := db.QueryRow(`
			SELECT b.id, b.treatment, b.client_name, b.client_email, b.client_phone, b.status, sl.starts_at
			FROM bookings b JOIN slots sl ON sl.id = b.slot_id WHERE b.id = ?`, bookingID,
		).Scan(&b.ID, &b.Treatment, &b.ClientName, &b.ClientEmail, &b.ClientPhone, &b.Status, &startsStr)
		if err == sql.ErrNoRows {
			jsonError(w, "booking not found", http.StatusNotFound)
			return
		}
		if err != nil {
			jsonError(w, "server error", http.StatusInternalServerError)
			return
		}
		if b.Status == "cancelled" {
			jsonError(w, "this booking is already cancelled", http.StatusConflict)
			return
		}
		starts, _ := time.Parse(time.RFC3339, startsStr)
		if time.Until(starts) <= MinChangeNotice {
			jsonError(w, "cancellations must be requested more than 24 hours before your session — please contact us directly", http.StatusConflict)
			return
		}

		if _, err := db.Exec(
			`UPDATE bookings SET cancellation_requested_at = ? WHERE id = ?`,
			time.Now().UTC().Format(time.RFC3339), bookingID,
		); err != nil {
			jsonError(w, "server error", http.StatusInternalServerError)
			return
		}

		go func() {
			when := starts.In(studioLocation).Format("Mon 2 Jan, 3:04pm")
			msg := "⚠️ CANCELLATION REQUESTED (action in admin)\n" +
				b.ClientName + " — " + b.Treatment + "\n" + when
			if req.Reason != "" {
				msg += "\nReason: " + req.Reason
			}
			if b.ClientPhone != "" {
				msg += "\n" + b.ClientPhone
			}
			deps.notifyOwner(msg)
			if err := deps.Mailer.SendBookingNotification(deps.NotifyTo, mailer.BookingNotificationData{
				ClientName:  b.ClientName,
				ClientEmail: b.ClientEmail,
				ClientPhone: b.ClientPhone,
				Treatment:   b.Treatment + " — CANCELLATION REQUESTED",
				StartsAt:    starts,
				Notes:       req.Reason,
			}); err != nil {
				log.Printf("ERROR send cancellation-request notification: %v", err)
			}
		}()

		jsonOK(w, map[string]any{"message": "Your cancellation request has been received. We'll confirm by email once it's processed."})
	}
}

// ManagePortalHandler handles POST /api/bookings/manage-portal {"token": "..."}
// For a member booking, returns a Stripe Customer Portal URL where the client
// cancels their whole Maintenance Plan (stops all future billing). Token-based
// so it works straight from the manage page without re-entering the email.
func ManagePortalHandler(db *sql.DB, deps BookingDeps) http.HandlerFunc {
	type reqBody struct {
		Token string `json:"token"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var req reqBody
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if deps.Stripe == nil || !deps.Stripe.Enabled() || deps.MemberSigner == nil || !deps.MemberSigner.Enabled() {
			jsonError(w, "this is not available right now", http.StatusServiceUnavailable)
			return
		}
		bookingID, verr := deps.MemberSigner.VerifyBooking(req.Token)
		if verr != nil {
			jsonError(w, "this link is invalid or has expired", http.StatusUnauthorized)
			return
		}

		// Resolve the Stripe customer via the booking's subscription.
		var customerID string
		err := db.QueryRow(`
			SELECT s.stripe_customer_id
			FROM bookings b JOIN subscriptions s ON s.id = b.subscription_id
			WHERE b.id = ?`, bookingID,
		).Scan(&customerID)
		if err == sql.ErrNoRows || customerID == "" {
			jsonError(w, "this booking is not part of a subscription", http.StatusBadRequest)
			return
		}
		if err != nil {
			jsonError(w, "server error", http.StatusInternalServerError)
			return
		}

		base := strings.TrimRight(deps.PublicBaseURL, "/")
		url, perr := deps.Stripe.CreatePortalSession(customerID, base+"/")
		if perr != nil {
			log.Printf("ERROR create portal session: %v", perr)
			jsonError(w, "could not open the billing portal", http.StatusBadGateway)
			return
		}
		jsonOK(w, map[string]any{"portal_url": url})
	}
}
