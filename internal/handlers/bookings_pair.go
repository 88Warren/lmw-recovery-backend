package handlers

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"lmwrecovery/backend/internal/mailer"
	"lmwrecovery/backend/internal/models"
)

type bookingPairRequest struct {
	SlotID1               int64  `json:"slot_id_1"`
	SlotID2               int64  `json:"slot_id_2"`
	Treatment             string `json:"treatment"`
	TreatmentDurationMins int    `json:"treatment_duration_minutes"`
	ClientName            string `json:"client_name"`
	ClientEmail           string `json:"client_email"`
	ClientPhone           string `json:"client_phone"`
	Notes                 string `json:"notes"`
}

// BookingPairHandler handles POST /api/bookings/pair
// Books two slots atomically for the Maintenance Plan (2 sessions/month).
// Uses the same overlap check as BookingHandler.
func BookingPairHandler(db *sql.DB, m *mailer.Mailer, notifyTo, brevoAPIKey string, brevoTemplateID int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req bookingPairRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}

		req.ClientName = strings.TrimSpace(req.ClientName)
		req.ClientEmail = strings.TrimSpace(req.ClientEmail)
		req.Treatment = strings.TrimSpace(req.Treatment)

		if req.SlotID1 == 0 || req.SlotID2 == 0 {
			jsonError(w, "slot_id_1 and slot_id_2 are required", http.StatusBadRequest)
			return
		}
		if req.SlotID1 == req.SlotID2 {
			jsonError(w, "both sessions must be different slots", http.StatusBadRequest)
			return
		}
		if req.ClientName == "" || req.ClientEmail == "" {
			jsonError(w, "client_name and client_email are required", http.StatusBadRequest)
			return
		}
		if !strings.Contains(req.ClientEmail, "@") {
			jsonError(w, "invalid email address", http.StatusBadRequest)
			return
		}
		if req.TreatmentDurationMins <= 0 {
			req.TreatmentDurationMins = 60
		}

		tx, err := db.Begin()
		if err != nil {
			log.Printf("ERROR begin tx: %v", err)
			jsonError(w, "server error", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback() //nolint:errcheck

		slots := make([]models.Slot, 2)
		for i, slotID := range []int64{req.SlotID1, req.SlotID2} {
			var s models.Slot
			var startsStr string
			err := tx.QueryRow(`SELECT id, starts_at FROM slots WHERE id = ?`, slotID).
				Scan(&s.ID, &startsStr)
			if err == sql.ErrNoRows {
				jsonError(w, "slot not found", http.StatusNotFound)
				return
			}
			if err != nil {
				log.Printf("ERROR query slot %d: %v", slotID, err)
				jsonError(w, "server error", http.StatusInternalServerError)
				return
			}
			s.StartsAt, _ = time.Parse(time.RFC3339, startsStr)
			slots[i] = s

			endsAt := s.StartsAt.Add(time.Duration(req.TreatmentDurationMins) * time.Minute)

			var conflicts int
			err = tx.QueryRow(`
				SELECT COUNT(*) FROM bookings b
				JOIN slots sl ON sl.id = b.slot_id
				WHERE b.status IN ('confirmed','pending')
				  AND datetime(sl.starts_at) < datetime(?)
				  AND datetime(?) < datetime(sl.starts_at, '+' || b.treatment_duration_minutes || ' minutes')
			`,
				endsAt.Format(time.RFC3339),
				s.StartsAt.Format(time.RFC3339),
			).Scan(&conflicts)
			if err != nil {
				log.Printf("ERROR overlap check slot %d: %v", slotID, err)
				jsonError(w, "server error", http.StatusInternalServerError)
				return
			}
			if conflicts > 0 {
				jsonError(w, "one or more selected times are no longer available", http.StatusConflict)
				return
			}
		}

		// Insert both bookings
		bookings := make([]models.Booking, 2)
		for i, s := range slots {
			var b models.Booking
			row := tx.QueryRow(`
				INSERT INTO bookings
				  (slot_id, treatment, treatment_duration_minutes, client_name, client_email, client_phone, notes, status)
				VALUES (?, ?, ?, ?, ?, ?, ?, 'confirmed')
				RETURNING id, created_at`,
				s.ID, req.Treatment, req.TreatmentDurationMins,
				req.ClientName, req.ClientEmail, req.ClientPhone, req.Notes,
			)
			if err := row.Scan(&b.ID, &b.CreatedAt); err != nil {
				log.Printf("ERROR insert booking %d: %v", i+1, err)
				jsonError(w, "could not save booking", http.StatusInternalServerError)
				return
			}
			b.SlotID = s.ID
			b.Treatment = req.Treatment
			b.TreatmentDurationMins = req.TreatmentDurationMins
			b.ClientName = req.ClientName
			b.ClientEmail = req.ClientEmail
			b.ClientPhone = req.ClientPhone
			b.Notes = req.Notes
			b.Status = "confirmed"
			b.Slot = &slots[i]
			bookings[i] = b
		}

		if err := tx.Commit(); err != nil {
			log.Printf("ERROR commit tx: %v", err)
			jsonError(w, "server error", http.StatusInternalServerError)
			return
		}

		go func() {
			notesExtra := req.Notes
			if notesExtra != "" {
				notesExtra += "\n"
			}
			notesExtra += "Session 2: " + slots[1].StartsAt.Format("Monday 2 January 2006, 15:04")

			if err := m.SendBookingConfirmation(req.ClientEmail, mailer.BookingConfirmationData{
				ClientName: req.ClientName,
				Treatment:  req.Treatment + " (2 sessions)",
				StartsAt:   slots[0].StartsAt,
				Notes:      notesExtra,
			}); err != nil {
				log.Printf("ERROR send booking confirmation: %v", err)
			}
			if err := m.SendBookingNotification(notifyTo, mailer.BookingNotificationData{
				ClientName:  req.ClientName,
				ClientEmail: req.ClientEmail,
				ClientPhone: req.ClientPhone,
				Treatment:   req.Treatment + " (2 sessions)",
				StartsAt:    slots[0].StartsAt,
				Notes:       "Session 1: " + slots[0].StartsAt.Format("Mon 2 Jan, 15:04") + "\nSession 2: " + slots[1].StartsAt.Format("Mon 2 Jan, 15:04"),
			}); err != nil {
				log.Printf("ERROR send booking notification: %v", err)
			}
			if brevoAPIKey != "" && brevoTemplateID > 0 {
				if err := triggerBrevoQuestionnaire(brevoAPIKey, brevoTemplateID, bookings[0]); err != nil {
					log.Printf("ERROR brevo questionnaire: %v", err)
				} else {
					for _, b := range bookings {
						if _, err := db.Exec(`UPDATE bookings SET brevo_sent = 1 WHERE id = ?`, b.ID); err != nil {
							log.Printf("ERROR update brevo_sent: %v", err)
						}
					}
				}
			}
		}()

		jsonOK(w, map[string]any{
			"message":     "Both sessions confirmed. Check your email for details.",
			"booking_ids": []int64{bookings[0].ID, bookings[1].ID},
			"slots":       slots,
		})
	}
}
