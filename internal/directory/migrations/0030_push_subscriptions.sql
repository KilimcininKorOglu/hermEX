-- Web push subscriptions: one row per browser PushManager subscription so the
-- webmail poll loop can deliver a new-mail web push to a user's devices. Keyed by
-- the push endpoint (unique per browser subscription); replacing a stale
-- subscription is an upsert on that key.
CREATE TABLE IF NOT EXISTS push_subscriptions (
  endpoint   VARCHAR(512) NOT NULL PRIMARY KEY,
  email      VARCHAR(255) NOT NULL,
  p256dh     VARCHAR(255) NOT NULL,
  auth       VARCHAR(255) NOT NULL,
  created_at BIGINT       NOT NULL,
  INDEX idx_push_subscriptions_email (email)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
