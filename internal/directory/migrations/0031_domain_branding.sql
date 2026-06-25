-- Per-domain login-page branding: a JSON blob (app_name, logo_url, primary_color,
-- tagline, footer_text) the unauthenticated /branding endpoint serves by the
-- accessed domain, so each tenant can present its own name and colours on the login
-- screen. NULL means the domain inherits the global default branding.
-- Idempotent ADD COLUMN (MariaDB ADD COLUMN IF NOT EXISTS).
ALTER TABLE domains ADD COLUMN IF NOT EXISTS branding_json TEXT DEFAULT NULL;
