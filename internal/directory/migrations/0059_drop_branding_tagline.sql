-- The login page no longer shows a tagline, so drop the key from every stored
-- branding blob, and clear a blob that held nothing else back to NULL so the domain
-- inherits the global default. Both statements match nothing on a second run.
UPDATE domains SET branding_json = JSON_REMOVE(branding_json, '$.tagline')
WHERE branding_json IS NOT NULL AND JSON_VALID(branding_json) AND JSON_EXISTS(branding_json, '$.tagline');
UPDATE domains SET branding_json = NULL
WHERE branding_json IS NOT NULL AND JSON_VALID(branding_json) AND JSON_LENGTH(branding_json) = 0;
