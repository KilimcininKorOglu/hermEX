-- LDAP/AD downsync configuration beyond auth: a JSON document holding the per-field
-- profile sync map (which standard fields are enabled and their LDAP attribute
-- names, including the photo) and the group-to-distribution-list sync settings. An
-- empty or NULL value means only the account's existence and login are downsynced
-- (the prior behaviour). Idempotent ADD COLUMN.
ALTER TABLE ldap_config ADD COLUMN IF NOT EXISTS sync_config TEXT DEFAULT NULL;
