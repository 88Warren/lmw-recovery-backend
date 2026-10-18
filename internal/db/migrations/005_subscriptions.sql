-- Migration 005: Maintenance Plan subscriptions.
--
-- The Maintenance Plan is a recurring £80/month Stripe subscription. A client
-- subscribes once (paying for their first month, while also booking that
-- month's 2 sessions); the subscription then renews monthly until they cancel
-- via the Stripe Customer Portal. We mirror the subscription's state here, keyed
-- by the lower-cased client email, so the booking flow can tell whether someone
-- is an active paying member and confirm their sessions without charging again.
--
-- status mirrors Stripe: active | trialing | past_due | canceled | unpaid | incomplete.
-- We treat 'active' and 'trialing' as entitling the client to book.

CREATE TABLE IF NOT EXISTS subscriptions (
    id                     INTEGER PRIMARY KEY AUTOINCREMENT,
    client_email           TEXT    NOT NULL,            -- lower-cased, the entitlement key
    client_name            TEXT    NOT NULL,
    stripe_customer_id     TEXT    NOT NULL,
    stripe_subscription_id TEXT    NOT NULL,
    status                 TEXT    NOT NULL,            -- mirrors Stripe subscription status
    current_period_end     DATETIME,                    -- when the paid period ends (UTC)
    cancel_at_period_end   INTEGER NOT NULL DEFAULT 0,  -- 1 if set to cancel at period end
    created_at             DATETIME DEFAULT (datetime('now')),
    updated_at             DATETIME DEFAULT (datetime('now')),
    UNIQUE(stripe_subscription_id)
);

CREATE INDEX IF NOT EXISTS idx_subscriptions_email  ON subscriptions(client_email);
CREATE INDEX IF NOT EXISTS idx_subscriptions_customer ON subscriptions(stripe_customer_id);

-- Link bookings to the subscription that entitled them (NULL for one-off paid
-- bookings). Lets us show which sessions belong to a member's plan.
ALTER TABLE bookings ADD COLUMN subscription_id INTEGER;
