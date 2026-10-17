package handlers

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"lmwrecovery/backend/internal/mailer"
	"lmwrecovery/backend/internal/notify"
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
