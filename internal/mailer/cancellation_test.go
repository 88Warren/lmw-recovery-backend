package mailer

import (
	"strings"
	"testing"
	"time"
)

func TestCancellationEmailOneOff(t *testing.T) {
	html, err := render(bookingCancellationTmpl, BookingCancellationData{
		ClientName: "Sam",
		Treatment:  "Sports Massage",
		StartsAt:   time.Date(2026, 10, 22, 17, 0, 0, 0, time.UTC),
		Refunded:   true,
		BookURL:    "https://www.lmwrecovery.co.uk/",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"session has been cancelled",
		"full refund has been issued",
		"Book another session",
		"Sports Massage",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("one-off cancellation email missing %q", want)
		}
	}
	// Must NOT mention the health questionnaire (the old bug).
	if strings.Contains(html, "questionnaire") {
		t.Error("cancellation email should not mention the health questionnaire")
	}
}

func TestCancellationEmailSubscription(t *testing.T) {
	html, err := render(bookingCancellationTmpl, BookingCancellationData{
		ClientName:     "Sam",
		IsSubscription: true,
		BookURL:        "https://www.lmwrecovery.co.uk/",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"plan has been cancelled",
		"won't be charged again",
		"Book another session",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("subscription cancellation email missing %q", want)
		}
	}
	if strings.Contains(html, "questionnaire") {
		t.Error("subscription cancellation email should not mention the questionnaire")
	}
}
