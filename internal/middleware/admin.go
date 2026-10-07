package middleware

import (
	"net/http"
	"strings"
)

// AdminAuth is a simple Bearer token check for the admin API routes.
// The token is set via the ADMIN_TOKEN environment variable and must be
// sent as: Authorization: Bearer <token>
func AdminAuth(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Allow pre-flight through without auth
			if r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}

			auth := r.Header.Get("Authorization")
			parts := strings.SplitN(auth, " ", 2)
			if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") || parts[1] != token {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":"unauthorised"}`)) //nolint:errcheck
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
