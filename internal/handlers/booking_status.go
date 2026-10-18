package handlers

import (
	"database/sql"
	"net/http"
)

// BookingStatusHandler handles GET /api/bookings/status?session_id=cs_...
//
// The Stripe success redirect lands the client on a page that calls this to
// show whether payment has confirmed yet. Because the webhook may arrive a
// moment after the redirect, the client can poll this briefly. Only
// non-sensitive status fields are returned, looked up by the Checkout session
// ID (which the client already holds from the redirect).
func BookingStatusHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID := r.URL.Query().Get("session_id")
		if sessionID == "" {
			jsonError(w, "session_id is required", http.StatusBadRequest)
			return
		}

		var (
			status        string
			paymentStatus string
			treatment     string
			clientName    string
		)
		err := db.QueryRow(`
			SELECT status, payment_status, treatment, client_name
			FROM bookings WHERE stripe_session_id = ?`, sessionID,
		).Scan(&status, &paymentStatus, &treatment, &clientName)
		if err == sql.ErrNoRows {
			jsonError(w, "booking not found", http.StatusNotFound)
			return
		}
		if err != nil {
			jsonError(w, "server error", http.StatusInternalServerError)
			return
		}

		jsonOK(w, map[string]any{
			"status":         status,
			"payment_status": paymentStatus,
			"treatment":      treatment,
			"client_name":    clientName,
		})
	}
}
