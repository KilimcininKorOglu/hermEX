-- password_policy_settings holds the minimum length, in characters, of every
-- password this server stores: an account created with one, an operator reset
-- and a self-service change. Each write reads the row, so a change applies to the
-- next password set, with no restart. Stored passwords are never re-checked, and a
-- sign-in that re-hashes one at the current work factor is not a new password.
-- With no row saved the minimum is 8. LDAP passwords are owned by the LDAP
-- server and never reach this rule.
CREATE TABLE IF NOT EXISTS password_policy_settings (
	id         TINYINT UNSIGNED NOT NULL,
	min_length INT NOT NULL DEFAULT 8,
	updated_at BIGINT NOT NULL,
	PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
