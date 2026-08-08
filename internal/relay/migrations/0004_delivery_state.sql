-- Per-recipient delivery state, so a bookkeeping failure can never become a
-- second live SMTP transaction. delivery_started is stamped before the delivery
-- attempt and cleared when it fails; delivered is set once the mail exchanger has
-- accepted the message, leaving only the settle to redo. Both default to 0, so a
-- row queued before this migration reads as "not started", which is exactly what
-- it is.

ALTER TABLE recipients ADD COLUMN delivery_started INTEGER NOT NULL DEFAULT 0;
ALTER TABLE recipients ADD COLUMN delivered        INTEGER NOT NULL DEFAULT 0;
