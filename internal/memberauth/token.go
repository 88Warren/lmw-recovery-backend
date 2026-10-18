// Package memberauth issues and verifies passwordless "magic link" tokens that
// prove a client controls a given email address. Tokens are stateless: a token
// is email + expiry, HMAC-signed with a server secret, so nothing is stored and
// no passwords exist. Used to gate free Maintenance Plan member bookings so that
// merely knowing a subscriber's email is not enough to book on their plan.
package memberauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// DefaultTTL is how long a magic link remains valid after issue.
const DefaultTTL = 30 * time.Minute

// Signer issues and verifies member tokens with a shared secret key.
type Signer struct {
	key []byte
}

// New returns a Signer. An empty key disables issuing/verifying (Enabled=false).
func New(secret string) *Signer {
	return &Signer{key: []byte(secret)}
}

// Enabled reports whether a signing secret is configured.
func (s *Signer) Enabled() bool { return len(s.key) > 0 }

// Issue creates a token for email valid for ttl. Format (all base64url):
//
//	<payload>.<sig>   where payload = base64("<lower-email>|<expiryUnix>")
func (s *Signer) Issue(email string, ttl time.Duration) (string, error) {
	if !s.Enabled() {
		return "", errors.New("member auth not configured")
	}
	email = strings.ToLower(strings.TrimSpace(email))
	exp := time.Now().Add(ttl).Unix()
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%s|%d", email, exp)))
	return payload + "." + s.sign(payload), nil
}

// Verify checks a token's signature and expiry and returns the email it is for.
func (s *Signer) Verify(token string) (email string, err error) {
	if !s.Enabled() {
		return "", errors.New("member auth not configured")
	}
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return "", errors.New("malformed token")
	}
	payload, sig := parts[0], parts[1]

	// Constant-time signature comparison.
	if !hmac.Equal([]byte(sig), []byte(s.sign(payload))) {
		return "", errors.New("invalid token signature")
	}

	raw, derr := base64.RawURLEncoding.DecodeString(payload)
	if derr != nil {
		return "", errors.New("malformed token payload")
	}
	fields := strings.SplitN(string(raw), "|", 2)
	if len(fields) != 2 {
		return "", errors.New("malformed token payload")
	}
	exp, perr := strconv.ParseInt(fields[1], 10, 64)
	if perr != nil {
		return "", errors.New("malformed token expiry")
	}
	if time.Now().Unix() > exp {
		return "", errors.New("token expired")
	}
	return fields[0], nil
}

func (s *Signer) sign(payload string) string {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
