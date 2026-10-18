package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"lmwrecovery/backend/internal/mailer"
	"lmwrecovery/backend/internal/memberauth"
	"lmwrecovery/backend/internal/models"
	"lmwrecovery/backend/internal/notify"
	"lmwrecovery/backend/internal/payments"
)

// studioLocation is the timezone booking times are shown in. Slots are stored
// in UTC, so any human-facing time must be converted before formatting.
var studioLocation = func() *time.Location {
	loc, err := time.LoadLocation("Europe/London")
	if err != nil {
		return time.UTC
	}
	return loc
}()

// BookingDeps bundles the shared dependencies the booking handlers need, so the
// handler signatures stay stable as new notification channels or config are
// added.
type BookingDeps struct {
	Mailer          *mailer.Mailer
	Notifier        notify.Notifier // owner booking alerts (Telegram/WhatsApp/…)
	NotifyTo        string          // email address booking alerts are sent to
	BrevoAPIKey     string
	BrevoTemplateID int

	// Studio location details shown in the client confirmation email.
	StudioAddress    string
	StudioWhat3Words string

	// Payments.
	Stripe             *payments.Service
	PublicBaseURL      string // site origin Stripe redirects back to
	MaintenancePriceID string // recurring Price for the Maintenance Plan subscription

	// MemberSigner issues/verifies passwordless magic-link tokens that prove a
	// client controls their email, gating free member bookings.
	MemberSigner *memberauth.Signer
}

// fireBookingConfirmed sends the client confirmation email, owner notification
// email, and owner chat alert for a now-confirmed single booking, and triggers
// the Brevo questionnaire if configured. Safe to run in a goroutine; all
// failures are logged, never fatal.
//
// This is the shared post-confirmation side effect, invoked once a booking
// reaches 'confirmed' — on payment success via the Stripe webhook.
func (d BookingDeps) fireBookingConfirmed(db *sql.DB, b models.Booking, startsAt time.Time) {
	if err := d.Mailer.SendBookingConfirmation(b.ClientEmail, mailer.BookingConfirmationData{
		ClientName:       b.ClientName,
		Treatment:        b.Treatment,
		StartsAt:         startsAt,
		Notes:            b.Notes,
		StudioAddress:    d.StudioAddress,
		StudioWhat3Words: d.StudioWhat3Words,
		ManageURL:        d.manageURL(b.ID),
	}); err != nil {
		log.Printf("ERROR send booking confirmation: %v", err)
	}
	if err := d.Mailer.SendBookingNotification(d.NotifyTo, mailer.BookingNotificationData{
		ClientName:  b.ClientName,
		ClientEmail: b.ClientEmail,
		ClientPhone: b.ClientPhone,
		Treatment:   b.Treatment,
		StartsAt:    startsAt,
		Notes:       b.Notes,
	}); err != nil {
		log.Printf("ERROR send booking notification: %v", err)
	}
	d.notifyOwner(ownerBookingMessage(b.ClientName, b.Treatment, startsAt, b.ClientPhone))

	if d.BrevoAPIKey != "" && d.BrevoTemplateID > 0 {
		if err := triggerBrevoQuestionnaire(d.BrevoAPIKey, d.BrevoTemplateID, b); err != nil {
			log.Printf("ERROR brevo questionnaire: %v", err)
		} else if _, err := db.Exec(`UPDATE bookings SET brevo_sent = 1 WHERE id = ?`, b.ID); err != nil {
			log.Printf("ERROR update brevo_sent: %v", err)
		}
	}
}

// manageURL builds the client self-service link for a booking, or "" if member
// tokens aren't configured. The signed token authorises managing this booking.
func (d BookingDeps) manageURL(bookingID int64) string {
	if d.MemberSigner == nil || !d.MemberSigner.Enabled() {
		return ""
	}
	tok, err := d.MemberSigner.IssueBooking(bookingID, memberauth.BookingTokenTTL)
	if err != nil {
		log.Printf("ERROR issue booking token: %v", err)
		return ""
	}
	base := strings.TrimRight(d.PublicBaseURL, "/")
	return base + "/booking/manage?token=" + tok
}

// notifyOwner sends a best-effort booking alert to the owner over whatever
// channels are configured. Failures are logged, never fatal — email remains the
// primary notification.
func (d BookingDeps) notifyOwner(message string) {
	if d.Notifier == nil || !d.Notifier.Enabled() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := d.Notifier.Send(ctx, message); err != nil {
		log.Printf("ERROR send booking notification (chat): %v", err)
	}
}

// ownerBookingMessage builds the plain-text WhatsApp alert sent to the owner on
// a new booking. Time is rendered in the studio's local timezone.
func ownerBookingMessage(clientName, treatment string, startsAt time.Time, phone string) string {
	when := startsAt.In(studioLocation).Format("Mon 2 Jan, 3:04pm")
	msg := "New LMW Recovery booking\n" +
		clientName + " — " + treatment + "\n" + when
	if phone != "" {
		msg += "\n" + phone
	}
	return msg
}

func jsonOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(data) //nolint:errcheck
}

func jsonError(w http.ResponseWriter, msg string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg}) //nolint:errcheck
}
