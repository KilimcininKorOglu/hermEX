// Package config loads hermEX daemon configuration. Accounts are NOT configured
// here — they live in the directory database (see internal/directory); config
// holds only infrastructure settings (the directory DSN, listen addresses, the
// mailbox data root, and the announced hostname).
package config

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
)

// Config is the JSON configuration shared by the mail daemons and the admin CLI.
type Config struct {
	DatabaseDSN    string   `json:"database_dsn"`    // MariaDB DSN for the directory (go-sql-driver form)
	DataDir        string   `json:"data_dir"`        // root under which mailbox/domain stores are created (control root for the relay spool and domain dirs)
	DataPartitions []string `json:"data_partitions"` // optional mailbox-placement pool; empty spreads nothing (all mailboxes under DataDir)
	Hostname       string   `json:"hostname"`        // announced in protocol greetings
	SMTPAddr       string   `json:"smtp_addr"`       // MTA listen address (default ":25")
	POP3Addr       string   `json:"pop3_addr"`       // POP3 listen address (default ":110")
	IMAPAddr       string   `json:"imap_addr"`       // IMAP listen address (default ":143")
	WebmailAddr    string   `json:"webmail_addr"`    // webmail HTTP listen address (default ":8080")
	Webmail2Addr   string   `json:"webmail2_addr"`   // webmail2 (SPA) HTTP listen address (default ":8080")
	Webmail2Secret string   `json:"webmail2_secret"` // signing key for webmail2 session cookies; required to serve webmail2
	Webmail2Dist   string   `json:"webmail2_dist"`   // filesystem path to the built webmail2 SPA (dist) directory to serve
	DAVAddr        string   `json:"dav_addr"`        // CalDAV/CardDAV HTTP listen address (default ":8080")
	ActiveSyncAddr string   `json:"activesync_addr"` // ActiveSync HTTP listen address (default ":8080")
	EWSAddr        string   `json:"ews_addr"`        // EWS (Exchange Web Services) HTTP listen address (default ":8080")
	MapiAddr       string   `json:"mapi_addr"`       // MAPI/HTTP (native Outlook) HTTP listen address (default ":8080")
	AdminAddr      string   `json:"admin_addr"`      // admin API HTTP listen address (default ":8081")
	AdminSecret    string   `json:"admin_secret"`    // signing key for admin session tokens; required to serve the admin API
	DigestSecret   string   `json:"digest_secret"`   // signing key for quarantine-digest release links; the MTA mints and webmail verifies — both daemons must share it, empty disables release links
	HealthAddr     string   `json:"health_addr"`     // per-daemon /healthz listen address (e.g. ":8090"); empty disables the health endpoint
	TLSCert        string   `json:"tls_cert"`        // PEM certificate (chain) for implicit-TLS/HTTPS listeners
	TLSKey         string   `json:"tls_key"`         // PEM private key paired with tls_cert
	IMAPSAddr      string   `json:"imaps_addr"`      // IMAP implicit-TLS listen address (e.g. ":993"); empty disables
	POP3SAddr      string   `json:"pop3s_addr"`      // POP3 implicit-TLS listen address (e.g. ":995"); empty disables
	SMTPSAddr      string   `json:"smtps_addr"`      // SMTP implicit-TLS listen address (e.g. ":465"); empty disables

	// Gateway front door. Only the gateway daemon reads these; every other daemon
	// ignores them, so they stay optional in the one shared config. Each empty field
	// falls back to a built-in default (see cmd/gateway resolveGateway): the listen
	// address and the backend base URLs default to the compose service names.
	GatewayAddr              string `json:"gateway_addr"`               // gateway listen address (default ":8080")
	GatewayBackendMapi       string `json:"gateway_backend_mapi"`       // MAPI/RPC backend base URL (default "http://mapi:8080")
	GatewayBackendEws        string `json:"gateway_backend_ews"`        // EWS/autodiscover backend base URL (default "http://ews:8080")
	GatewayBackendActiveSync string `json:"gateway_backend_activesync"` // ActiveSync backend base URL (default "http://activesync:8080")
	GatewayBackendDAV        string `json:"gateway_backend_dav"`        // CalDAV/CardDAV backend base URL (default "http://dav:8080")
	GatewayBackendWebmail    string `json:"gateway_backend_webmail"`    // webmail backend base URL (default "http://webmail:8080")
	ACMEStorage              string `json:"acme_storage"`               // CertMagic state dir in acme mode (default DataDir/acme)
	ACMECARoot               string `json:"acme_ca_root"`               // PEM bundle trusting a private ACME CA (dev/pebble only; empty = system roots)

	// Centralized logging (MongoDB). Empty MongoURI keeps logging to stderr only.
	MongoURI         string `json:"mongo_uri"`          // MongoDB URI for the central log store (empty = stderr only)
	LogDatabase      string `json:"log_database"`       // Mongo database holding the logs collection (default "hermex")
	LogRetentionDays int    `json:"log_retention_days"` // seeds the admin-managed log retention (days) on first run; 0 or negative keeps logs forever
	LogSpillDir      string `json:"log_spill_dir"`      // local dir for log batches buffered while Mongo is unreachable

	// Antivirus (ClamAV clamd). Empty ClamdAddr disables AV scanning entirely; the
	// mail path never hard-depends on it (per-domain toggles gate scanning, and an
	// unreachable daemon temp-fails inbound but fails open on authenticated submission).
	ClamdAddr string `json:"clamd_addr"` // clamd INSTREAM socket (e.g. "tcp://clamav:3310" or "unix:///run/clamav/clamd.sock"); empty = no AV scanning

	// Push notifications (central notify daemon). The notify daemon relays mailbox-change
	// wake signals so a long-poll consumer reacts the instant a write lands instead of on
	// its next poll tick. The mail path never hard-depends on it: an empty NotifyURL (or an
	// unreachable daemon) leaves every consumer on its existing poll cadence — push is a
	// best-effort accelerator, never a delivery requirement.
	NotifyAddr   string `json:"notify_addr"`   // the notify daemon's own listen address (default ":8080"); only the notify daemon reads it
	NotifyURL    string `json:"notify_url"`    // base URL producers/consumers reach the notify daemon at (e.g. "http://notify:8080"); empty = push disabled, poll-only
	NotifySecret string `json:"notify_secret"` // shared bearer secret authenticating publish/subscribe; empty disables the auth check (dev only)

	HealthTargets []HealthTarget `json:"health_targets"` // daemons the admin Live status page probes (each daemon's /healthz URL)
}

// HealthTarget names a daemon and the URL of its /healthz endpoint, probed by the
// admin Live status page.
type HealthTarget struct {
	Name string `json:"name"`
	URL  string `json:"url"`
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

// ServerHostname reports the server's public mail hostname (config Hostname). It
// is the MX and autodiscover/autoconfig target that the admin DNS-records
// prescription points a domain owner at; a method (not the bare field) so it can
// be exposed through the admin Paths interface.
func (c *Config) ServerHostname() string {
	return c.Hostname
}

// MaildirFor derives a user's mailbox directory
// (the internal spec §5.5): {root}/user/{domain}/{localpart}. Collision
// suffixing (~N) is handled by the directory at provisioning time, not here.
//
// The root is chosen from the maildir-placement pool: with no data_partitions
// configured the pool is just DataDir, so the path is unchanged; with a pool
// the root is the listed partition selected by a stable FNV-1a hash of the
// address. The choice is a placement decision made once at provisioning — reads
// use the full path stored in users.maildir, so a chosen partition is recorded
// in that path and never re-derived for an existing mailbox.
func (c *Config) MaildirFor(address string) string {
	address = strings.ToLower(address)
	local, domain, _ := strings.Cut(address, "@")
	return filepath.Join(c.maildirRoot(address), "user", domain, local)
}

// maildirRoot picks a new mailbox's storage root from the placement pool. An
// empty pool falls back to DataDir, making MaildirFor byte-identical to the
// single-root behaviour.
func (c *Config) maildirRoot(address string) string {
	if len(c.DataPartitions) == 0 {
		return c.DataDir
	}
	h := fnv.New32a()
	h.Write([]byte(address))
	return c.DataPartitions[h.Sum32()%uint32(len(c.DataPartitions))]
}

// HomedirFor derives a domain's public-store directory: {DataDir}/domain/{domain}.
// The domain dir stays under DataDir (the control root) and is not spread across
// the maildir pool — under private-mailbox-only operation it holds no store.
func (c *Config) HomedirFor(domain string) string {
	return filepath.Join(c.DataDir, "domain", strings.ToLower(domain))
}

// RelaySpoolPath is the single outbound relay spool shared by every daemon:
// {DataDir}/relay.sqlite3. Each user-facing protocol enqueues external mail
// here and the MTA's relay worker drains it, so all daemons MUST derive the path
// through this one helper — a divergent path would split the queue and strand
// mail in a spool nothing drains.
func (c *Config) RelaySpoolPath() string {
	return filepath.Join(c.DataDir, "relay.sqlite3")
}

// AntispamModelPath is where the Bayesian spam model lives under the data root:
// the self-training task writes it and the MTA loads it at startup. The filename
// must match antispam.ModelFileName (kept literal here to avoid a config→feature
// dependency).
func (c *Config) AntispamModelPath() string {
	return filepath.Join(c.DataDir, "antispam-model.json")
}

// AntispamRulesPath is where the SpamAssassin ruleset lives under the data root:
// the MTA seeds it on first run and loads it at startup, and the refresh tool
// updates it. The filename must match antispam.RulesFileName (kept literal here to
// avoid a config→feature dependency).
func (c *Config) AntispamRulesPath() string {
	return filepath.Join(c.DataDir, "antispam-rules.cf")
}

// QuarantinePath is where the antivirus scanner stores a held message's raw
// bytes: {DataDir}/quarantine/{id}.eml. The directory records only the metadata
// the admin panel lists; the MTA writes the eml here and the panel reads it back
// by id, so both derive the path through this one helper.
func (c *Config) QuarantinePath(id int64) string {
	return filepath.Join(c.DataDir, "quarantine", fmt.Sprintf("%d.eml", id))
}
