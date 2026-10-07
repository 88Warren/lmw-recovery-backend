package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"lmwrecovery/backend/internal/config"
	"lmwrecovery/backend/internal/db"
	"lmwrecovery/backend/internal/handlers"
	"lmwrecovery/backend/internal/mailer"
	"lmwrecovery/backend/internal/middleware"
	"lmwrecovery/backend/internal/scheduler"
)

func main() {
	// ── Config ────────────────────────────────────────────────────────────────
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	// ── Database ──────────────────────────────────────────────────────────────
	database, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer database.Close()

	// ── Seed default availability slots ──────────────────────────────────────
	if err := scheduler.SeedDefaultSlots(database); err != nil {
		log.Printf("WARNING: slot seeding failed: %v", err)
	}

	// ── Mailer ────────────────────────────────────────────────────────────────
	mail := mailer.New(
		cfg.SMTPHost,
		cfg.SMTPPort,
		cfg.SMTPUser,
		cfg.SMTPPassword,
		cfg.EmailFrom,
		cfg.MailDryRun,
	)

	// ── Public router ─────────────────────────────────────────────────────────
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`)) //nolint:errcheck
	})

	mux.Handle("GET /api/treatments", handlers.TreatmentsHandler())
	mux.Handle("GET /api/slots", handlers.SlotsHandler(database))
	mux.Handle("POST /api/contact", handlers.ContactHandler(database, mail, cfg.EmailTo, cfg.RecaptchaSecret))
	mux.Handle("POST /api/bookings",
		handlers.BookingHandler(database, mail, cfg.EmailTo, cfg.BrevoAPIKey, cfg.BrevoTemplateID),
	)
	mux.Handle("POST /api/bookings/pair",
		handlers.BookingPairHandler(database, mail, cfg.EmailTo, cfg.BrevoAPIKey, cfg.BrevoTemplateID),
	)

	// ── Admin routes (all require Bearer token) ───────────────────────────────
	adminAuth := middleware.AdminAuth(cfg.AdminToken)

	mux.Handle("GET /api/admin/slots",
		adminAuth(handlers.AdminListSlotsHandler(database)))

	mux.Handle("POST /api/admin/slots",
		adminAuth(handlers.AdminCreateSlotsHandler(database)))

	mux.Handle("DELETE /api/admin/slots/{id}",
		adminAuth(handlers.AdminDeleteSlotHandler(database)))

	mux.Handle("GET /api/admin/bookings",
		adminAuth(handlers.AdminListBookingsHandler(database)))

	mux.Handle("PATCH /api/admin/bookings/{id}/cancel",
		adminAuth(handlers.AdminCancelBookingHandler(database)))

	mux.Handle("GET /api/admin/contacts",
		adminAuth(handlers.AdminListContactsHandler(database)))

	// ── Global middleware chain ───────────────────────────────────────────────
	var handler http.Handler = mux
	handler = middleware.CORS(cfg.FrontendOrigin)(handler)
	handler = middleware.Logger(handler)
	handler = middleware.RecoverPanic(handler)

	// ── Server ────────────────────────────────────────────────────────────────
	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Printf("LMW Recovery API listening on :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	<-quit
	log.Println("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("forced shutdown: %v", err)
	}
	log.Println("server stopped")
}
