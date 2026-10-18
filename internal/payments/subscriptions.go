package payments

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	stripe "github.com/stripe/stripe-go/v79"
	bpsession "github.com/stripe/stripe-go/v79/billingportal/session"
	"github.com/stripe/stripe-go/v79/checkout/session"
	"github.com/stripe/stripe-go/v79/subscription"
)

// SubscriptionCheckoutParams describes a subscription-mode Checkout session for
// the Maintenance Plan. BookingIDs are the two sessions booked for the first
// month; they are stored in metadata so the webhook can confirm them on payment.
type SubscriptionCheckoutParams struct {
	PriceID       string
	BookingIDs    []int64
	CustomerEmail string
	ClientName    string
	SuccessURL    string
	CancelURL     string
}

// CreateSubscriptionCheckout creates a subscription-mode Checkout session for
// the Maintenance Plan and returns its ID and redirect URL. The booking IDs are
// stored in session metadata (comma-separated) so the webhook can confirm them.
func (s *Service) CreateSubscriptionCheckout(p SubscriptionCheckoutParams) (sessionID, url string, err error) {
	if !s.Enabled() {
		return "", "", ErrDisabled
	}
	if p.PriceID == "" {
		return "", "", fmt.Errorf("maintenance price id not configured")
	}

	params := &stripe.CheckoutSessionParams{
		Mode:          stripe.String(string(stripe.CheckoutSessionModeSubscription)),
		SuccessURL:    stripe.String(p.SuccessURL),
		CancelURL:     stripe.String(p.CancelURL),
		CustomerEmail: stripe.String(p.CustomerEmail),
		// Show Stripe's hosted "Add promotion code" field. Promotion codes
		// (incl. ones that apply to the recurring plan) are created in the
		// Stripe dashboard.
		AllowPromotionCodes: stripe.Bool(true),
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{Price: stripe.String(p.PriceID), Quantity: stripe.Int64(1)},
		},
	}
	params.AddMetadata("booking_ids", joinIDs(p.BookingIDs))
	params.AddMetadata("client_name", p.ClientName)
	// Also stamp the subscription itself so subscription-level webhooks
	// (renewals, cancellations) can be tied back to the client.
	params.SubscriptionData = &stripe.CheckoutSessionSubscriptionDataParams{}
	params.SubscriptionData.AddMetadata("client_name", p.ClientName)

	sess, err := session.New(params)
	if err != nil {
		return "", "", fmt.Errorf("create subscription checkout: %w", err)
	}
	return sess.ID, sess.URL, nil
}

// CreatePortalSession creates a Stripe Customer Portal session so the client can
// manage or cancel their subscription. Returns the redirect URL.
func (s *Service) CreatePortalSession(customerID, returnURL string) (string, error) {
	if !s.Enabled() {
		return "", ErrDisabled
	}
	if customerID == "" {
		return "", fmt.Errorf("customer id required")
	}
	ps, err := bpsession.New(&stripe.BillingPortalSessionParams{
		Customer:  stripe.String(customerID),
		ReturnURL: stripe.String(returnURL),
	})
	if err != nil {
		return "", fmt.Errorf("create portal session: %w", err)
	}
	return ps.URL, nil
}

// SubscriptionInfo is a snapshot of a subscription's state used to sync our DB.
type SubscriptionInfo struct {
	SubscriptionID     string
	CustomerID         string
	Status             string
	CurrentPeriodStart int64 // unix seconds
	CurrentPeriodEnd   int64 // unix seconds
	CancelAtPeriodEnd  bool
	ClientName         string // from subscription metadata, if present
}

// FetchSubscription loads the current state of a subscription from Stripe. Used
// after a checkout completes (the session only references the subscription ID).
func (s *Service) FetchSubscription(subscriptionID string) (SubscriptionInfo, error) {
	if !s.Enabled() {
		return SubscriptionInfo{}, ErrDisabled
	}
	sub, err := subscription.Get(subscriptionID, nil)
	if err != nil {
		return SubscriptionInfo{}, fmt.Errorf("get subscription: %w", err)
	}
	return subInfoFrom(sub), nil
}

func subInfoFrom(sub *stripe.Subscription) SubscriptionInfo {
	info := SubscriptionInfo{
		SubscriptionID:     sub.ID,
		Status:             string(sub.Status),
		CurrentPeriodStart: sub.CurrentPeriodStart,
		CurrentPeriodEnd:   sub.CurrentPeriodEnd,
		CancelAtPeriodEnd:  sub.CancelAtPeriodEnd,
	}
	if sub.Customer != nil {
		info.CustomerID = sub.Customer.ID
	}
	if sub.Metadata != nil {
		info.ClientName = sub.Metadata["client_name"]
	}
	return info
}

// SubscriptionCheckoutResult is returned for a completed subscription Checkout.
type SubscriptionCheckoutResult struct {
	BookingIDs     []int64
	CustomerID     string
	CustomerEmail  string
	SubscriptionID string
	ClientName     string
}

// parseSubscriptionCheckout extracts subscription-signup details from a
// completed checkout.session.completed event whose mode is "subscription".
func parseSubscriptionCheckout(sess *stripe.CheckoutSession) (SubscriptionCheckoutResult, error) {
	res := SubscriptionCheckoutResult{}
	if sess.Subscription != nil {
		res.SubscriptionID = sess.Subscription.ID
	}
	if sess.Customer != nil {
		res.CustomerID = sess.Customer.ID
	}
	if sess.CustomerDetails != nil {
		res.CustomerEmail = sess.CustomerDetails.Email
	}
	if res.CustomerEmail == "" {
		res.CustomerEmail = sess.CustomerEmail
	}
	if sess.Metadata != nil {
		res.ClientName = sess.Metadata["client_name"]
		res.BookingIDs = parseIDs(sess.Metadata["booking_ids"])
	}
	if res.SubscriptionID == "" {
		return res, fmt.Errorf("subscription session %s has no subscription id", sess.ID)
	}
	return res, nil
}

// ParseSubscriptionEvent verifies a webhook and, for subscription lifecycle
// events (customer.subscription.updated/deleted), returns the new state so the
// DB can be kept in sync. ok is false for events this does not handle.
func (s *Service) ParseSubscriptionEvent(payload []byte, sigHeader string) (info SubscriptionInfo, ok bool, err error) {
	event, verr := s.verify(payload, sigHeader)
	if verr != nil {
		return SubscriptionInfo{}, false, verr
	}
	switch event.Type {
	case "customer.subscription.updated", "customer.subscription.deleted", "customer.subscription.created":
		var sub stripe.Subscription
		if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
			return SubscriptionInfo{}, false, fmt.Errorf("unmarshal subscription: %w", err)
		}
		return subInfoFrom(&sub), true, nil
	default:
		return SubscriptionInfo{}, false, nil
	}
}

func joinIDs(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ",")
}

func parseIDs(s string) []int64 {
	if s == "" {
		return nil
	}
	var out []int64
	for _, p := range strings.Split(s, ",") {
		if id, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64); err == nil {
			out = append(out, id)
		}
	}
	return out
}
