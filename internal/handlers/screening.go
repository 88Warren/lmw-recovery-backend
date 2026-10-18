package handlers

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Screening handlers power the client health-screening form: a mobile-friendly
// web form reached from a signed link in the booking confirmation email. The
// client fills it in, submits it back, and it is stored for the studio owner to
// read in the admin area. No file uploads or app downloads are involved — the
// answers are structured JSON.

// screeningIntro is the public payload for the form page: who it's for and
// whether they've already submitted (so the UI can show a "thank you" instead
// of letting them re-submit by accident).
type screeningIntro struct {
	ClientName   string `json:"client_name"`
	AlreadyDone  bool   `json:"already_done"`
	Treatment    string `json:"treatment"`
	BookingEmail string `json:"booking_email"`
}

// ScreeningIntroHandler handles GET /api/screening?token=…
// Verifies the signed link and returns the client's name so the form can greet
// them, plus whether a form has already been submitted for this booking.
func ScreeningIntroHandler(db *sql.DB, deps BookingDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.MemberSigner == nil || !deps.MemberSigner.Enabled() {
			jsonError(w, "this form is not available right now", http.StatusServiceUnavailable)
			return
		}
		bookingID, verr := deps.MemberSigner.VerifyScreening(r.URL.Query().Get("token"))
		if verr != nil {
			jsonError(w, "this link is invalid or has expired", http.StatusUnauthorized)
			return
		}

		var (
			name      string
			email     string
			treatment string
		)
		err := db.QueryRow(
			`SELECT client_name, client_email, treatment FROM bookings WHERE id = ?`, bookingID,
		).Scan(&name, &email, &treatment)
		if err == sql.ErrNoRows {
			jsonError(w, "booking not found", http.StatusNotFound)
			return
		}
		if err != nil {
			jsonError(w, "server error", http.StatusInternalServerError)
			return
		}

		var done int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM screening_forms WHERE booking_id = ?`, bookingID,
		).Scan(&done); err != nil {
			jsonError(w, "server error", http.StatusInternalServerError)
			return
		}

		jsonOK(w, screeningIntro{
			ClientName:   name,
			AlreadyDone:  done > 0,
			Treatment:    treatment,
			BookingEmail: email,
		})
	}
}

// screeningSubmit is the client submission body. Answers carries the full form
// (every question, lifestyle, GP info, declaration) as a free-form object so
// the form can grow without a schema change; the explicit fields are the ones
// we also want as queryable columns.
type screeningSubmit struct {
	Token            string          `json:"token"`
	DateOfBirth      string          `json:"date_of_birth"`
	Phone            string          `json:"phone"`
	SignatureName    string          `json:"signature_name"`
	ConsentTreatment bool            `json:"consent_treatment"`
	ConsentData      bool            `json:"consent_data"`
	Answers          json.RawMessage `json:"answers"`
}

// ScreeningSubmitHandler handles POST /api/screening
// Token-verified. Stores the completed form against the booking. Both consent
// boxes and a typed signature are required — this is the informed-consent gate.
func ScreeningSubmitHandler(db *sql.DB, deps BookingDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req screeningSubmit
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if deps.MemberSigner == nil || !deps.MemberSigner.Enabled() {
			jsonError(w, "this form is not available right now", http.StatusServiceUnavailable)
			return
		}
		bookingID, verr := deps.MemberSigner.VerifyScreening(req.Token)
		if verr != nil {
			jsonError(w, "this link is invalid or has expired", http.StatusUnauthorized)
			return
		}

		// Informed-consent gate: both acknowledgements and a signature required.
		if !req.ConsentTreatment || !req.ConsentData {
			jsonError(w, "please confirm both consent statements to submit", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.SignatureName) == "" {
			jsonError(w, "please type your full name to sign the declaration", http.StatusBadRequest)
			return
		}
		if len(req.Answers) == 0 {
			jsonError(w, "the form is empty", http.StatusBadRequest)
			return
		}

		var (
			name  string
			email string
		)
		err := db.QueryRow(
			`SELECT client_name, client_email FROM bookings WHERE id = ?`, bookingID,
		).Scan(&name, &email)
		if err == sql.ErrNoRows {
			jsonError(w, "booking not found", http.StatusNotFound)
			return
		}
		if err != nil {
			jsonError(w, "server error", http.StatusInternalServerError)
			return
		}

		// One screening per booking — treat a repeat submit as idempotent.
		var existing int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM screening_forms WHERE booking_id = ?`, bookingID,
		).Scan(&existing); err != nil {
			jsonError(w, "server error", http.StatusInternalServerError)
			return
		}
		if existing > 0 {
			jsonOK(w, map[string]any{"message": "Thank you — your form has already been received."})
			return
		}

		_, err = db.Exec(`
			INSERT INTO screening_forms
			  (booking_id, client_name, client_email, client_phone, date_of_birth,
			   answers, consent_treatment, consent_data, signature_name, submitted_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			bookingID, name, email, strings.TrimSpace(req.Phone), strings.TrimSpace(req.DateOfBirth),
			string(req.Answers), boolToInt(req.ConsentTreatment), boolToInt(req.ConsentData),
			strings.TrimSpace(req.SignatureName), time.Now().UTC().Format(time.RFC3339),
		)
		if err != nil {
			log.Printf("ERROR insert screening form: %v", err)
			jsonError(w, "could not save your form", http.StatusInternalServerError)
			return
		}

		// Let the owner know a screening form has landed (best-effort).
		go deps.notifyOwner("New screening form submitted\n" + name + " — " + email)

		jsonOK(w, map[string]any{"message": "Thank you — your form has been received."})
	}
}

// ── Admin: list & read screening submissions (Bearer-token protected) ─────────

type adminScreeningSummary struct {
	ID               int64     `json:"id"`
	BookingID        *int64    `json:"booking_id,omitempty"`
	ClientName       string    `json:"client_name"`
	ClientEmail      string    `json:"client_email"`
	ClientPhone      string    `json:"client_phone"`
	DateOfBirth      string    `json:"date_of_birth"`
	ConsentTreatment bool      `json:"consent_treatment"`
	ConsentData      bool      `json:"consent_data"`
	SignatureName    string    `json:"signature_name"`
	SubmittedAt      time.Time `json:"submitted_at"`
}

// AdminListScreeningHandler handles GET /api/admin/screening
func AdminListScreeningHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.Query(`
			SELECT id, booking_id, client_name, client_email, client_phone,
			       date_of_birth, consent_treatment, consent_data,
			       signature_name, submitted_at
			FROM screening_forms
			ORDER BY submitted_at DESC
			LIMIT 500`)
		if err != nil {
			log.Printf("ERROR list screening forms: %v", err)
			jsonError(w, "could not fetch screening forms", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		result := make([]adminScreeningSummary, 0)
		for rows.Next() {
			var (
				s            adminScreeningSummary
				bookingID    sql.NullInt64
				phone, dob   sql.NullString
				sig          sql.NullString
				ct, cd       int
				submittedStr string
			)
			if err := rows.Scan(
				&s.ID, &bookingID, &s.ClientName, &s.ClientEmail, &phone,
				&dob, &ct, &cd, &sig, &submittedStr,
			); err != nil {
				log.Printf("ERROR scan screening form: %v", err)
				continue
			}
			if bookingID.Valid {
				s.BookingID = &bookingID.Int64
			}
			s.ClientPhone = phone.String
			s.DateOfBirth = dob.String
			s.SignatureName = sig.String
			s.ConsentTreatment = ct == 1
			s.ConsentData = cd == 1
			s.SubmittedAt, _ = time.Parse(time.RFC3339, submittedStr)
			result = append(result, s)
		}
		if err := rows.Err(); err != nil {
			log.Printf("ERROR iterate screening forms: %v", err)
			jsonError(w, "could not fetch screening forms", http.StatusInternalServerError)
			return
		}
		jsonOK(w, result)
	}
}

// AdminGetScreeningHandler handles GET /api/admin/screening/{id}
// Returns the full submission including the raw answers JSON for review.
func AdminGetScreeningHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id == 0 {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}

		var (
			s            adminScreeningSummary
			bookingID    sql.NullInt64
			phone, dob   sql.NullString
			sig          sql.NullString
			ct, cd       int
			submittedStr string
			answers      string
		)
		err = db.QueryRow(`
			SELECT id, booking_id, client_name, client_email, client_phone,
			       date_of_birth, consent_treatment, consent_data,
			       signature_name, submitted_at, answers
			FROM screening_forms WHERE id = ?`, id,
		).Scan(&s.ID, &bookingID, &s.ClientName, &s.ClientEmail, &phone,
			&dob, &ct, &cd, &sig, &submittedStr, &answers)
		if err == sql.ErrNoRows {
			jsonError(w, "screening form not found", http.StatusNotFound)
			return
		}
		if err != nil {
			jsonError(w, "server error", http.StatusInternalServerError)
			return
		}
		if bookingID.Valid {
			s.BookingID = &bookingID.Int64
		}
		s.ClientPhone = phone.String
		s.DateOfBirth = dob.String
		s.SignatureName = sig.String
		s.ConsentTreatment = ct == 1
		s.ConsentData = cd == 1
		s.SubmittedAt, _ = time.Parse(time.RFC3339, submittedStr)

		// answers is stored as a JSON string; forward it as raw JSON.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		enc := json.NewEncoder(w)
		enc.Encode(map[string]any{ //nolint:errcheck
			"id":                s.ID,
			"booking_id":        s.BookingID,
			"client_name":       s.ClientName,
			"client_email":      s.ClientEmail,
			"client_phone":      s.ClientPhone,
			"date_of_birth":     s.DateOfBirth,
			"consent_treatment": s.ConsentTreatment,
			"consent_data":      s.ConsentData,
			"signature_name":    s.SignatureName,
			"submitted_at":      s.SubmittedAt,
			"answers":           json.RawMessage(answers),
		})
	}
}
