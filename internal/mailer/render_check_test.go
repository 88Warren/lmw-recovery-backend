package mailer

import (
	"strings"
	"testing"
	"time"
)

// TestConfirmationRendersLocationDetails checks the confirmation email contains
// the full address and a working what3words link when provided.
func TestConfirmationRendersLocationDetails(t *testing.T) {
	html, err := render(bookingConfirmationTmpl, BookingConfirmationData{
		ClientName:       "Sam",
		Treatment:        "Sports Massage",
		StartsAt:         time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC),
		StudioAddress:    "12 Example Street, Llantrisant, CF72 8XX",
		StudioWhat3Words: "///filled.count.soap",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	for _, want := range []string{
		"12 Example Street, Llantrisant, CF72 8XX",
		"https://what3words.com/filled.count.soap",
		"///filled.count.soap",
		"9:00am",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("confirmation HTML missing %q", want)
		}
	}
}

// TestConfirmationFallsBackWithoutAddress keeps the "sent separately" line when
// no address is configured, and omits the what3words row.
func TestConfirmationFallsBackWithoutAddress(t *testing.T) {
	html, err := render(bookingConfirmationTmpl, BookingConfirmationData{
		ClientName: "Sam",
		Treatment:  "Sports Massage",
		StartsAt:   time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(html, "Full address sent separately.") {
		t.Error("expected fallback address line when no address configured")
	}
	if strings.Contains(html, "what3words") {
		t.Error("did not expect a what3words row when none configured")
	}
}
