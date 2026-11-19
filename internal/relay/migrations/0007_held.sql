-- A recipient held after a delivery attempt failed inside this server (a panic in
-- its own code) rather than at the recipient's side. A held row is never claimed
-- for delivery, never counts toward the attempt limit and never bounces: retrying
-- cannot succeed until the code is fixed, and bouncing would hand the sender a
-- failure that is not theirs. It waits on the mail-queue page until an
-- administrator retries or deletes it. Existing rows default to not held.

ALTER TABLE recipients ADD COLUMN held INTEGER NOT NULL DEFAULT 0;
