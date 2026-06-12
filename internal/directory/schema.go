package directory

// directoryDDL creates the account/domain directory, modeled faithfully on
// Gromox's MySQL schema (see contract-map/06): the columns the daemons read are
// preserved with their names and types, including the password VARCHAR(136)
// holding a crypt(3) string. The one representation change is display_type as a
// column standing in for Gromox's PR_DISPLAY_TYPE_EX row in user_properties
// (the object-class discriminator), keeping the login logic without the EAV
// table. Statements are idempotent so applying them to an existing DB is a no-op.
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
}

// Gromox address_status packing (mysql_adaptor.hpp): low nibble = user status,
// bits 4-5 = domain status. Only AF_USER_NORMAL may log in.
const (
	afUserNormal     = 0x00
	afUserSuspended  = 0x01
	afUserSharedMbox = 0x04
	afUserMask       = 0x0F
	afDomainMask     = 0x30
)

// dtMailuser is PR_DISPLAY_TYPE_EX == DT_MAILUSER; login requires it.
const dtMailuser = 0
