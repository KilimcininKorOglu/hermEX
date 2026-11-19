-- A scoped permission names its domain or organization by id, and the admin
-- panel's write check compares that id as text against the decimal form of the
-- domain id. A scope stored as "05" or "+5" therefore gave read access to domain
-- 5 (the list filter parses the number) but no write access. Rewrite every
-- numeric scope to its decimal form; where that form is already granted, the
-- duplicate row is removed. The column is ascii, so the decimal form is
-- converted to ascii before it is compared. Running this again changes nothing.
UPDATE IGNORE role_permissions
SET params = CONVERT(CAST(CAST(params AS UNSIGNED) AS CHAR) USING ascii)
WHERE params REGEXP '^[+]?[0-9]+$'
  AND params <> CONVERT(CAST(CAST(params AS UNSIGNED) AS CHAR) USING ascii);

DELETE FROM role_permissions
WHERE params REGEXP '^[+]?[0-9]+$'
  AND params <> CONVERT(CAST(CAST(params AS UNSIGNED) AS CHAR) USING ascii);
