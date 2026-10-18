// Package payments wraps Stripe one-off Checkout: creating a hosted Checkout
// Session for a booking and verifying the webhook that confirms payment.
//
// Card data never touches this server — the client is redirected to Stripe's
// hosted page. We only create the session and react to the signed webhook.
package payments

import (
	"encoding/json"
	"errors"
	"fmt"

	stripe "github.com/stripe/stripe-go/v79"
	"github.com/stripe/stripe-go/v79/checkout/session"
	"github.com/stripe/stripe-go/v79/webhook"
)

// ErrDisabled is returned by operations when Stripe is not configured.
var ErrDisabled = errors.New("stripe is not configured")

// Service creates Checkout sessions and verifies webhook signatures.
type Service struct {
	secretKey     string
	webhookSecret string
}

// New returns a Stripe service. When secretKey is empty the service is disabled
// and CreateCheckoutSession returns ErrDisabled.
func New(secretKey, webhookSecret string) *Service {
	if secretKey != "" {
		stripe.Key = secretKey
	}
	return &Service{secretKey: secretKey, webhookSecret: webhookSecret}
}

// Enabled reports whether a secret key is configured.
func (s *Service) Enabled() bool { return s.secretKey != "" }

// CheckoutParams describes a one-off Checkout session for a booking.
type CheckoutParams struct {
	BookingID     int64
	AmountPence   int64
	Currency      string // e.g. "gbp"
	ProductName   string // e.g. "Sports Massage"
	Description   string // e.g. "Tuesday 20 October 2026, 6:00pm"
	CustomerEmail string
	SuccessURL    string // Stripe appends ?session_id={CHECKOUT_SESSION_ID}
	CancelURL     string
}

// CreateCheckoutSession creates a hosted Checkout session and returns its ID and
// redirect URL. The booking ID is stored in metadata and client_reference_id so
// the webhook can tie the payment back to the booking.
func (s *Service) CreateCheckoutSession(p CheckoutParams) (sessionID, url string, err error) {
	if !s.Enabled() {
		return "", "", ErrDisabled
	}
	if p.AmountPence <= 0 {
		return "", "", fmt.Errorf("invalid amount %d", p.AmountPence)
	}

	bookingRef := fmt.Sprintf("%d", p.BookingID)
	params := &stripe.CheckoutSessionParams{
		Mode:              stripe.String(string(stripe.CheckoutSessionModePayment)),
		SuccessURL:        stripe.String(p.SuccessURL),
		CancelURL:         stripe.String(p.CancelURL),
		ClientReferenceID: stripe.String(bookingRef),
		CustomerEmail:     stripe.String(p.CustomerEmail),
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{
				Quantity: stripe.Int64(1),
				PriceData: &stripe.CheckoutSessionLineItemPriceDataParams{
					Currency:   stripe.String(p.Currency),
					UnitAmount: stripe.Int64(p.AmountPence),
					ProductData: &stripe.CheckoutSessionLineItemPriceDataProductDataParams{
						Name:        stripe.String(p.ProductName),
						Description: stripe.String(p.Description),
					},
				},
			},
		},
	}
	params.AddMetadata("booking_id", bookingRef)

	sess, err := session.New(params)
	if err != nil {
		return "", "", fmt.Errorf("create checkout session: %w", err)
	}
	return sess.ID, sess.URL, nil
}

// CheckoutResult is the outcome of a verified checkout.session.completed event.
type CheckoutResult struct {
	BookingID       int64
	SessionID       string
	PaymentIntentID string
	AmountPence     int64
	Paid            bool
}

// verify validates the Stripe signature on a raw webhook body and returns the
// parsed event. IgnoreAPIVersionMismatch is set because the Stripe account's
// API version can be newer than the one stripe-go was built against; we only
// read stable fields (IDs, statuses, metadata) that don't change across
// versions, so the mismatch is safe to ignore rather than pin the account.
func (s *Service) verify(payload []byte, sigHeader string) (stripe.Event, error) {
	if s.webhookSecret == "" {
		return stripe.Event{}, errors.New("webhook secret not configured")
	}
	event, err := webhook.ConstructEventWithOptions(payload, sigHeader, s.webhookSecret,
		webhook.ConstructEventOptions{IgnoreAPIVersionMismatch: true})
	if err != nil {
		return stripe.Event{}, fmt.Errorf("verify signature: %w", err)
	}
	return event, nil
}

// WebhookEvent is a classified, verified webhook. Exactly one of OneOff or
// Subscription is set when Kind is the corresponding value.
type WebhookEvent struct {
	Kind         WebhookKind
	OneOff       CheckoutResult             // Kind == WebhookOneOffPaid
	Subscription SubscriptionCheckoutResult // Kind == WebhookSubscriptionStarted
}

// WebhookKind classifies a verified webhook into the actions we take.
type WebhookKind int

const (
	WebhookIgnored             WebhookKind = iota // nothing to do; acknowledge
	WebhookOneOffPaid                             // a one-off booking was paid
	WebhookSubscriptionStarted                    // a Maintenance Plan subscription began
)

// ParseWebhook verifies the signature and classifies the event. For a
// checkout.session.completed it distinguishes a one-off payment (confirm one
// booking) from a subscription signup (start the plan + confirm the booked
// sessions). Subscription lifecycle events are handled via
// ParseSubscriptionEvent instead.
func (s *Service) ParseWebhook(payload []byte, sigHeader string) (WebhookEvent, error) {
	event, err := s.verify(payload, sigHeader)
	if err != nil {
		return WebhookEvent{}, err
	}

	if event.Type != "checkout.session.completed" {
		return WebhookEvent{Kind: WebhookIgnored}, nil
	}

	var sess stripe.CheckoutSession
	if err := json.Unmarshal(event.Data.Raw, &sess); err != nil {
		return WebhookEvent{}, fmt.Errorf("unmarshal session: %w", err)
	}

	// Subscription-mode signup for the Maintenance Plan.
	if sess.Mode == stripe.CheckoutSessionModeSubscription {
		// Only act once the subscription's first invoice is paid.
		if sess.PaymentStatus == stripe.CheckoutSessionPaymentStatusUnpaid {
			return WebhookEvent{Kind: WebhookIgnored}, nil
		}
		sub, perr := parseSubscriptionCheckout(&sess)
		if perr != nil {
			return WebhookEvent{}, perr
		}
		return WebhookEvent{Kind: WebhookSubscriptionStarted, Subscription: sub}, nil
	}

	// One-off payment-mode booking.
	if sess.PaymentStatus != stripe.CheckoutSessionPaymentStatusPaid {
		return WebhookEvent{Kind: WebhookIgnored}, nil
	}
	bookingID, perr := bookingIDFromSession(&sess)
	if perr != nil {
		return WebhookEvent{}, perr
	}
	res := CheckoutResult{
		BookingID:   bookingID,
		SessionID:   sess.ID,
		AmountPence: sess.AmountTotal,
		Paid:        true,
	}
	if sess.PaymentIntent != nil {
		res.PaymentIntentID = sess.PaymentIntent.ID
	}
	return WebhookEvent{Kind: WebhookOneOffPaid, OneOff: res}, nil
}
