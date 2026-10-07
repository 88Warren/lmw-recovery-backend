package models

import "time"

// Contact represents a message submitted via the contact form.
type Contact struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Phone     string    `json:"phone,omitempty"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

// Slot is a single bookable start time. Availability for a given treatment
// duration is checked via overlap query — there is no ends_at or available flag.
type Slot struct {
	ID       int64     `json:"id"`
	StartsAt time.Time `json:"starts_at"`
}

// Booking is a confirmed (or pending) session.
type Booking struct {
	ID                    int64     `json:"id"`
	SlotID                int64     `json:"slot_id"`
	Treatment             string    `json:"treatment"`
	TreatmentDurationMins int       `json:"treatment_duration_minutes"`
	ClientName            string    `json:"client_name"`
	ClientEmail           string    `json:"client_email"`
	ClientPhone           string    `json:"client_phone,omitempty"`
	Notes                 string    `json:"notes,omitempty"`
	Status                string    `json:"status"` // pending | confirmed | cancelled
	BrevoSent             bool      `json:"brevo_sent"`
	CreatedAt             time.Time `json:"created_at"`

	// Populated on reads that JOIN slots
	Slot *Slot `json:"slot,omitempty"`
}

// Treatments is the canonical list of offered treatments.
var Treatments = []struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Duration int    `json:"duration_minutes"`
	Price    string `json:"price"`
}{
	{ID: "sports-massage", Name: "Sports Massage", Duration: 60, Price: "£45"},
	{ID: "recovery-express", Name: "Recovery Express", Duration: 30, Price: "£30"},
	{ID: "maintenance-plan", Name: "Maintenance Plan", Duration: 60, Price: "£80"},
}
