-- Per-tenant outgoing display-name templates. The From display name on outgoing
-- mail is rebuilt from these templates, separately for intra-tenant (internal) and
-- external recipients, so a tenant can present e.g. "Ali Veli (Acme - Sales)"
-- externally while showing "Ali Veli (Sales)" internally. The placeholders {name},
-- {company}, {title}, {department}, {office} are filled from the sender's directory
-- profile. An empty template leaves the From display name untouched for that
-- direction. Idempotent ADD COLUMN.
ALTER TABLE domains ADD COLUMN IF NOT EXISTS outgoing_name_tpl_internal VARCHAR(255) NOT NULL DEFAULT '';
ALTER TABLE domains ADD COLUMN IF NOT EXISTS outgoing_name_tpl_external VARCHAR(255) NOT NULL DEFAULT '';
