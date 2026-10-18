package payments

import (
	"errors"
	"fmt"

	stripe "github.com/stripe/stripe-go/v79"
	"github.com/stripe/stripe-go/v79/refund"
)

// ErrAlreadyRefunded indicates the charge behind the PaymentIntent has already
// been fully refunded. Callers should treat this as success (idempotent).
var ErrAlreadyRefunded = errors.New("charge already refunded")

// Refund issues a full refund for a one-off payment, identified by its
// PaymentIntent ID (stored on the booking as stripe_payment_intent_id). Returns
// the refund ID. If the charge was already fully refunded, it returns
// ErrAlreadyRefunded so the caller can treat the cancellation as successful
// rather than erroring on a retry.
func (s *Service) Refund(paymentIntentID string) (refundID string, err error) {
	if !s.Enabled() {
		return "", ErrDisabled
	}
	if paymentIntentID == "" {
		return "", errors.New("no payment intent to refund")
	}
	r, err := refund.New(&stripe.RefundParams{
		PaymentIntent: stripe.String(paymentIntentID),
	})
	if err != nil {
		// Stripe returns a "charge_already_refunded" error code when the charge
		// has already been fully refunded. Surface that as ErrAlreadyRefunded.
		var serr *stripe.Error
		if errors.As(err, &serr) && serr.Code == stripe.ErrorCodeChargeAlreadyRefunded {
			return "", ErrAlreadyRefunded
		}
		return "", fmt.Errorf("create refund: %w", err)
	}
	return r.ID, nil
}
