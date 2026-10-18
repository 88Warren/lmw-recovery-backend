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
	"lmwrecovery/backend/internal/memberauth"
	"lmwrecovery/backend/internal/models"
	"lmwrecovery/backend/internal/payments"
)

// startSubscription handles a completed Maintenance Plan subscription signup:
// it records/updates the subscription, confirms the two booked sessions, links
// them to the subscription, and fires one confirmation. Idempotent — re-running
// for an already-recorded subscription does nothing further.
func startSubscription(db *sql.DB, deps BookingDeps, res payments.SubscriptionCheckoutResult) error {
	// Pull the live subscription state (status, period end) from Stripe.
	info, err := deps.Stripe.FetchSubscription(res.SubscriptionID)
	if err != nil {
		return fmt.Errorf("fetch subscription: %w", err)
	}
	if info.ClientName == "" {
		info.ClientName = res.ClientName
	}
	email := strings.ToLower(strings.TrimSpace(res.CustomerEmail))

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	// Idempotency: if we've already recorded this subscription, assume the
	// signup was fully processed and do nothing (no duplicate emails).
	var existingID int64
	err = tx.QueryRow(`SELECT id FROM subscriptions WHERE stripe_subscription_id = ?`, res.SubscriptionID).Scan(&existingID)
	if err == nil {
		return nil // already processed
	}
	if err != sql.ErrNoRows {
		return err
	}

	periodStart := unixToRFC3339(info.CurrentPeriodStart)
	periodEnd := unixToRFC3339(info.CurrentPeriodEnd)
	cancelAtEnd := boolToInt(info.CancelAtPeriodEnd)

	row := tx.QueryRow(`
		INSERT INTO subscriptions
		  (client_email, client_name, stripe_customer_id, stripe_subscription_id,
		   status, current_period_start, current_period_end, cancel_at_period_end, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))
		RETURNING id`,
		email, info.ClientName, info.CustomerID, res.SubscriptionID,
		info.Status, periodStart, periodEnd, cancelAtEnd,
	)
	var subRowID int64
	if err := row.Scan(&subRowID); err != nil {
		return fmt.Errorf("insert subscription: %w", err)
	}

	// Confirm the two booked sessions and link them to the subscription.
	confirmed := make([]models.Booking, 0, len(res.BookingIDs))
	starts := make([]time.Time, 0, len(res.BookingIDs))
	now := time.Now().UTC().Format(time.RFC3339)
	for _, bid := range res.BookingIDs {
		var (
			b         models.Booking
			startsStr string
			payStatus string
		)
		err := tx.QueryRow(`
			SELECT b.id, b.client_name, b.client_email, b.client_phone, b.treatment, b.notes,
			       b.payment_status, sl.starts_at
			FROM bookings b JOIN slots sl ON sl.id = b.slot_id
			WHERE b.id = ?`, bid,
		).Scan(&b.ID, &b.ClientName, &b.ClientEmail, &b.ClientPhone, &b.Treatment, &b.Notes,
			&payStatus, &startsStr)
		if err == sql.ErrNoRows {
			log.Printf("subscription signup: booking %d not found (ignoring)", bid)
			continue
		}
		if err != nil {
			return err
		}
		if payStatus == "paid" {
			continue // already confirmed
		}
		if _, err := tx.Exec(`
			UPDATE bookings
			SET status = 'confirmed', payment_status = 'paid', paid_at = ?, subscription_id = ?
			WHERE id = ?`, now, subRowID, bid,
		); err != nil {
			return err
		}
		b.Status = "confirmed"
		startsAt, _ := time.Parse(time.RFC3339, startsStr)
		confirmed = append(confirmed, b)
		starts = append(starts, startsAt)
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	// Fire a single confirmation covering both sessions, plus the owner alert.
	go fireSubscriptionConfirmed(deps, info.ClientName, email, confirmed, starts)
	return nil
}

// syncSubscription updates our mirror when Stripe reports a subscription change
// (renewal, cancellation, status change). When the subscription becomes
// cancelled it also releases the member's upcoming (future) sessions and emails
// the client that their plan has ended.
func syncSubscription(db *sql.DB, deps BookingDeps, info payments.SubscriptionInfo) error {
	// Capture prior status + the sub row so we can detect the transition into a
	// cancelled state (and only act once).
	var (
		subRowID   int64
		prevStatus string
		email      string
		name       string
	)
	err := db.QueryRow(
		`SELECT id, status, client_email, client_name FROM subscriptions WHERE stripe_subscription_id = ?`,
		info.SubscriptionID,
	).Scan(&subRowID, &prevStatus, &email, &name)
	if err == sql.ErrNoRows {
		log.Printf("subscription sync: %s not found yet (ignoring)", info.SubscriptionID)
		return nil
	}
	if err != nil {
		return err
	}

	if _, err := db.Exec(`
		UPDATE subscriptions
		SET status = ?, current_period_start = ?, current_period_end = ?,
		    cancel_at_period_end = ?, updated_at = datetime('now')
		WHERE stripe_subscription_id = ?`,
		info.Status, unixToRFC3339(info.CurrentPeriodStart), unixToRFC3339(info.CurrentPeriodEnd),
		boolToInt(info.CancelAtPeriodEnd), info.SubscriptionID,
	); err != nil {
		return err
	}

	// Only act when the subscription has actually ENDED (status canceled/unpaid),
	// and only once (on the transition). Note: "cancel at period end" keeps the
	// status 'active' until the period actually ends — at that point we get a
	// 'canceled' event, so a member who cancels mid-month keeps their access and
	// any sessions booked up to the period end.
	nowEntitles := models.SubscriptionActiveStatuses[info.Status]
	endedNow := !nowEntitles && info.Status != prevStatus &&
		(info.Status == "canceled" || info.Status == "unpaid")
	if !endedNow {
		return nil
	}

	// Release only sessions that fall AFTER the effective end of the plan.
	// Sessions on or before the end date were paid for and are honoured.
	//   - cancel at period end: effective end = current_period_end
	//   - immediate / unpaid:   effective end = now
	effectiveEnd := time.Now().UTC()
	if info.CancelAtPeriodEnd && info.CurrentPeriodEnd > 0 {
		if pe := time.Unix(info.CurrentPeriodEnd, 0).UTC(); pe.After(effectiveEnd) {
			effectiveEnd = pe
		}
	}

	if _, err := db.Exec(`
		UPDATE bookings SET status = 'cancelled'
		WHERE subscription_id = ? AND status != 'cancelled'
		  AND datetime((SELECT starts_at FROM slots WHERE slots.id = bookings.slot_id)) > datetime(?)`,
		subRowID, effectiveEnd.Format(time.RFC3339),
	); err != nil {
		log.Printf("ERROR release sessions for cancelled sub %s: %v", info.SubscriptionID, err)
	}

	if email != "" {
		go func() {
			if err := deps.Mailer.SendBookingCancellation(email, mailer.BookingCancellationData{
				ClientName:     name,
				IsSubscription: true,
				BookURL:        strings.TrimRight(deps.PublicBaseURL, "/") + "/",
			}); err != nil {
				log.Printf("ERROR send subscription-cancellation email: %v", err)
			}
		}()
		deps.notifyOwner("Maintenance Plan CANCELLED by member\n" + name + " (" + email + ")")
	}
	return nil
}

// fireSubscriptionConfirmed emails the client a combined confirmation for both
// sessions and alerts the owner. Best-effort; failures are logged.
func fireSubscriptionConfirmed(deps BookingDeps, clientName, email string, bookings []models.Booking, starts []time.Time) {
	if len(bookings) == 0 {
		return
	}
	// Build a combined "2 sessions" note from the session times.
	var lines []string
	for i, st := range starts {
		lines = append(lines, fmt.Sprintf("Session %d: %s", i+1, st.In(studioLocation).Format("Monday 2 January 2006, 3:04pm")))
	}
	notes := bookings[0].Notes
	if notes != "" {
		notes += "\n"
	}
	notes += strings.Join(lines, "\n")

	if err := deps.Mailer.SendBookingConfirmation(email, mailer.BookingConfirmationData{
		ClientName:       clientName,
		Treatment:        "Maintenance Plan (2 sessions/month)",
		StartsAt:         starts[0],
		Notes:            notes,
		StudioAddress:    deps.StudioAddress,
		StudioWhat3Words: deps.StudioWhat3Words,
		ManageURL:        deps.manageURL(bookings[0].ID),
	}); err != nil {
		log.Printf("ERROR send subscription confirmation: %v", err)
	}

	ownerLines := strings.Join(lines, "\n")
	if err := deps.Mailer.SendBookingNotification(deps.NotifyTo, mailer.BookingNotificationData{
		ClientName:  clientName,
		ClientEmail: email,
		ClientPhone: bookings[0].ClientPhone,
		Treatment:   "Maintenance Plan (subscription)",
		StartsAt:    starts[0],
		Notes:       ownerLines,
	}); err != nil {
		log.Printf("ERROR send subscription notification: %v", err)
	}

	msg := "New LMW Recovery Maintenance Plan subscription\n" + clientName + "\n" + ownerLines
	if bookings[0].ClientPhone != "" {
		msg += "\n" + bookings[0].ClientPhone
	}
	deps.notifyOwner(msg)
}

// RequestMemberLinkHandler handles POST /api/member/request-link {"email":…}.
//
// If the email belongs to an active member, it emails them a short-lived magic
// link that verifies they control the inbox. The response is deliberately the
// same whether or not a subscription exists, so the endpoint can't be used to
// probe who is a member.
func RequestMemberLinkHandler(db *sql.DB, deps BookingDeps) http.HandlerFunc {
	type reqBody struct {
		Email string `json:"email"`
	}
	const sameResponse = "If that email has an active plan, we've sent a sign-in link to it."
	return func(w http.ResponseWriter, r *http.Request) {
		var body reqBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		email := strings.ToLower(strings.TrimSpace(body.Email))
		if email == "" || !strings.Contains(email, "@") {
			jsonError(w, "a valid email is required", http.StatusBadRequest)
			return
		}

		active, _, _ := activeSubscriptionFor(db, email)
		if active && deps.MemberSigner != nil && deps.MemberSigner.Enabled() {
			token, err := deps.MemberSigner.Issue(email, memberauth.DefaultTTL)
			if err == nil {
				base := strings.TrimRight(deps.PublicBaseURL, "/")
				link := base + "/booking/member?token=" + token
				go func() {
					if err := deps.Mailer.SendMemberLink(email, link); err != nil {
						log.Printf("ERROR send member link: %v", err)
					}
				}()
			} else {
				log.Printf("ERROR issue member token: %v", err)
			}
		}
		// Always the same response.
		jsonOK(w, map[string]any{"message": sameResponse})
	}
}

// VerifyMemberHandler handles GET /api/member/verify?token=… — validates a magic
// link token and returns the member's email and current entitlement. The
// frontend stores the token and sends it with the pair booking to book without
// paying.
func VerifyMemberHandler(db *sql.DB, deps BookingDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.MemberSigner == nil || !deps.MemberSigner.Enabled() {
			jsonError(w, "member verification is not available", http.StatusServiceUnavailable)
			return
		}
		token := r.URL.Query().Get("token")
		email, err := deps.MemberSigner.Verify(token)
		if err != nil {
			jsonError(w, "this link is invalid or has expired", http.StatusUnauthorized)
			return
		}
		active, _, _ := activeSubscriptionFor(db, email)
		jsonOK(w, map[string]any{"email": email, "active": active})
	}
}

// SubscriptionStatusHandler handles GET /api/subscriptions/status?email=…
// Returns whether the email has an active Maintenance Plan, so the frontend can
// let a returning member book their next month's sessions without paying again.
func SubscriptionStatusHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		email := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("email")))
		if email == "" || !strings.Contains(email, "@") {
			jsonError(w, "a valid email is required", http.StatusBadRequest)
			return
		}
		active, _, _ := activeSubscriptionFor(db, email)
		jsonOK(w, map[string]any{"active": active})
	}
}

// PortalHandler handles POST /api/subscriptions/portal with a JSON body
// {"email": "..."}. It returns a Stripe Customer Portal URL where the client can
// manage or cancel their Maintenance Plan. Email-based so a client can self-serve
// without logging in; only ever reveals a portal for an email that has a
// subscription on file.
func PortalHandler(db *sql.DB, deps BookingDeps) http.HandlerFunc {
	type portalReq struct {
		Email string `json:"email"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.Stripe == nil || !deps.Stripe.Enabled() {
			jsonError(w, "subscriptions are not available right now", http.StatusServiceUnavailable)
			return
		}
		var req portalReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		email := strings.ToLower(strings.TrimSpace(req.Email))
		if email == "" || !strings.Contains(email, "@") {
			jsonError(w, "a valid email is required", http.StatusBadRequest)
			return
		}

		// Find the customer id for this email (any subscription row, even a
		// cancelled one, so they can still view past billing).
		var customerID string
		err := db.QueryRow(`
			SELECT stripe_customer_id FROM subscriptions
			WHERE client_email = ? ORDER BY updated_at DESC LIMIT 1`, email,
		).Scan(&customerID)
		if err == sql.ErrNoRows || customerID == "" {
			jsonError(w, "no subscription found for that email", http.StatusNotFound)
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
			jsonError(w, "could not open billing portal", http.StatusBadGateway)
			return
		}
		jsonOK(w, map[string]any{"portal_url": url})
	}
}

// MaxSessionsPerPeriod is the hard cap on Maintenance Plan sessions a member may
// book within a single billing period.
const MaxSessionsPerPeriod = 2

// memberSessionsThisPeriod counts a member's non-cancelled sessions linked to
// their subscription within the current billing period. Bookings are counted by
// the slot start time falling inside [current_period_start, current_period_end).
// If the period window isn't known (null), it counts all non-cancelled sessions
// on the subscription as a safe fallback.
func memberSessionsThisPeriod(db *sql.DB, subRowID int64) (int, error) {
	var periodStart, periodEnd sql.NullString
	err := db.QueryRow(
		`SELECT current_period_start, current_period_end FROM subscriptions WHERE id = ?`, subRowID,
	).Scan(&periodStart, &periodEnd)
	if err != nil {
		return 0, err
	}

	if periodStart.Valid && periodEnd.Valid {
		var n int
		err := db.QueryRow(`
			SELECT COUNT(*) FROM bookings b
			JOIN slots sl ON sl.id = b.slot_id
			WHERE b.subscription_id = ?
			  AND b.status != 'cancelled'
			  AND datetime(sl.starts_at) >= datetime(?)
			  AND datetime(sl.starts_at) <  datetime(?)`,
			subRowID, periodStart.String, periodEnd.String,
		).Scan(&n)
		return n, err
	}

	var n int
	err = db.QueryRow(`
		SELECT COUNT(*) FROM bookings
		WHERE subscription_id = ? AND status != 'cancelled'`, subRowID,
	).Scan(&n)
	return n, err
}

// activeSubscriptionFor reports whether the email has an entitling subscription
// and returns its row id and stripe customer id when so.
func activeSubscriptionFor(db *sql.DB, email string) (active bool, rowID int64, customerID string) {
	email = strings.ToLower(strings.TrimSpace(email))
	rows, err := db.Query(`
		SELECT id, status, stripe_customer_id FROM subscriptions
		WHERE client_email = ?`, email)
	if err != nil {
		log.Printf("ERROR query subscriptions: %v", err)
		return false, 0, ""
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id     int64
			status string
			cust   string
		)
		if err := rows.Scan(&id, &status, &cust); err != nil {
			continue
		}
		if models.SubscriptionActiveStatuses[status] {
			return true, id, cust
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("ERROR iterate subscriptions: %v", err)
	}
	return false, 0, ""
}

func unixToRFC3339(sec int64) any {
	if sec <= 0 {
		return nil
	}
	return time.Unix(sec, 0).UTC().Format(time.RFC3339)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
