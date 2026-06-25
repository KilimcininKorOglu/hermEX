-- Distribution-list owner (the Exchange managedBy attribute): the address of the
-- user who owns the list. NULL means no owner. The address book exposes the owner's
-- EntryID to Outlook as PR_EMS_AB_OWNER, and the owner may manage the list's
-- membership from webmail.
-- Idempotent ADD COLUMN (MariaDB ADD COLUMN IF NOT EXISTS).
ALTER TABLE mlists ADD COLUMN IF NOT EXISTS owner VARCHAR(320) CHARACTER SET ascii DEFAULT NULL;
