package directory

import (
	"fmt"
	"strings"
)

// FetchmailEntry is one remote-account poll configuration for a local mailbox: the
// fetch-worker connects to SrcServer with the source credentials and delivers new mail
// into Mailbox. A zero SrcPort means the protocol/SSL default.
type FetchmailEntry struct {
	ID          int64
	Mailbox     string // local username mail is delivered to
	Active      bool
	SrcServer   string
	SrcPort     int
	SrcUser     string
	SrcPassword string
	Protocol    string // "POP3" or "IMAP"
	SrcFolder   string // IMAP source folder; ignored for POP3
	FetchAll    bool   // fetch already-seen messages too
	Keep        bool   // leave originals on the source server
	UseSSL      bool
	SSLVerify   bool
}

// Validate checks the fields a poll needs before the entry is stored: a recognized
// protocol and the non-empty server, source user, and local mailbox.
func (e FetchmailEntry) Validate() error {
	switch strings.ToUpper(e.Protocol) {
	case "POP3", "IMAP":
	default:
		return fmt.Errorf("fetchmail: protocol must be POP3 or IMAP, got %q", e.Protocol)
	}
	if strings.TrimSpace(e.SrcServer) == "" {
		return fmt.Errorf("fetchmail: source server is required")
	}
	if strings.TrimSpace(e.SrcUser) == "" {
		return fmt.Errorf("fetchmail: source user is required")
	}
	if strings.TrimSpace(e.Mailbox) == "" {
		return fmt.Errorf("fetchmail: local mailbox is required")
	}
	if e.SrcPort < 0 || e.SrcPort > 65535 {
		return fmt.Errorf("fetchmail: source port %d out of range", e.SrcPort)
	}
	return nil
}

const fetchmailCols = `id, mailbox, active, src_server, src_port, src_user, src_password,
	protocol, src_folder, fetchall, keep, use_ssl, ssl_verify`

// scanFetchmail reads one row in fetchmailCols order.
func scanFetchmail(s interface{ Scan(...any) error }) (FetchmailEntry, error) {
	var e FetchmailEntry
	var active, fetchall, keep, useSSL, sslVerify int
	if err := s.Scan(&e.ID, &e.Mailbox, &active, &e.SrcServer, &e.SrcPort, &e.SrcUser,
		&e.SrcPassword, &e.Protocol, &e.SrcFolder, &fetchall, &keep, &useSSL, &sslVerify); err != nil {
		return FetchmailEntry{}, err
	}
	e.Active, e.FetchAll, e.Keep, e.UseSSL, e.SSLVerify = active != 0, fetchall != 0, keep != 0, useSSL != 0, sslVerify != 0
	return e, nil
}

// ListFetchmail returns one mailbox's poll configurations, for the admin editor.
func (d *SQLDirectory) ListFetchmail(mailbox string) ([]FetchmailEntry, error) {
	rows, err := d.db.Query(`SELECT `+fetchmailCols+` FROM fetchmail WHERE mailbox = ? ORDER BY id`, mailbox)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FetchmailEntry
	for rows.Next() {
		e, err := scanFetchmail(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListActiveFetchmail returns every active poll configuration across all mailboxes, for
// the fetch-worker.
func (d *SQLDirectory) ListActiveFetchmail() ([]FetchmailEntry, error) {
	rows, err := d.db.Query(`SELECT ` + fetchmailCols + ` FROM fetchmail WHERE active <> 0 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FetchmailEntry
	for rows.Next() {
		e, err := scanFetchmail(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CreateFetchmail stores a new poll configuration and returns its id.
func (d *SQLDirectory) CreateFetchmail(e FetchmailEntry) (int64, error) {
	if err := e.Validate(); err != nil {
		return 0, err
	}
	res, err := d.db.Exec(
		`INSERT INTO fetchmail
			(mailbox, active, src_server, src_port, src_user, src_password,
			 protocol, src_folder, fetchall, keep, use_ssl, ssl_verify)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Mailbox, b2i(e.Active), e.SrcServer, e.SrcPort, e.SrcUser, e.SrcPassword,
		strings.ToUpper(e.Protocol), e.SrcFolder, b2i(e.FetchAll), b2i(e.Keep), b2i(e.UseSSL), b2i(e.SSLVerify))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DeleteFetchmail removes a poll configuration by id, reporting whether a row existed.
// The cascade clears its fetchmail_seen rows.
func (d *SQLDirectory) DeleteFetchmail(id int64) (bool, error) {
	res, err := d.db.Exec(`DELETE FROM fetchmail WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// FetchmailSeen returns the set of source unique-ids already delivered for a kept POP3
// entry, so the worker can skip them. (IMAP dedup uses the server's \Seen flag, not this
// table, so only POP3 needs it.)
func (d *SQLDirectory) FetchmailSeen(configID int64) (map[string]bool, error) {
	rows, err := d.db.Query(`SELECT uid FROM fetchmail_seen WHERE config_id = ?`, configID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		seen[uid] = true
	}
	return seen, rows.Err()
}

// MarkFetchmailSeen records source unique-ids as delivered for a kept POP3 entry. A uid
// already present is ignored, so a re-recorded id is harmless.
func (d *SQLDirectory) MarkFetchmailSeen(configID int64, uids []string) error {
	for _, uid := range uids {
		if _, err := d.db.Exec(`INSERT IGNORE INTO fetchmail_seen (config_id, uid) VALUES (?, ?)`, configID, uid); err != nil {
			return err
		}
	}
	return nil
}

// b2i maps a bool to the TINYINT the schema stores.
func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
