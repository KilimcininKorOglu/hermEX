package directory

// directoryDDL creates the account/domain directory (see the internal spec): the
// columns the daemons read are preserved with their names and types, including
// the password VARCHAR(136) holding a crypt(3) string. The one representation
// change is display_type as a column standing in for the PR_DISPLAY_TYPE_EX row
// in user_properties (the object-class discriminator), keeping the login logic
// without the EAV table. Statements are idempotent so applying them to an
// existing DB is a no-op.
var directoryDDL = []string{
	// orgs is an organization: a named grouping of domains for multi-tenant
	// administration. A domain's membership is its domains.org_id (a soft
	// reference, no FK — DeleteOrg detaches its domains explicitly), and the
	// org-scoped configuration (ldap_config, sync_policy, org-admin grants) keys
	// on the same id. Id 0 is the reserved "organizationless" sentinel and is
	// never a real row (AUTO_INCREMENT starts at 1), so the global-default
	// sync_policy row (org_id 0) is never mistaken for an organization.
	`CREATE TABLE IF NOT EXISTS orgs (
		id          INT UNSIGNED NOT NULL AUTO_INCREMENT,
		name        VARCHAR(32) NOT NULL,
		description VARCHAR(255) NOT NULL DEFAULT '',
		PRIMARY KEY (id),
		UNIQUE KEY name (name)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	// domain_status gates the whole domain: 0 = active, 1 = suspended. It is read
	// directly (the domains row / a JOIN) by every authority point — login
	// (Authenticate), local-delivery (IsLocalDomain), and the address-book queries
	// — so setting it suspends the domain everywhere with no per-user fan-out.
	// max_user caps the domain's mailbox count (0 = unlimited), enforced at user
	// creation. title/address/admin_name/tel are descriptive contact metadata.
	`CREATE TABLE IF NOT EXISTS domains (
		id            INT UNSIGNED NOT NULL AUTO_INCREMENT,
		org_id        INT UNSIGNED NOT NULL DEFAULT 0,
		domainname    VARCHAR(255) CHARACTER SET ascii NOT NULL,
		homeserver    TINYINT UNSIGNED NOT NULL DEFAULT 0,
		homedir       VARCHAR(255) NOT NULL DEFAULT '',
		domain_status TINYINT NOT NULL DEFAULT 0,
		max_user      INT UNSIGNED NOT NULL DEFAULT 0,
		title         VARCHAR(128) NOT NULL DEFAULT '',
		address       VARCHAR(128) NOT NULL DEFAULT '',
		admin_name    VARCHAR(32) NOT NULL DEFAULT '',
		tel           VARCHAR(64) NOT NULL DEFAULT '',
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

	// forwards holds a user's mail-forward directive: a single destination address
	// and the forward_type that selects whether the original is also kept locally
	// (0 = CC: keep a local copy and forward one; 1 = Redirect: forward only). One
	// row per user (username UNIQUE). username is a plain address string with no FK
	// (as aliases.mainname is), so DeleteUser removes the row explicitly.
	`CREATE TABLE IF NOT EXISTS forwards (
		id           INT UNSIGNED NOT NULL AUTO_INCREMENT,
		username     VARCHAR(320) CHARACTER SET ascii NOT NULL,
		forward_type TINYINT NOT NULL DEFAULT 0,
		destination  VARCHAR(320) CHARACTER SET ascii NOT NULL,
		PRIMARY KEY (id),
		UNIQUE KEY username (username)
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

	// roles is a named administrative role: a label carrying a set of fine-grained
	// permissions, assignable to any number of users. It expresses authority the
	// coarse admin_roles tiers cannot — read-only access, per-domain purge,
	// password reset — and is the source of truth for those capabilities. The two
	// models coexist: the permission resolver unions a role's permissions with the
	// equivalents of a user's direct admin_roles grants.
	`CREATE TABLE IF NOT EXISTS roles (
		id          INT UNSIGNED NOT NULL AUTO_INCREMENT,
		name        VARCHAR(64) NOT NULL,
		description VARCHAR(255) NOT NULL DEFAULT '',
		PRIMARY KEY (id),
		UNIQUE KEY name (name)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	// role_permissions is the permission set of a role: one row per granted
	// capability. permission is the capability name; params scopes it — the
	// decimal id of one org or domain, "*" for all of them, or empty for an
	// unscoped capability. A role may hold several permissions.
	`CREATE TABLE IF NOT EXISTS role_permissions (
		role_id    INT UNSIGNED NOT NULL,
		permission VARCHAR(32) CHARACTER SET ascii NOT NULL,
		params     VARCHAR(64) CHARACTER SET ascii NOT NULL DEFAULT '',
		PRIMARY KEY (role_id, permission, params),
		CONSTRAINT role_permissions_role_fk FOREIGN KEY (role_id)
			REFERENCES roles (id) ON DELETE CASCADE ON UPDATE CASCADE
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	// user_roles assigns named roles to users (many-to-many). Both sides cascade:
	// deleting a user or a role removes its assignments.
	`CREATE TABLE IF NOT EXISTS user_roles (
		user_id INT UNSIGNED NOT NULL,
		role_id INT UNSIGNED NOT NULL,
		PRIMARY KEY (user_id, role_id),
		CONSTRAINT user_roles_user_fk FOREIGN KEY (user_id)
			REFERENCES users (id) ON DELETE CASCADE ON UPDATE CASCADE,
		CONSTRAINT user_roles_role_fk FOREIGN KEY (role_id)
			REFERENCES roles (id) ON DELETE CASCADE ON UPDATE CASCADE
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	// user_properties is the per-user MAPI property store (EAV): one row per
	// (user, property tag, order). It holds the directory-visible properties that
	// are not promoted to a users column — display name, nickname, the contact
	// fields, address-book cloak bits — keyed by the full 32-bit proptag. String
	// values live in propval_str, binary in propval_bin. order_id is 1 for a
	// single-valued property. (display_type stays a users column and is NOT
	// duplicated here.)
	`CREATE TABLE IF NOT EXISTS user_properties (
		user_id     INT UNSIGNED NOT NULL,
		proptag     INT UNSIGNED NOT NULL,
		order_id    INT UNSIGNED NOT NULL DEFAULT 1,
		propval_bin VARBINARY(4096) DEFAULT NULL,
		propval_str VARCHAR(4096) DEFAULT NULL,
		PRIMARY KEY (user_id, proptag, order_id),
		CONSTRAINT user_properties_user_fk FOREIGN KEY (user_id)
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

	// sync_policy holds the server-wide default ActiveSync device policy as a JSON
	// object of policy field → value, keyed by organization (the single global row is
	// org_id 0). It is the base a mailbox's per-user override is merged over at device
	// provisioning. Keyed by org so a multi-tenant deployment can later default each
	// org independently.
	`CREATE TABLE IF NOT EXISTS sync_policy (
		org_id INT UNSIGNED NOT NULL DEFAULT 0,
		policy TEXT NOT NULL,
		PRIMARY KEY (org_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	// fetchmail holds a local user's remote-account poll configurations: the
	// fetch-worker periodically connects to each active entry's source POP3/IMAP
	// server and delivers new mail into the local mailbox. mailbox is the local
	// username (a plain string like forwards, removed explicitly on user delete).
	`CREATE TABLE IF NOT EXISTS fetchmail (
		id           INT UNSIGNED NOT NULL AUTO_INCREMENT,
		mailbox      VARCHAR(320) CHARACTER SET ascii NOT NULL,
		active       TINYINT NOT NULL DEFAULT 1,
		src_server   VARCHAR(255) NOT NULL,
		src_port     INT UNSIGNED NOT NULL DEFAULT 0,
		src_user     VARCHAR(255) NOT NULL,
		src_password VARCHAR(255) NOT NULL DEFAULT '',
		protocol     VARCHAR(8) NOT NULL DEFAULT 'POP3',
		src_folder   VARCHAR(255) NOT NULL DEFAULT 'INBOX',
		fetchall     TINYINT NOT NULL DEFAULT 0,
		keep         TINYINT NOT NULL DEFAULT 0,
		use_ssl      TINYINT NOT NULL DEFAULT 1,
		ssl_verify   TINYINT NOT NULL DEFAULT 1,
		PRIMARY KEY (id),
		KEY fetchmail_mailbox (mailbox)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	// fetchmail_seen records the source-message identifiers (POP3 UIDL or IMAP UID)
	// already delivered for a kept (non-deleting) entry, so a later poll does not
	// re-deliver them. Rows are scoped to a fetchmail entry and cascade on its delete.
	`CREATE TABLE IF NOT EXISTS fetchmail_seen (
		config_id INT UNSIGNED NOT NULL,
		uid       VARCHAR(255) NOT NULL,
		PRIMARY KEY (config_id, uid),
		CONSTRAINT fetchmail_seen_fk FOREIGN KEY (config_id)
			REFERENCES fetchmail (id) ON DELETE CASCADE ON UPDATE CASCADE
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	// mlists is a distribution list: a users row (display_type = DT_DISTLIST, no
	// maildir or password — it cannot log in) extended with its expansion policy.
	// list_type selects how membership is computed (normal = the explicit
	// associations rows, domain = every mailuser in domain_id); list_privilege
	// gates who may send to the list. listname references the users row, so the
	// list's GAL entry, hide mask, and properties come from the shared tables.
	`CREATE TABLE IF NOT EXISTS mlists (
		id             INT UNSIGNED NOT NULL AUTO_INCREMENT,
		listname       VARCHAR(320) CHARACTER SET ascii NOT NULL,
		domain_id      INT UNSIGNED NOT NULL,
		list_type      TINYINT NOT NULL DEFAULT 0,
		list_privilege TINYINT NOT NULL DEFAULT 0,
		PRIMARY KEY (id),
		UNIQUE KEY listname (listname),
		KEY domain_id (domain_id),
		CONSTRAINT mlists_user_fk FOREIGN KEY (listname)
			REFERENCES users (username) ON DELETE CASCADE ON UPDATE CASCADE
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	// associations holds the explicit members of a normal-type list: one member
	// address per row. A member may itself be a list, which the expander resolves
	// recursively under a loop guard.
	`CREATE TABLE IF NOT EXISTS associations (
		list_id  INT UNSIGNED NOT NULL,
		username VARCHAR(320) CHARACTER SET ascii NOT NULL,
		PRIMARY KEY (list_id, username),
		KEY list_id (list_id),
		CONSTRAINT associations_list_fk FOREIGN KEY (list_id)
			REFERENCES mlists (id) ON DELETE CASCADE ON UPDATE CASCADE
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	// specifieds names the senders permitted to post to a list whose privilege is
	// "specified": a full address or a bare domain matches.
	`CREATE TABLE IF NOT EXISTS specifieds (
		id       INT UNSIGNED NOT NULL AUTO_INCREMENT,
		list_id  INT UNSIGNED NOT NULL,
		username VARCHAR(320) CHARACTER SET ascii NOT NULL,
		PRIMARY KEY (id),
		KEY list_id (list_id),
		CONSTRAINT specifieds_list_fk FOREIGN KEY (list_id)
			REFERENCES mlists (id) ON DELETE CASCADE ON UPDATE CASCADE
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

	// Additive column upgrades for databases created before these columns existed.
	// CREATE TABLE above carries the canonical definition for a fresh database;
	// these idempotent ALTERs (MariaDB ADD COLUMN IF NOT EXISTS) bring an existing
	// domains table up to date without a migration framework. A no-op once applied.
	`ALTER TABLE domains ADD COLUMN IF NOT EXISTS max_user INT UNSIGNED NOT NULL DEFAULT 0`,
	`ALTER TABLE domains ADD COLUMN IF NOT EXISTS title VARCHAR(128) NOT NULL DEFAULT ''`,
	`ALTER TABLE domains ADD COLUMN IF NOT EXISTS address VARCHAR(128) NOT NULL DEFAULT ''`,
	`ALTER TABLE domains ADD COLUMN IF NOT EXISTS admin_name VARCHAR(32) NOT NULL DEFAULT ''`,
	`ALTER TABLE domains ADD COLUMN IF NOT EXISTS tel VARCHAR(64) NOT NULL DEFAULT ''`,
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

// display_type (PR_DISPLAY_TYPE_EX) values. dtMailuser is a normal mailbox user
// and login requires it; dtDistlist is a distribution list (a users row with no
// mailbox, expanded by the address book and the MTA). dtRoom/dtEquipment are
// resource mailboxes; dtContact is a mail contact (an external address with no
// mailbox). All five are address-book recipients and classify the named address
// lists (All Users/Distribution Lists/Contacts/Rooms/Equipment).
const (
	dtMailuser  = 0
	dtDistlist  = 1
	dtContact   = 6 // DT_REMOTE_MAILUSER
	dtRoom      = 7 // DT_ROOM
	dtEquipment = 8 // DT_EQUIPMENT
)
