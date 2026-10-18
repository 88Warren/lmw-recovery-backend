package memberauth

import (
	"crypto/hmac"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// BookingTokenTTL is how long a booking-management link stays valid. It is long
// because the link lives in the client's confirmation email and should work
// right up until (and a little past) the session; the 24h action rule is
// enforced separately when they actually reschedule/cancel.
const BookingTokenTTL = 90 * 24 * time.Hour

// IssueBooking creates a token that proves the holder received the confirmation
// email for a specific booking. Format: "b:<payload>.<sig>" where
// payload = base64("<bookingID>|<expiryUnix>"). The "b:" prefix distinguishes
// it from member (email) tokens so the two can't be used interchangeably.
func (s *Signer) IssueBooking(bookingID int64, ttl time.Duration) (string, error) {
	if !s.Enabled() {
		return "", errors.New("auth not configured")
	}
	exp := time.Now().Add(ttl).Unix()
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d|%d", bookingID, exp)))
	return "b:" + payload + "." + s.sign("b:"+payload), nil
}

// VerifyBooking checks a booking token and returns the booking ID it authorises.
func (s *Signer) VerifyBooking(token string) (bookingID int64, err error) {
	if !s.Enabled() {
		return 0, errors.New("auth not configured")
	}
	if !strings.HasPrefix(token, "b:") {
		return 0, errors.New("not a booking token")
	}
	body := strings.TrimPrefix(token, "b:")
	parts := strings.SplitN(body, ".", 2)
	if len(parts) != 2 {
		return 0, errors.New("malformed token")
	}
	payload, sig := parts[0], parts[1]
	if !hmac.Equal([]byte(sig), []byte(s.sign("b:"+payload))) {
		return 0, errors.New("invalid token signature")
	}
	raw, derr := base64.RawURLEncoding.DecodeString(payload)
	if derr != nil {
		return 0, errors.New("malformed token payload")
	}
	fields := strings.SplitN(string(raw), "|", 2)
	if len(fields) != 2 {
		return 0, errors.New("malformed token payload")
	}
	exp, perr := strconv.ParseInt(fields[1], 10, 64)
	if perr != nil {
		return 0, errors.New("malformed token expiry")
	}
	if time.Now().Unix() > exp {
		return 0, errors.New("token expired")
	}
	id, ierr := strconv.ParseInt(fields[0], 10, 64)
	if ierr != nil {
		return 0, errors.New("malformed booking id")
	}
	return id, nil
}
