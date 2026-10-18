-- ─── Client screening & health history forms ────────────────────────────────
-- A client completes this once before their first session, via a signed link in
-- their confirmation email. Identity fields are stored as columns for the admin
-- list view; the full question set (medical history, lifestyle, declaration) is
-- stored as a JSON blob so the form can evolve without a migration each time.
CREATE TABLE IF NOT EXISTS screening_forms (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    booking_id           INTEGER REFERENCES bookings(id),
    client_name          TEXT    NOT NULL,
    client_email         TEXT    NOT NULL,
    client_phone         TEXT,
    date_of_birth        TEXT,
    -- answers holds the complete submitted form as JSON: client details,
    -- lifestyle, every medical question (yes/no + details), GP info, and the
    -- signed declaration/consent block.
    answers              TEXT    NOT NULL,
    -- Consent flags, surfaced as columns so they are queryable/auditable.
    consent_treatment    INTEGER NOT NULL DEFAULT 0, -- consented to assessment & treatment
    consent_data         INTEGER NOT NULL DEFAULT 0, -- acknowledged privacy policy & terms
    signature_name       TEXT,                        -- typed signature (full name)
    submitted_at         DATETIME DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_screening_booking ON screening_forms(booking_id);
CREATE INDEX IF NOT EXISTS idx_screening_email   ON screening_forms(client_email);
