// Package config loads hermEX daemon configuration. Accounts are NOT configured
// here — they live in the directory database (see internal/directory); config
// holds only infrastructure settings (the directory DSN, listen addresses, the
// mailbox data root, and the announced hostname).
package config

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config is the JSON configuration shared by the mail daemons and the admin CLI.
type Config struct {
	DatabaseDSN    string `json:"database_dsn"`    // MariaDB DSN for the directory (go-sql-driver form)
	DataDir        string `json:"data_dir"`        // root under which mailbox/domain stores are created
	Hostname       string `json:"hostname"`        // announced in protocol greetings
	SMTPAddr       string `json:"smtp_addr"`       // MTA listen address (default ":25")
	POP3Addr       string `json:"pop3_addr"`       // POP3 listen address (default ":110")
	IMAPAddr       string `json:"imap_addr"`       // IMAP listen address (default ":143")
	WebmailAddr    string `json:"webmail_addr"`    // webmail HTTP listen address (default ":8080")
	DAVAddr        string `json:"dav_addr"`        // CalDAV/CardDAV HTTP listen address (default ":8080")
	ActiveSyncAddr string `json:"activesync_addr"` // ActiveSync HTTP listen address (default ":8080")
	EWSAddr        string `json:"ews_addr"`        // EWS (Exchange Web Services) HTTP listen address (default ":8080")
	MapiAddr       string `json:"mapi_addr"`       // MAPI/HTTP (native Outlook) HTTP listen address (default ":8080")
	TLSCert        string `json:"tls_cert"`        // PEM certificate (chain) for implicit-TLS/HTTPS listeners
	TLSKey         string `json:"tls_key"`         // PEM private key paired with tls_cert
	IMAPSAddr      string `json:"imaps_addr"`      // IMAP implicit-TLS listen address (e.g. ":993"); empty disables
	POP3SAddr      string `json:"pop3s_addr"`      // POP3 implicit-TLS listen address (e.g. ":995"); empty disables
	SMTPSAddr      string `json:"smtps_addr"`      // SMTP implicit-TLS listen address (e.g. ":465"); empty disables

	// Centralized logging (MongoDB). Empty MongoURI keeps logging to stderr only.
	MongoURI         string `json:"mongo_uri"`          // MongoDB URI for the central log store (empty = stderr only)
	LogDatabase      string `json:"log_database"`       // Mongo database holding the logs collection (default "hermex")
	LogRetentionDays int    `json:"log_retention_days"` // TTL window in days; 0 or negative keeps logs forever
	LogSpillDir      string `json:"log_spill_dir"`      // local dir for log batches buffered while Mongo is unreachable
}

// Load reads and validates a JSON config file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if c.DatabaseDSN == "" {
		return nil, fmt.Errorf("config: database_dsn is required")
	}
	if c.DataDir == "" {
		return nil, fmt.Errorf("config: data_dir is required")
	}
	return &c, nil
}

// TLSConfig builds a hardened server TLS configuration from the configured
// certificate and key files. It enforces a TLS 1.2 floor; the cipher suites are
// left to the Go runtime, whose TLS 1.2 defaults are already restricted to
// AEAD/forward-secret suites and whose TLS 1.3 suites are not configurable.
// It returns an error if no certificate is configured or the pair fails to load.
func (c *Config) TLSConfig() (*tls.Config, error) {
	if c.TLSCert == "" || c.TLSKey == "" {
		return nil, fmt.Errorf("config: tls_cert and tls_key are required for TLS")
	}
	cert, err := tls.LoadX509KeyPair(c.TLSCert, c.TLSKey)
	if err != nil {
		return nil, fmt.Errorf("config: load tls keypair: %w", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// TLSEnabled reports whether a certificate and key are both configured, i.e.
// whether listeners should terminate TLS rather than fall back to plaintext.
func (c *Config) TLSEnabled() bool {
	return c.TLSCert != "" && c.TLSKey != ""
}

// MaildirFor derives a user's mailbox directory
// (the internal spec §5.5): {DataDir}/user/{domain}/{localpart}. Collision
// suffixing (~N) is handled by the directory at provisioning time, not here.
func (c *Config) MaildirFor(address string) string {
	address = strings.ToLower(address)
	local, domain, _ := strings.Cut(address, "@")
	return filepath.Join(c.DataDir, "user", domain, local)
}

// HomedirFor derives a domain's public-store directory: {DataDir}/domain/{domain}.
func (c *Config) HomedirFor(domain string) string {
	return filepath.Join(c.DataDir, "domain", strings.ToLower(domain))
}
