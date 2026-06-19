package directory

// directoryDDL creates the account/domain directory (see the internal spec): the
// columns the daemons read are preserved with their names and types, including
// the password VARCHAR(136) holding a crypt(3) string. The one representation
// change is display_type as a column standing in for the PR_DISPLAY_TYPE_EX row
// in user_properties (the object-class discriminator), keeping the login logic
// without the EAV table. Statements are idempotent so applying them to an
// existing DB is a no-op.
var directoryDDL = []string{
	`CREATE TABLE IF NOT EXISTS domains (
		id            INT UNSIGNED NOT NULL AUTO_INCREMENT,
		org_id        INT UNSIGNED NOT NULL DEFAULT 0,
		domainname    VARCHAR(255) CHARACTER SET ascii NOT NULL,
		homeserver    TINYINT UNSIGNED NOT NULL DEFAULT 0,
		homedir       VARCHAR(255) NOT NULL DEFAULT '',
		domain_status TINYINT NOT NULL DEFAULT 0,
		PRIMARY KEY (id),
		UNIQUE KEY domainname (domainname)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	`CREATE TABLE IF NOT EXISTS users (
		id             INT UNSIGNED NOT NULL AUTO_INCREMENT,
		username       VARCHAR(320) CHARACTER SET ascii NOT NULL,
		password       VARCHAR(136) CHARACTER SET ascii NOT NULL DEFAULT '',
		domain_id      INT UNSIGNED NOT NULL,
		homeserver     TINYINT UNSIGNED NOT NULL DEFAULT 0,
		maildir        VARCHAR(255) NOT NULL DEFAULT '',
		lang           VARCHAR(32) NOT NULL DEFAULT '',
		timezone       VARCHAR(64) NOT NULL DEFAULT '',
		privilege_bits INT UNSIGNED NOT NULL DEFAULT 0,
		address_status TINYINT NOT NULL DEFAULT 0,
		display_type   INT UNSIGNED NOT NULL DEFAULT 0,
		externid       VARBINARY(64) DEFAULT NULL,
		PRIMARY KEY (id),
		UNIQUE KEY username (username),
		KEY domain_id (domain_id),
		KEY maildir (maildir),
		CONSTRAINT users_domain_fk FOREIGN KEY (domain_id)
			REFERENCES domains (id) ON DELETE CASCADE ON UPDATE CASCADE
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	`CREATE TABLE IF NOT EXISTS aliases (
		id        INT UNSIGNED NOT NULL AUTO_INCREMENT,
		aliasname VARCHAR(320) CHARACTER SET ascii NOT NULL,
		mainname  VARCHAR(320) CHARACTER SET ascii NOT NULL,
		PRIMARY KEY (id),
		UNIQUE KEY aliasname (aliasname),
		KEY mainname (mainname)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	`CREATE TABLE IF NOT EXISTS altnames (
		user_id INT UNSIGNED NOT NULL,
		altname VARCHAR(320) CHARACTER SET ascii NOT NULL,
		PRIMARY KEY (user_id, altname),
		UNIQUE KEY altname (altname),
		CONSTRAINT altnames_user_fk FOREIGN KEY (user_id)
			REFERENCES users (id) ON DELETE CASCADE ON UPDATE CASCADE
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	// admin_roles grants a user administrative authority for the admin API. The
	// role is the tier — system (every org and domain), org (one organization),
	// or domain (one domain) — and scope_id names the org or domain it is bound to
	// (0 for system). A user may hold several roles.
	`CREATE TABLE IF NOT EXISTS admin_roles (
		user_id  INT UNSIGNED NOT NULL,
		role     VARCHAR(16) CHARACTER SET ascii NOT NULL,
		scope_id INT UNSIGNED NOT NULL DEFAULT 0,
		PRIMARY KEY (user_id, role, scope_id),
		CONSTRAINT admin_roles_user_fk FOREIGN KEY (user_id)
			REFERENCES users (id) ON DELETE CASCADE ON UPDATE CASCADE
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	// ldap_config holds one LDAP/AD bind-to-verify configuration per organization
	// (domains.org_id). A user whose externid is set authenticates against their
	// org's directory here; users with no externid stay on local crypt. Keyed by
	// org so a multi-tenant deployment points each org at its own directory.
	`CREATE TABLE IF NOT EXISTS ldap_config (
		org_id        INT UNSIGNED NOT NULL,
		uri           VARCHAR(255) NOT NULL DEFAULT '',
		start_tls     TINYINT NOT NULL DEFAULT 0,
		bind_dn       VARCHAR(255) NOT NULL DEFAULT '',
		bind_password VARCHAR(255) NOT NULL DEFAULT '',
		base_dn       VARCHAR(255) NOT NULL DEFAULT '',
		username_attr VARCHAR(64) NOT NULL DEFAULT 'mail',
		PRIMARY KEY (org_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
}

// address_status packing: low nibble = user status, bits 4-5 = domain status.
// Only AF_USER_NORMAL may log in.
const (
	afUserNormal     = 0x00
	afUserSuspended  = 0x01
	afUserSharedMbox = 0x04
	afUserMask       = 0x0F
	afDomainMask     = 0x30
)

// dtMailuser is PR_DISPLAY_TYPE_EX == DT_MAILUSER; login requires it.
const dtMailuser = 0
