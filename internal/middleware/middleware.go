package middleware

import (
	"log"
	"net/http"
	"strings"
	"time"
)

// CORS adds the headers that allow the configured frontend origin(s) to call
// the API. It accepts a comma-separated list of allowed origins (e.g.
// "https://lmwrecovery.co.uk,https://www.lmwrecovery.co.uk") and echoes back
// the single origin that matches the incoming request's Origin header.
//
// Echoing a single matched origin — rather than a static value — is the
// spec-correct way to support more than one origin: the
// Access-Control-Allow-Origin header may contain only one origin, and when
// credentials are allowed it must not be the "*" wildcard.
func CORS(allowedOrigins string) func(http.Handler) http.Handler {
	// Parse once at setup into a set for O(1) lookups.
	allowed := make(map[string]struct{})
	for _, o := range strings.Split(allowedOrigins, ",") {
		if o = strings.TrimSpace(o); o != "" {
			allowed[o] = struct{}{}
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if _, ok := allowed[origin]; ok {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				// Caches must vary on Origin since the header is request-dependent.
				w.Header().Add("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}

			// Handle pre-flight
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// Logger logs each request with method, path, status and duration.
func Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, rw.status, time.Since(start))
	})
}

// RecoverPanic catches any panics in handlers and returns a 500.
func RecoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("PANIC: %v", rec)
				http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// responseWriter wraps http.ResponseWriter to capture the status code.
type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}
