package config

import (
	"fmt"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

// Config holds all runtime configuration loaded from environment variables.
type Config struct {
	// Server
	Port string

	// Database
	DBPath string

	// SMTP — Ionos
	SMTPHost     string
	SMTPPort     int
	SMTPUser     string // your full Ionos email address
	SMTPPassword string
	EmailFrom    string // display name + address, e.g. "LMW Recovery <info@lmwrecovery.co.uk>"
	EmailTo      string // where booking/contact notifications are sent (Laura's inbox)

	// Brevo — health questionnaire automation
	BrevoAPIKey     string
	BrevoListID     int // contact list to add new clients to
	BrevoTemplateID int // transactional template ID for the health questionnaire

	// Stripe
	StripeSecretKey     string // sk_test_… / sk_live_…
	StripeWebhookSecret string // whsec_… signing secret for the webhook endpoint

	// StripeMaintenancePriceID is the recurring Price (price_…) for the £80/month
	// Maintenance Plan subscription, created in the Stripe product catalogue.
	StripeMaintenancePriceID string

	// PublicBaseURL is the client-facing site origin Stripe redirects back to
	// after Checkout, e.g. https://www.lmwrecovery.co.uk. Used to build the
	// success/cancel URLs. Falls back to FrontendOrigin when unset.
	PublicBaseURL string

	// reCAPTCHA v3
	RecaptchaSecret string

	// Admin
	AdminToken string

	// MailDryRun — when true, emails are logged but not sent (useful for local dev when SMTP is blocked)
	MailDryRun bool

	// Frontend origin for CORS
	FrontendOrigin string

	// Studio location shown in the booking confirmation email.
	StudioAddress    string // full address, e.g. "12 Example St, Llantrisant, CF72 8XX"
	StudioWhat3Words string // what3words address, e.g. "filled.count.soap" (with or without leading ///)

	// Telegram booking notification (free, no limits). When both are set, a
	// Telegram message is sent on each new booking.
	TelegramBotToken string // token from @BotFather
	TelegramChatID   string // your chat ID (from getUpdates)
}

// Load reads the environment-specific .env file then falls back to any
// real environment variables already set (which take precedence).
//
// File resolution:
//
//	APP_ENV=development  →  .env.development
//	APP_ENV=production   →  .env.production
//	APP_ENV unset        →  .env.development (safe local default)
//
// In production on the VPS you can skip the file entirely and set env
// vars directly — godotenv won't error if the file is missing.
func Load() (*Config, error) {
	env := os.Getenv("APP_ENV")
	if env == "" {
		env = "development"
	}

	envFile := fmt.Sprintf(".env.%s", env)
	if err := godotenv.Load(envFile); err != nil && !os.IsNotExist(err) {
		// File exists but couldn't be read — surface the error.
		return nil, fmt.Errorf("load %s: %w", envFile, err)
	}
	// os.IsNotExist is fine — production may rely on real env vars.

	cfg := &Config{
		Port:             getEnv("PORT", "8080"),
		DBPath:           getEnv("DB_PATH", "./lmw.db"),
		SMTPHost:         getEnv("SMTP_HOST", "smtp.ionos.co.uk"),
		SMTPUser:         mustEnv("SMTP_USER"),
		SMTPPassword:     mustEnv("SMTP_PASSWORD"),
		EmailFrom:        getEnv("EMAIL_FROM", "LMW Recovery <info@lmwrecovery.co.uk>"),
		EmailTo:          getEnv("EMAIL_TO", "info@lmwrecovery.co.uk"),
		BrevoAPIKey:      getEnv("BREVO_API_KEY", ""),
		AdminToken:       mustEnv("ADMIN_TOKEN"),
		MailDryRun:       os.Getenv("MAIL_DRY_RUN") == "true",
		RecaptchaSecret:  getEnv("RECAPTCHA_SECRET", ""),
		FrontendOrigin:   getEnv("FRONTEND_ORIGIN", "http://localhost:5173"),
		StudioAddress:    getEnv("STUDIO_ADDRESS", ""),
		StudioWhat3Words: getEnv("STUDIO_WHAT3WORDS", ""),
		TelegramBotToken: getEnv("TELEGRAM_BOT_TOKEN", ""),
		TelegramChatID:   getEnv("TELEGRAM_CHAT_ID", ""),

		StripeSecretKey:          getEnv("STRIPE_SECRET_KEY", ""),
		StripeWebhookSecret:      getEnv("STRIPE_WEBHOOK_SECRET", ""),
		StripeMaintenancePriceID: getEnv("STRIPE_MAINTENANCE_PRICE_ID", ""),
		PublicBaseURL:            getEnv("PUBLIC_BASE_URL", ""),
	}

	// Default the public base URL to the frontend origin when not set explicitly.
	if cfg.PublicBaseURL == "" {
		cfg.PublicBaseURL = cfg.FrontendOrigin
	}

	// SMTP port
	portStr := getEnv("SMTP_PORT", "587")
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("invalid SMTP_PORT %q: %w", portStr, err)
	}
	cfg.SMTPPort = port

	// Brevo list ID (optional — only needed if using Brevo contact lists)
	if v := os.Getenv("BREVO_LIST_ID"); v != "" {
		id, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("invalid BREVO_LIST_ID %q: %w", v, err)
		}
		cfg.BrevoListID = id
	}

	// Brevo template ID for health questionnaire email
	if v := os.Getenv("BREVO_TEMPLATE_ID"); v != "" {
		id, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("invalid BREVO_TEMPLATE_ID %q: %w", v, err)
		}
		cfg.BrevoTemplateID = id
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		// Not fatal at config-load time — main will catch the zero value.
		fmt.Printf("WARNING: required env var %q is not set\n", key)
	}
	return v
}
