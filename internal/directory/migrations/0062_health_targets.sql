-- The daemons the admin Live status page probes: a display name and the URL of
-- each daemon's /healthz endpoint. The list is operator-managed from the panel
-- and read on every probe, so a change applies without a restart.
CREATE TABLE IF NOT EXISTS health_targets (
  id INT UNSIGNED NOT NULL AUTO_INCREMENT,
  name VARCHAR(64) NOT NULL,
  url VARCHAR(512) NOT NULL,
  PRIMARY KEY (id),
  UNIQUE KEY health_targets_name (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
