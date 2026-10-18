-- Migration 007: cancellation requests and refund tracking on bookings.
--
-- Clients can reschedule their own bookings directly, but cancellations (which
-- may involve a refund) are admin-actioned: the client requests one, the owner
-- is alerted, and the owner cancels + refunds from the admin area. These columns
-- record that lifecycle.
--
--   cancellation_requested_at : set when a client requests a cancellation (NULL otherwise)
--   refund_status             : none | refunded   (one-off bookings only)
--   refund_id                 : Stripe refund id once refunded

ALTER TABLE bookings ADD COLUMN cancellation_requested_at DATETIME;
ALTER TABLE bookings ADD COLUMN refund_status TEXT NOT NULL DEFAULT 'none';
ALTER TABLE bookings ADD COLUMN refund_id TEXT;
