-- Migration 003: Rework slots to start-time-only, add treatment_duration_minutes to bookings.
-- Applied directly to the DB before server start. No-op on re-run.
SELECT 1;
