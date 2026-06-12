// Package config loads hermEX daemon configuration. Accounts are NOT configured
// here — they live in the directory database (see internal/directory); config
// holds only infrastructure settings (the directory DSN, listen addresses, the
// mailbox data root, and the announced hostname).
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config is the JSON configuration shared by the mail daemons and the admin CLI.
type Config struct {
	DatabaseDSN string `json:"database_dsn"` // MariaDB DSN for the directory (go-sql-driver form)
	DataDir     string `json:"data_dir"`     // root under which mailbox/domain stores are created
	Hostname    string `json:"hostname"`     // announced in protocol greetings
	SMTPAddr    string `json:"smtp_addr"`    // MTA listen address (default ":25")
	POP3Addr    string `json:"pop3_addr"`    // POP3 listen address (default ":110")
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

// MaildirFor derives a user's mailbox directory the reference way
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
