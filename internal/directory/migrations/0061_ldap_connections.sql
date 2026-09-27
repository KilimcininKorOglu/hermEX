-- ldap_connections holds one reachable LDAP/AD directory: where it lives, how to
-- reach it, the service account that searches it, and the attribute a login is
-- matched against. The bind password is wrapped at rest with the directory key
-- secret (the "enc:v1:" prefix) whenever one is configured, hence the width.
-- legacy_org_id records the per-organization ldap_config row a connection was
-- copied from, so copying again finds it and inserts nothing.
CREATE TABLE IF NOT EXISTS ldap_connections (
	id            INT UNSIGNED NOT NULL AUTO_INCREMENT,
	name          VARCHAR(64)  NOT NULL,
	uri           VARCHAR(255) NOT NULL DEFAULT '',
	start_tls     TINYINT      NOT NULL DEFAULT 0,
	bind_dn       VARCHAR(255) NOT NULL DEFAULT '',
	bind_password VARCHAR(512) NOT NULL DEFAULT '',
	base_dn       VARCHAR(255) NOT NULL DEFAULT '',
	username_attr VARCHAR(64)  NOT NULL DEFAULT 'mail',
	legacy_org_id INT UNSIGNED NULL DEFAULT NULL,
	PRIMARY KEY (id),
	UNIQUE KEY ldap_connections_name (name),
	UNIQUE KEY ldap_connections_legacy_org (legacy_org_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- ldap_bindings attaches one domain to one connection with that domain's mapping:
-- which profile fields sync and from which attribute, the alias attribute, the
-- group and contact switches, and an optional base DN override (sync_config, a
-- JSON document). A domain binds to at most one connection. Deleting either the
-- connection or the domain removes the binding.
CREATE TABLE IF NOT EXISTS ldap_bindings (
	id            INT UNSIGNED NOT NULL AUTO_INCREMENT,
	connection_id INT UNSIGNED NOT NULL,
	domain_id     INT UNSIGNED NOT NULL,
	preset        VARCHAR(32)  NOT NULL DEFAULT '',
	sync_config   TEXT,
	PRIMARY KEY (id),
	UNIQUE KEY ldap_bindings_domain (domain_id),
	INDEX idx_ldap_bindings_connection (connection_id),
	CONSTRAINT ldap_bindings_connection_fk FOREIGN KEY (connection_id)
		REFERENCES ldap_connections (id) ON DELETE CASCADE,
	CONSTRAINT ldap_bindings_domain_fk FOREIGN KEY (domain_id)
		REFERENCES domains (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Every configured organization directory becomes a connection. An empty URI is
-- how an operator turned the directory off, so it is not copied.
INSERT IGNORE INTO ldap_connections
	(name, uri, start_tls, bind_dn, bind_password, base_dn, username_attr, legacy_org_id)
SELECT CONCAT('org-', l.org_id), l.uri, l.start_tls, l.bind_dn, l.bind_password, l.base_dn, l.username_attr, l.org_id
  FROM ldap_config l
 WHERE l.uri <> '';

-- Every domain of such an organization is bound to its connection with the
-- organization's mapping. Contact sync filed every contact under one domain, so it
-- stays on only for the binding of that domain; every other binding has the switch
-- removed (an absent key reads as off). A document without a contact domain never
-- ran contact sync, so it loses the switch as well.
INSERT IGNORE INTO ldap_bindings (connection_id, domain_id, preset, sync_config)
SELECT c.id, d.id, '',
       CASE
         WHEN l.sync_config IS NULL OR l.sync_config = '' THEN NULL
         WHEN JSON_EXISTS(l.sync_config, '$.contactDomain') = 1
          AND LOWER(TRIM(JSON_VALUE(l.sync_config, '$.contactDomain'))) = CONVERT(d.domainname USING utf8mb4) THEN l.sync_config
         WHEN JSON_EXISTS(l.sync_config, '$.syncContacts') = 1 THEN JSON_REMOVE(l.sync_config, '$.syncContacts')
         ELSE l.sync_config
       END
  FROM domains d
  JOIN ldap_config l ON l.org_id = d.org_id
  JOIN ldap_connections c ON c.legacy_org_id = l.org_id;
