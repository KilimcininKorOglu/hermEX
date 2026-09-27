-- Per-user interface preferences shared by webmail and the admin panel: the colour
-- theme ('' until the user picks one, then light, dark or system) and whether the
-- webmail inbox still shows its welcome banner. The language lives in users.lang.
ALTER TABLE users ADD COLUMN IF NOT EXISTS ui_theme VARCHAR(8) NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS show_welcome_banner TINYINT(1) NOT NULL DEFAULT 1;
