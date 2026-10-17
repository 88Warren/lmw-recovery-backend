package mailer

import (
	"strings"
	"testing"
	"time"
)

// TestLocalStartsAtBST verifies the DST bug fix: a 09:00 Europe/London slot in
// October is stored as 08:00 UTC (BST = UTC+1). The confirmation must display
// 9:00am, not 8:00am.
func TestLocalStartsAtBST(t *testing.T) {
	// 2026-10-18 is during British Summer Time.
	utc := time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC) // == 09:00 London

	got := BookingConfirmationData{StartsAt: utc}.LocalStartsAt()
	if !strings.Contains(got, "9:00am") {
		t.Errorf("confirmation time = %q, want it to contain 9:00am", got)
	}
	if strings.Contains(got, "8:00am") {
		t.Errorf("confirmation time = %q still shows the UTC hour (8:00am)", got)
	}

	gotNotify := BookingNotificationData{StartsAt: utc}.LocalStartsAt()
	if !strings.Contains(gotNotify, "9:00am") {
		t.Errorf("notification time = %q, want it to contain 9:00am", gotNotify)
	}
}

// TestLocalStartsAtGMT verifies the winter (GMT, UTC+0) case still works: a
// 09:00 London slot in January is stored as 09:00 UTC and shows as 9:00am.
func TestLocalStartsAtGMT(t *testing.T) {
	utc := time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC) // == 09:00 London (GMT)
	got := BookingConfirmationData{StartsAt: utc}.LocalStartsAt()
	if !strings.Contains(got, "9:00am") {
		t.Errorf("confirmation time = %q, want 9:00am", got)
	}
}

// TestWhat3WordsClean strips a leading /// so the template renders consistently.
func TestWhat3WordsClean(t *testing.T) {
	cases := map[string]string{
		"///filled.count.soap": "filled.count.soap",
		"filled.count.soap":    "filled.count.soap",
		"  ///a.b.c  ":         "a.b.c",
		"":                     "",
	}
	for in, want := range cases {
		got := BookingConfirmationData{StudioWhat3Words: in}.What3WordsClean()
		if got != want {
			t.Errorf("What3WordsClean(%q) = %q, want %q", in, got, want)
		}
	}
}
