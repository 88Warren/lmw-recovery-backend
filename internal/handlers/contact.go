package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"lmwrecovery/backend/internal/mailer"
	"lmwrecovery/backend/internal/models"
)

type contactRequest struct {
	Name           string `json:"name"`
	Email          string `json:"email"`
	Phone          string `json:"phone"`
	Message        string `json:"message"`
	RecaptchaToken string `json:"recaptcha_token"`
}

// ContactHandler handles POST /api/contact
// recaptchaSecret: the server-side secret key. Pass "" to skip verification (dev/test).
func ContactHandler(db *sql.DB, m *mailer.Mailer, notifyTo, recaptchaSecret string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req contactRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		req.Name = strings.TrimSpace(req.Name)
		req.Email = strings.TrimSpace(req.Email)
		req.Message = strings.TrimSpace(req.Message)

		if req.Name == "" || req.Email == "" || req.Message == "" {
			jsonError(w, "name, email and message are required", http.StatusBadRequest)
			return
		}
		if !strings.Contains(req.Email, "@") {
			jsonError(w, "invalid email address", http.StatusBadRequest)
			return
		}

		// ── reCAPTCHA v3 verification ─────────────────────────────────────────
		if recaptchaSecret != "" {
			if req.RecaptchaToken == "" {
				jsonError(w, "missing reCAPTCHA token", http.StatusBadRequest)
				return
			}
			score, err := verifyRecaptcha(recaptchaSecret, req.RecaptchaToken, r.RemoteAddr)
			if err != nil {
				log.Printf("ERROR recaptcha verify: %v", err)
				jsonError(w, "could not verify reCAPTCHA", http.StatusInternalServerError)
				return
			}
			if score < 0.5 {
				log.Printf("recaptcha blocked — score %.2f for %s", score, r.RemoteAddr)
				jsonError(w, "request blocked by spam filter", http.StatusForbidden)
				return
			}
		}

		// ── Persist ───────────────────────────────────────────────────────────
		contact := models.Contact{
			Name:      req.Name,
			Email:     req.Email,
			Phone:     req.Phone,
			Message:   req.Message,
			CreatedAt: time.Now().UTC(),
		}
		row := db.QueryRow(
			`INSERT INTO contacts (name, email, phone, message) VALUES (?, ?, ?, ?) RETURNING id, created_at`,
			contact.Name, contact.Email, contact.Phone, contact.Message,
		)
		if err := row.Scan(&contact.ID, &contact.CreatedAt); err != nil {
			log.Printf("ERROR insert contact: %v", err)
			jsonError(w, "could not save your message", http.StatusInternalServerError)
			return
		}

		// ── Emails (async) ────────────────────────────────────────────────────
		data := mailer.ContactNotificationData{
			Name:    contact.Name,
			Email:   contact.Email,
			Phone:   contact.Phone,
			Message: contact.Message,
		}
		go func() {
			if err := m.SendContactNotification(notifyTo, data); err != nil {
				log.Printf("ERROR send contact notification: %v", err)
			}
			if err := m.SendContactAck(contact.Email, data); err != nil {
				log.Printf("ERROR send contact ack: %v", err)
			}
		}()

		jsonOK(w, map[string]any{
			"message": "Thanks for getting in touch — I'll be in touch shortly.",
			"id":      contact.ID,
		})
	}
}

// verifyRecaptcha calls Google's siteverify endpoint and returns the score (0–1).
// A score ≥ 0.5 is considered human.
func verifyRecaptcha(secret, token, remoteIP string) (float64, error) {
	resp, err := http.PostForm("https://www.google.com/recaptcha/api/siteverify", url.Values{
		"secret":   {secret},
		"response": {token},
		"remoteip": {remoteIP},
	})
	if err != nil {
		return 0, fmt.Errorf("siteverify request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("siteverify read: %w", err)
	}

	var result struct {
		Success    bool     `json:"success"`
		Score      float64  `json:"score"`
		Action     string   `json:"action"`
		ErrorCodes []string `json:"error-codes"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, fmt.Errorf("siteverify parse: %w", err)
	}
	if !result.Success {
		return 0, fmt.Errorf("siteverify failed: %v", result.ErrorCodes)
	}

	return result.Score, nil
}
