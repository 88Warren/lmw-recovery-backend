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

// ScreeningTokenTTL is how long a screening-form link stays valid. It lives in
// the client's confirmation email and should remain usable right up to their
// first session, so it is generous.
const ScreeningTokenTTL = 90 * 24 * time.Hour

// IssueScreening creates a token that authorises completing the screening form
// for a specific booking. Format: "s:<payload>.<sig>" where
// payload = base64("<bookingID>|<expiryUnix>"). The "s:" prefix keeps it
// distinct from member ("" ) and booking ("b:") tokens so they cannot be used
// interchangeably.
func (s *Signer) IssueScreening(bookingID int64, ttl time.Duration) (string, error) {
	if !s.Enabled() {
		return "", errors.New("auth not configured")
	}
	exp := time.Now().Add(ttl).Unix()
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d|%d", bookingID, exp)))
	return "s:" + payload + "." + s.sign("s:"+payload), nil
}

// VerifyScreening checks a screening token and returns the booking ID it
// authorises.
func (s *Signer) VerifyScreening(token string) (bookingID int64, err error) {
	if !s.Enabled() {
		return 0, errors.New("auth not configured")
	}
	if !strings.HasPrefix(token, "s:") {
		return 0, errors.New("not a screening token")
	}
	body := strings.TrimPrefix(token, "s:")
	parts := strings.SplitN(body, ".", 2)
	if len(parts) != 2 {
		return 0, errors.New("malformed token")
	}
	payload, sig := parts[0], parts[1]
	if !hmac.Equal([]byte(sig), []byte(s.sign("s:"+payload))) {
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
