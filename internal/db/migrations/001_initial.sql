-- ─── Contacts ───────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS contacts (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL,
    email      TEXT    NOT NULL,
    phone      TEXT,
    message    TEXT    NOT NULL,
    created_at DATETIME DEFAULT (datetime('now'))
);

-- ─── Availability slots ──────────────────────────────────────────────────────
-- Laura pre-populates these; clients can only book into existing open slots.
CREATE TABLE IF NOT EXISTS slots (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    starts_at  DATETIME NOT NULL,           -- UTC stored, display in local
    ends_at    DATETIME NOT NULL,
    available  INTEGER  NOT NULL DEFAULT 1, -- 1 = open, 0 = taken
    UNIQUE(starts_at)
);

-- ─── Bookings ────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS bookings (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    slot_id         INTEGER NOT NULL REFERENCES slots(id),
    treatment       TEXT    NOT NULL,       -- e.g. "Sports Massage"
    client_name     TEXT    NOT NULL,
    client_email    TEXT    NOT NULL,
    client_phone    TEXT,
    notes           TEXT,                   -- anything the client wants to add
    status          TEXT    NOT NULL DEFAULT 'pending',  -- pending | confirmed | cancelled
    brevo_sent      INTEGER NOT NULL DEFAULT 0,          -- 1 once health questionnaire fired
    created_at      DATETIME DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_bookings_slot    ON bookings(slot_id);
CREATE INDEX IF NOT EXISTS idx_bookings_email   ON bookings(client_email);
CREATE INDEX IF NOT EXISTS idx_slots_starts_at  ON slots(starts_at);
