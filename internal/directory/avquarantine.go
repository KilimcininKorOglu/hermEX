package directory

import (
	"database/sql"
	"errors"
	"strings"
)

// QuarantineEntry is the metadata for a message the antivirus scanner held. The
// raw message lives on disk (config.QuarantinePath); only this is recorded in
// the directory for the admin panel to list.
type QuarantineEntry struct {
	Direction    string   // "inbound" or "outbound"
	MailFrom     string   // envelope sender
	Recipients   []string // envelope recipients
	Subject      string   // message subject (informational)
	VirusName    string   // the matched signature
	InfectedFile string   // attachment/part name when clamd reports one
	DomainID     int64    // scoping domain: recipient domain inbound, sender domain outbound
	CreatedAt    int64    // unix seconds
}

// QuarantineRecord is a stored QuarantineEntry with its assigned id and status.
type QuarantineRecord struct {
	ID int64
	QuarantineEntry
	Status string
}

// quarantineRcptSep joins the recipient list into the single rcpt_to column.
// Envelope addresses never contain a newline, so it round-trips cleanly.
const quarantineRcptSep = "\n"

// rowScanner is the row-reading surface common to *sql.Row and *sql.Rows.
type rowScanner interface{ Scan(dest ...any) error }

// QuarantineMessage records a quarantined message and returns its new id. The
// caller writes the raw bytes to config.QuarantinePath(id) using that id.
func (d *SQLDirectory) QuarantineMessage(e QuarantineEntry) (int64, error) {
	res, err := d.db.Exec(
		`INSERT INTO av_quarantine
		   (direction, mail_from, rcpt_to, subject, virus_name, infected_file, domain_id, created_at, status)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'held')`,
		e.Direction, e.MailFrom, strings.Join(e.Recipients, quarantineRcptSep),
		capString(e.Subject, 255), capString(e.VirusName, 255), capString(e.InfectedFile, 255),
		e.DomainID, e.CreatedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListQuarantine returns quarantine records newest first. With all=true every
// record is returned (a system admin); otherwise only records whose domain_id is
// in domainIDs (a domain admin's scope). all=false with no domainIDs returns
// nothing. limit defaults to 200 when <= 0.
func (d *SQLDirectory) ListQuarantine(domainIDs []int64, all bool, limit int) ([]QuarantineRecord, error) {
	if limit <= 0 {
		limit = 200
	}
	q := `SELECT id, direction, mail_from, rcpt_to, subject, virus_name, infected_file, domain_id, created_at, status
	        FROM av_quarantine`
	var args []any
	if !all {
		if len(domainIDs) == 0 {
			return nil, nil
		}
		ph := make([]string, len(domainIDs))
		for i, id := range domainIDs {
			ph[i] = "?"
			args = append(args, id)
		}
		q += " WHERE domain_id IN (" + strings.Join(ph, ",") + ")"
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QuarantineRecord
	for rows.Next() {
		r, err := scanQuarantine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetQuarantine returns one record by id (ok=false when absent).
func (d *SQLDirectory) GetQuarantine(id int64) (rec QuarantineRecord, ok bool, err error) {
	rec, err = scanQuarantine(d.db.QueryRow(
		`SELECT id, direction, mail_from, rcpt_to, subject, virus_name, infected_file, domain_id, created_at, status
		   FROM av_quarantine WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return QuarantineRecord{}, false, nil
	}
	if err != nil {
		return QuarantineRecord{}, false, err
	}
	return rec, true, nil
}

// DeleteQuarantine removes a record by id; the caller deletes the on-disk eml.
func (d *SQLDirectory) DeleteQuarantine(id int64) error {
	_, err := d.db.Exec(`DELETE FROM av_quarantine WHERE id = ?`, id)
	return err
}

// DomainOrgAdminEmails returns the login addresses of domainID's domain admins
// plus the org admins of that domain's organization, to notify them when a
// message is quarantined. A domain with no organization (org_id 0) yields just
// its domain admins.
func (d *SQLDirectory) DomainOrgAdminEmails(domainID int64) ([]string, error) {
	rows, err := d.db.Query(
		`SELECT DISTINCT u.username
		   FROM admin_roles ar JOIN users u ON u.id = ar.user_id
		  WHERE (ar.role = ? AND ar.scope_id = ?)
		     OR (ar.role = ? AND ar.scope_id = (SELECT org_id FROM domains WHERE id = ? AND org_id <> 0))`,
		AdminDomain, domainID, AdminOrg, domainID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var addr string
		if err := rows.Scan(&addr); err != nil {
			return nil, err
		}
		out = append(out, addr)
	}
	return out, rows.Err()
}

func scanQuarantine(s rowScanner) (QuarantineRecord, error) {
	var r QuarantineRecord
	var rcpt string
	if err := s.Scan(&r.ID, &r.Direction, &r.MailFrom, &rcpt, &r.Subject,
		&r.VirusName, &r.InfectedFile, &r.DomainID, &r.CreatedAt, &r.Status); err != nil {
		return QuarantineRecord{}, err
	}
	if rcpt != "" {
		r.Recipients = strings.Split(rcpt, quarantineRcptSep)
	}
	return r, nil
}

// capString caps s to at most n bytes for a VARCHAR-bounded column.
func capString(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
