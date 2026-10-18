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
//
// FreeMinutes is how many minutes are actually free starting at this slot before
// the next confirmed/pending booking begins (capped at MaxFreeMinutes). A value
// of 0 means the start time itself is already taken. The frontend uses this to
// grey out fully-taken slots and to flag ones with only partial time left (e.g.
// a 30-minute gap where only a short treatment fits), instead of silently
// hiding slots that don't fit a particular duration.
type Slot struct {
	ID          int64     `json:"id"`
	StartsAt    time.Time `json:"starts_at"`
	FreeMinutes int       `json:"free_minutes"`
}

// Booking is a confirmed (or pending) session.
//
// Payment lifecycle (status / payment_status):
//   - A booking is created as status 'pending' with payment_status 'unpaid'
//     when the client starts Stripe Checkout. 'pending' holds the slot (the
//     overlap query counts it), so two people can't check out the same time.
//   - On a successful Stripe webhook it becomes status 'confirmed' /
//     payment_status 'paid', and the confirmation emails + alerts fire.
//   - A pending booking whose checkout is never completed is swept to
//     'cancelled' after a short hold window, releasing the slot.
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

	// Payment fields (one-off Stripe Checkout).
	AmountPence           int        `json:"amount_pence"`
	Currency              string     `json:"currency"`
	PaymentStatus         string     `json:"payment_status"` // unpaid | paid
	StripeSessionID       string     `json:"stripe_session_id,omitempty"`
	StripePaymentIntentID string     `json:"stripe_payment_intent_id,omitempty"`
	PaidAt                *time.Time `json:"paid_at,omitempty"`

	// Populated on reads that JOIN slots
	Slot *Slot `json:"slot,omitempty"`
}

// Subscription mirrors a Stripe Maintenance Plan subscription. Entitlement to
// book member sessions is keyed on the lower-cased client email.
type Subscription struct {
	ID                   int64      `json:"id"`
	ClientEmail          string     `json:"client_email"`
	ClientName           string     `json:"client_name"`
	StripeCustomerID     string     `json:"stripe_customer_id"`
	StripeSubscriptionID string     `json:"stripe_subscription_id"`
	Status               string     `json:"status"`
	CurrentPeriodStart   *time.Time `json:"current_period_start,omitempty"`
	CurrentPeriodEnd     *time.Time `json:"current_period_end,omitempty"`
	CancelAtPeriodEnd    bool       `json:"cancel_at_period_end"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

// SubscriptionActiveStatuses are the Stripe statuses that entitle a client to
// book member sessions.
var SubscriptionActiveStatuses = map[string]bool{
	"active":   true,
	"trialing": true,
}

// Treatment is a single offered service. AmountPence is the canonical price in
// minor units (pence) used for charging — the display Price string is for the
// UI only and is never trusted for payment.
type Treatment struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Duration int    `json:"duration_minutes"`
	Price    string `json:"price"`

	// AmountPence is the authoritative charge amount in pence. Omitted from the
	// public JSON — the amount is resolved server-side from the treatment ID.
	AmountPence int `json:"-"`
}

// Treatments is the canonical list of offered treatments.
var Treatments = []Treatment{
	{ID: "sports-massage", Name: "Sports Massage", Duration: 60, Price: "£45", AmountPence: 4500},
	{ID: "recovery-express", Name: "Recovery Express", Duration: 30, Price: "£30", AmountPence: 3000},
	// Maintenance Plan is a recurring subscription handled separately (not a
	// one-off Checkout). AmountPence left 0 so it can't be charged as a one-off.
	{ID: "maintenance-plan", Name: "Maintenance Plan", Duration: 60, Price: "£80"},
}

// TreatmentByID returns the treatment with the given ID, or false if unknown.
func TreatmentByID(id string) (Treatment, bool) {
	for _, t := range Treatments {
		if t.ID == id {
			return t, true
		}
	}
	return Treatment{}, false
}

// TreatmentByName returns the treatment whose Name matches, or false. Used when
// the client sends the display name rather than the ID.
func TreatmentByName(name string) (Treatment, bool) {
	for _, t := range Treatments {
		if t.Name == name {
			return t, true
		}
	}
	return Treatment{}, false
}
