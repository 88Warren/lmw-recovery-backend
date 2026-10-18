package memberauth

import (
	"testing"
	"time"
)

func TestIssueAndVerifyRoundTrip(t *testing.T) {
	s := New("test-secret-key")
	tok, err := s.Issue("Member@Example.com", DefaultTTL)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	email, err := s.Verify(tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if email != "member@example.com" { // normalised to lower-case
		t.Errorf("email = %q, want member@example.com", email)
	}
}

func TestVerifyRejectsTampering(t *testing.T) {
	s := New("test-secret-key")
	tok, _ := s.Issue("a@b.c", DefaultTTL)

	// Flip the last character of the signature.
	bad := tok[:len(tok)-1] + string(rune(tok[len(tok)-1])^1)
	if _, err := s.Verify(bad); err == nil {
		t.Error("expected tampered token to be rejected")
	}
}

func TestVerifyRejectsWrongKey(t *testing.T) {
	tok, _ := New("key-one").Issue("a@b.c", DefaultTTL)
	if _, err := New("key-two").Verify(tok); err == nil {
		t.Error("expected token signed with a different key to be rejected")
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	s := New("test-secret-key")
	tok, _ := s.Issue("a@b.c", -1*time.Minute) // already expired
	if _, err := s.Verify(tok); err == nil {
		t.Error("expected expired token to be rejected")
	}
}

func TestDisabledSigner(t *testing.T) {
	s := New("")
	if s.Enabled() {
		t.Error("empty-key signer should be disabled")
	}
	if _, err := s.Issue("a@b.c", DefaultTTL); err == nil {
		t.Error("disabled signer should not issue tokens")
	}
}

func TestBookingTokenRoundTrip(t *testing.T) {
	s := New("secret")
	tok, err := s.IssueBooking(42, BookingTokenTTL)
	if err != nil {
		t.Fatalf("IssueBooking: %v", err)
	}
	id, err := s.VerifyBooking(tok)
	if err != nil {
		t.Fatalf("VerifyBooking: %v", err)
	}
	if id != 42 {
		t.Errorf("booking id = %d, want 42", id)
	}
}

func TestBookingTokenNotInterchangeableWithMemberToken(t *testing.T) {
	s := New("secret")
	// A member (email) token must not verify as a booking token.
	memberTok, _ := s.Issue("a@b.c", DefaultTTL)
	if _, err := s.VerifyBooking(memberTok); err == nil {
		t.Error("member token should not verify as a booking token")
	}
	// And vice versa.
	bookingTok, _ := s.IssueBooking(1, BookingTokenTTL)
	if _, err := s.Verify(bookingTok); err == nil {
		t.Error("booking token should not verify as a member token")
	}
}

func TestBookingTokenRejectsTampering(t *testing.T) {
	s := New("secret")
	tok, _ := s.IssueBooking(7, BookingTokenTTL)
	bad := tok[:len(tok)-1] + string(rune(tok[len(tok)-1])^1)
	if _, err := s.VerifyBooking(bad); err == nil {
		t.Error("expected tampered booking token to be rejected")
	}
}
