// Package config loads hermEX daemon configuration and derives the runtime
// account set from it.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"hermex/internal/directory"
)

// Account is one configured mailbox account.
type Account struct {
	Address  string `json:"address"`
	Password string `json:"password"`
}

// Config is the JSON configuration shared by the mail daemons.
type Config struct {
	DataDir  string    `json:"data_dir"`  // directory holding per-mailbox store files
	Hostname string    `json:"hostname"`  // announced in protocol greetings
	SMTPAddr string    `json:"smtp_addr"` // listen address for the MTA (default ":25")
	POP3Addr string    `json:"pop3_addr"` // listen address for POP3 (default ":110")
	Accounts []Account `json:"accounts"`
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
	if c.DataDir == "" {
		return nil, fmt.Errorf("config: data_dir is required")
	}
	return &c, nil
}

// MailboxPath returns the store-file path for an address under DataDir.
func (c *Config) MailboxPath(address string) string {
	return filepath.Join(c.DataDir, strings.ToLower(address)+".sqlite3")
}

// StaticAccounts builds the directory account set from the configured accounts.
func (c *Config) StaticAccounts() directory.StaticAccounts {
	accts := make(directory.StaticAccounts, len(c.Accounts))
	for _, a := range c.Accounts {
		addr := strings.ToLower(a.Address)
		accts[addr] = directory.Account{Password: a.Password, MailboxPath: c.MailboxPath(addr)}
	}
	return accts
}
