package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAndDerivations(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	doc := `{"database_dsn":"root:pw@tcp(db:3306)/email","data_dir":"/data/mb",
	         "hostname":"mail.test","smtp_addr":":25","pop3_addr":":110"}`
	if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.DatabaseDSN == "" || c.Hostname != "mail.test" {
		t.Fatalf("loaded config = %+v", c)
	}
	// Maildir/homedir follow the Gromox {prefix}/{domain}/{localpart} rule.
	if got := c.MaildirFor("Alice@Example.com"); got != "/data/mb/user/example.com/alice" {
		t.Errorf("MaildirFor = %q", got)
	}
	if got := c.HomedirFor("Example.com"); got != "/data/mb/domain/example.com" {
		t.Errorf("HomedirFor = %q", got)
	}
}

func TestLoadRequiresDSNandDataDir(t *testing.T) {
	cases := []string{`{"data_dir":"/x"}`, `{"database_dsn":"d"}`}
	for _, doc := range cases {
		p := filepath.Join(t.TempDir(), "c.json")
		if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p); err == nil {
			t.Errorf("Load(%s) should fail", doc)
		}
	}
}
