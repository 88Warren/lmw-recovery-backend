-- Migration 006: track the subscription's current billing period start.
--
-- The Maintenance Plan allows 2 sessions per billing period. To enforce that we
-- need the period window; 005 stored only current_period_end. This adds the
-- start so we can count a member's sessions within [start, end).
ALTER TABLE subscriptions ADD COLUMN current_period_start DATETIME;
