-- Migration 004: Add payment fields to bookings for one-off Stripe Checkout.
--
-- Bookings now go through a pending -> confirmed lifecycle gated by payment:
--   * created as status 'pending' / payment_status 'unpaid' when checkout starts
--     (the overlap query counts 'pending' so the slot is held), and
--   * flipped to 'confirmed' / 'paid' by the Stripe webhook on success.
--
-- All columns are additive with safe defaults so existing rows (historic
-- bookings created before payments) remain valid: they are treated as already
-- paid/confirmed via the defaults below, and the amount is backfilled to 0.
--
-- Runs exactly once (tracked in schema_migrations). ADD COLUMN is used rather
-- than a table rebuild to avoid disturbing existing data.

ALTER TABLE bookings ADD COLUMN amount_pence INTEGER NOT NULL DEFAULT 0;
ALTER TABLE bookings ADD COLUMN currency TEXT NOT NULL DEFAULT 'gbp';
-- Historic rows predate payments; mark them 'paid' so they are not swept.
ALTER TABLE bookings ADD COLUMN payment_status TEXT NOT NULL DEFAULT 'paid';
ALTER TABLE bookings ADD COLUMN stripe_session_id TEXT;
ALTER TABLE bookings ADD COLUMN stripe_payment_intent_id TEXT;
ALTER TABLE bookings ADD COLUMN paid_at DATETIME;

CREATE INDEX IF NOT EXISTS idx_bookings_stripe_session ON bookings(stripe_session_id);
CREATE INDEX IF NOT EXISTS idx_bookings_status ON bookings(status);
