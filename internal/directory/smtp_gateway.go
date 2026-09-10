package directory

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The transport a gateway connection uses, stored in the encryption column.
//
//   - GatewaySTARTTLS upgrades a plain connection and REQUIRES the upgrade to succeed
//     with a valid certificate, so a gateway that stops offering STARTTLS fails the
//     delivery rather than sending the mail in the clear.
//   - GatewayImplicitTLS wraps the connection in TLS from the first byte (the submission
//     port 465 shape).
//   - GatewayNoTLS sends in the clear and is only for a gateway reached over a trusted
//     link the operator controls.
const (
	GatewaySTARTTLS    = "starttls"
	GatewayImplicitTLS = "tls"
	GatewayNoTLS       = "none"
)

// SMTPGateway is one outbound gateway (smart-host) configuration: where outgoing mail is
// handed instead of being delivered to each recipient domain's own mail exchanger, and the
// credentials to authenticate there. Enabled false keeps the row but leaves delivery on the
// direct path, so an operator can turn a configured gateway off without losing its settings.
type SMTPGateway struct {
	Enabled    bool
	Host       string
	Port       int
	Encryption string
	Username   string
	Password   string
}

// GlobalGateway is the domain key of the gateway every sending domain inherits. A row keyed
// by a domain name overrides it for mail whose envelope sender is in that domain.
const GlobalGateway = ""

// validGatewayEncryption reports whether a stored encryption value is one this server can
// dial. An unknown value is refused on write rather than silently downgraded at delivery.
func validGatewayEncryption(enc string) bool {
	switch enc {
	case GatewaySTARTTLS, GatewayImplicitTLS, GatewayNoTLS:
		return true
	}
	return false
}

// GetSMTPGateway returns one gateway configuration, found=false when the key has no row.
// Pass GlobalGateway for the default, or a domain name for that domain's override.
func (d *SQLDirectory) GetSMTPGateway(domain string) (SMTPGateway, bool, error) {
	var g SMTPGateway
	var stored string
	err := d.db.QueryRow(
		`SELECT enabled, host, port, encryption, username, password FROM smtp_gateways WHERE domain = ?`,
		gatewayKey(domain)).Scan(&g.Enabled, &g.Host, &g.Port, &g.Encryption, &g.Username, &stored)
	if errors.Is(err, sql.ErrNoRows) {
		return SMTPGateway{}, false, nil
	}
	if err != nil {
		return SMTPGateway{}, false, err
	}
	password, _, err := d.unwrapKey(wrapGateway, stored)
	if err != nil {
		return SMTPGateway{}, false, err
	}
	g.Password = password
	return g, true, nil
}

// SetSMTPGateway stores or replaces one gateway configuration. The password is wrapped at
// rest, so the stored row carries ciphertext wherever a key secret is configured. An
// unusable configuration (an enabled gateway with no host, a port outside 1-65535, an
// unknown encryption) is refused, because the MTA would otherwise poll it up and fail every
// outbound delivery.
func (d *SQLDirectory) SetSMTPGateway(domain string, g SMTPGateway) error {
	g.Host = strings.TrimSpace(g.Host)
	if g.Encryption == "" {
		g.Encryption = GatewaySTARTTLS
	}
	if !validGatewayEncryption(g.Encryption) {
		return fmt.Errorf("directory: unknown gateway encryption %q", g.Encryption)
	}
	if g.Port <= 0 || g.Port > 65535 {
		return fmt.Errorf("directory: gateway port %d is out of range", g.Port)
	}
	if g.Enabled && g.Host == "" {
		return errors.New("directory: an enabled gateway needs a host")
	}
	stored, err := d.wrapKey(wrapGateway, g.Password)
	if err != nil {
		return err
	}
	_, err = d.db.Exec(
		`INSERT INTO smtp_gateways (domain, enabled, host, port, encryption, username, password, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE enabled = VALUES(enabled), host = VALUES(host), port = VALUES(port),
		   encryption = VALUES(encryption), username = VALUES(username), password = VALUES(password),
		   updated_at = VALUES(updated_at)`,
		gatewayKey(domain), g.Enabled, g.Host, g.Port, g.Encryption, g.Username, stored, time.Now().UnixMilli())
	return err
}

// DeleteSMTPGateway removes one gateway configuration, reporting whether a row went. Removing
// a domain's row returns that domain to the global gateway; removing the global row returns
// every domain to direct delivery.
func (d *SQLDirectory) DeleteSMTPGateway(domain string) (bool, error) {
	res, err := d.db.Exec(`DELETE FROM smtp_gateways WHERE domain = ?`, gatewayKey(domain))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ListSMTPGateways returns every stored gateway keyed by sending domain, with GlobalGateway
// holding the default. The MTA's settings poll reads the whole set in this one call, so a
// changed gateway applies without a restart. A disabled row is omitted: the poll installs
// exactly what delivery should use.
func (d *SQLDirectory) ListSMTPGateways() (map[string]SMTPGateway, error) {
	rows, err := d.db.Query(
		`SELECT domain, enabled, host, port, encryption, username, password FROM smtp_gateways`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]SMTPGateway)
	for rows.Next() {
		var domain, stored string
		var g SMTPGateway
		if err := rows.Scan(&domain, &g.Enabled, &g.Host, &g.Port, &g.Encryption, &g.Username, &stored); err != nil {
			return nil, err
		}
		if !g.Enabled {
			continue
		}
		password, _, err := d.unwrapKey(wrapGateway, stored)
		if err != nil {
			return nil, err
		}
		g.Password = password
		out[domain] = g
	}
	return out, rows.Err()
}

// gatewayKey normalizes a domain key: lower-cased and trimmed, so a gateway saved as
// "Example.Test" is found by the sender domain the relay extracts.
func gatewayKey(domain string) string {
	return strings.ToLower(strings.TrimSpace(domain))
}
