package payments

import (
	"fmt"
	"strconv"

	stripe "github.com/stripe/stripe-go/v79"
)

// bookingIDFromSession extracts the booking ID a Checkout session refers to,
// preferring metadata["booking_id"] and falling back to client_reference_id.
func bookingIDFromSession(sess *stripe.CheckoutSession) (int64, error) {
	ref := ""
	if sess.Metadata != nil {
		ref = sess.Metadata["booking_id"]
	}
	if ref == "" {
		ref = sess.ClientReferenceID
	}
	if ref == "" {
		return 0, fmt.Errorf("session %s has no booking reference", sess.ID)
	}
	id, err := strconv.ParseInt(ref, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid booking reference %q: %w", ref, err)
	}
	return id, nil
}
