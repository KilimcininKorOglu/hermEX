-- Force a password change on next login after an admin reset. The admin
-- set-password endpoint flags the account (must_change_password = 1); the user
-- clears it by changing their own password. 0 = no change required (default).
-- Idempotent ADD COLUMN (MariaDB ADD COLUMN IF NOT EXISTS); applied once by the
-- runner and recorded in schema_migrations.
ALTER TABLE users ADD COLUMN IF NOT EXISTS must_change_password TINYINT NOT NULL DEFAULT 0;
