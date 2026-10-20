package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const corsAllowed = "https://lmwrecovery.co.uk,https://www.lmwrecovery.co.uk"

// okHandler is a trivial downstream handler used to confirm the middleware
// passes non-preflight requests through.
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
})

func TestCORS_EchoesMatchingOrigin(t *testing.T) {
	cases := []struct {
		name       string
		origin     string
		wantOrigin string // expected Access-Control-Allow-Origin ("" = header absent)
	}{
		{"canonical apex", "https://lmwrecovery.co.uk", "https://lmwrecovery.co.uk"},
		{"www alias", "https://www.lmwrecovery.co.uk", "https://www.lmwrecovery.co.uk"},
		{"disallowed origin", "https://evil.example.com", ""},
		{"no origin header", "", ""},
	}

	h := CORS(corsAllowed)(okHandler)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/treatments", nil)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			got := rec.Header().Get("Access-Control-Allow-Origin")
			if got != tc.wantOrigin {
				t.Errorf("Allow-Origin = %q, want %q", got, tc.wantOrigin)
			}

			// A disallowed origin must never receive credentials permission.
			if tc.wantOrigin == "" && rec.Header().Get("Access-Control-Allow-Credentials") != "" {
				t.Errorf("Allow-Credentials set for disallowed origin")
			}
		})
	}
}

func TestCORS_PreflightShortCircuits(t *testing.T) {
	// A downstream handler that fails the test if reached — preflight OPTIONS
	// must be answered by the middleware without calling through.
	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("preflight OPTIONS should not reach the next handler")
	})
	h := CORS(corsAllowed)(next)

	req := httptest.NewRequest(http.MethodOptions, "/api/bookings", nil)
	req.Header.Set("Origin", "https://lmwrecovery.co.uk")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://lmwrecovery.co.uk" {
		t.Errorf("preflight Allow-Origin = %q, want the request origin", got)
	}
}
