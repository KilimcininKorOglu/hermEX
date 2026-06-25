-- A distribution list mastered by an LDAP/AD group sync carries the group's stable
-- identifier (objectGUID/entryUUID); a NULL externid means the list is managed
-- locally. The group sync is the sole authority over a mastered list's owner and
-- membership, so the webmail and admin editing paths refuse to mutate one (the sync
-- would otherwise overwrite the manual edit on its next run). Idempotent ADD COLUMN.
ALTER TABLE mlists ADD COLUMN IF NOT EXISTS externid VARBINARY(64) DEFAULT NULL;
