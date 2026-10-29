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
	"lmwrecovery/backend/internal/memberauth"
	"lmwrecovery/backend/internal/middleware"
	"lmwrecovery/backend/internal/notify"
	"lmwrecovery/backend/internal/payments"
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
	// A seeding failure means the booking page would render with zero slots,
	// which is indistinguishable from "fully booked" to clients. Fail loudly on
	// startup instead of serving an empty calendar.
	if err := scheduler.SeedDefaultSlots(database); err != nil {
		log.Fatalf("slot seeding failed: %v", err)
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

	// ── Owner booking notifications (optional, free) ───────────────────────────
	// Telegram (free, no limits). When configured, each new booking also sends a
	// Telegram alert on top of the email notification.
	ownerNotifier := notify.NewMulti(
		notify.NewTelegram(cfg.TelegramBotToken, cfg.TelegramChatID),
	)
	if ownerNotifier.Enabled() {
		log.Printf("owner booking notifications enabled")
	}

	// ── Payments (Stripe one-off Checkout) ─────────────────────────────────────
	stripeSvc := payments.New(cfg.StripeSecretKey, cfg.StripeWebhookSecret)
	if stripeSvc.Enabled() {
		log.Printf("stripe payments enabled")
	} else {
		log.Printf("WARNING: stripe not configured — bookings cannot be paid for")
	}

	bookingDeps := handlers.BookingDeps{
		Mailer:             mail,
		Notifier:           ownerNotifier,
		NotifyTo:           cfg.EmailTo,
		BrevoAPIKey:        cfg.BrevoAPIKey,
		BrevoTemplateID:    cfg.BrevoTemplateID,
		StudioAddress:      cfg.StudioAddress,
		StudioWhat3Words:   cfg.StudioWhat3Words,
		Stripe:             stripeSvc,
		PublicBaseURL:      cfg.PublicBaseURL,
		MaintenancePriceID: cfg.StripeMaintenancePriceID,
		// Reuse the admin token as the HMAC key for passwordless member links.
		MemberSigner: memberauth.New(cfg.AdminToken),
	}

	// ── Expire unpaid pending bookings on an interval ──────────────────────────
	expiryCtx, stopExpiry := context.WithCancel(context.Background())
	defer stopExpiry()
	go scheduler.StartPendingExpiryLoop(expiryCtx, database, 5*time.Minute)

	// ── Keep the bookable horizon ~3 months ahead ──────────────────────────────
	// Seeding at startup only covers whatever "now" was at boot; re-seed daily so
	// a long-running server always has roughly 3 months of future slots open.
	seedCtx, stopSeed := context.WithCancel(context.Background())
	defer stopSeed()
	go scheduler.StartSeedLoop(seedCtx, database, 24*time.Hour)

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
		handlers.BookingHandler(database, bookingDeps),
	)
	mux.Handle("POST /api/bookings/pair",
		handlers.BookingPairHandler(database, bookingDeps),
	)
	// Stripe calls this server-to-server; it verifies its own signature on the
	// raw body, so it needs no CORS and no auth.
	mux.Handle("POST /api/stripe/webhook",
		handlers.StripeWebhookHandler(database, bookingDeps),
	)
	mux.Handle("GET /api/bookings/status",
		handlers.BookingStatusHandler(database),
	)
	mux.Handle("GET /api/subscriptions/status",
		handlers.SubscriptionStatusHandler(database),
	)
	mux.Handle("POST /api/subscriptions/portal",
		handlers.PortalHandler(database, bookingDeps),
	)
	mux.Handle("POST /api/member/request-link",
		handlers.RequestMemberLinkHandler(database, bookingDeps),
	)
	mux.Handle("GET /api/member/verify",
		handlers.VerifyMemberHandler(database, bookingDeps),
	)
	// Client self-service booking management (via signed link in their email).
	mux.Handle("GET /api/bookings/manage",
		handlers.ManageBookingHandler(database, bookingDeps),
	)
	mux.Handle("POST /api/bookings/reschedule",
		handlers.RescheduleHandler(database, bookingDeps),
	)
	mux.Handle("POST /api/bookings/request-cancellation",
		handlers.RequestCancellationHandler(database, bookingDeps),
	)
	mux.Handle("POST /api/bookings/manage-portal",
		handlers.ManagePortalHandler(database, bookingDeps),
	)
	// Client health screening form (via signed link in their confirmation email).
	mux.Handle("GET /api/screening",
		handlers.ScreeningIntroHandler(database, bookingDeps),
	)
	mux.Handle("POST /api/screening",
		handlers.ScreeningSubmitHandler(database, bookingDeps),
	)

	// ── Admin routes (all require Bearer token) ───────────────────────────────
	adminAuth := middleware.AdminAuth(cfg.AdminToken)

	mux.Handle("GET /api/admin/slots",
		adminAuth(handlers.AdminListSlotsHandler(database)))

	mux.Handle("POST /api/admin/slots",
		adminAuth(handlers.AdminCreateSlotsHandler(database)))

	mux.Handle("DELETE /api/admin/slots/{id}",
		adminAuth(handlers.AdminDeleteSlotHandler(database)))

	mux.Handle("PATCH /api/admin/slots/{id}/block",
		adminAuth(handlers.AdminSetSlotBlockHandler(database)))

	mux.Handle("GET /api/admin/bookings",
		adminAuth(handlers.AdminListBookingsHandler(database)))

	mux.Handle("PATCH /api/admin/bookings/{id}/cancel",
		adminAuth(handlers.AdminCancelBookingHandler(database, bookingDeps)))

	mux.Handle("GET /api/admin/contacts",
		adminAuth(handlers.AdminListContactsHandler(database)))

	mux.Handle("GET /api/admin/screening",
		adminAuth(handlers.AdminListScreeningHandler(database)))

	mux.Handle("GET /api/admin/screening/{id}",
		adminAuth(handlers.AdminGetScreeningHandler(database)))

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
