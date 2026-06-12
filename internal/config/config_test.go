package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAndAccounts(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	doc := `{"data_dir":"/data/mb","hostname":"mail.test","smtp_addr":":25",
	         "pop3_addr":":110","accounts":[{"address":"Alice@Test","password":"secret"}]}`
	if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Hostname != "mail.test" || c.DataDir != "/data/mb" {
		t.Fatalf("loaded config = %+v", c)
	}
	accts := c.StaticAccounts()
	// The address is lowercased and mapped to a store path under DataDir.
	path, ok := accts.Resolve("alice@test")
	if !ok || path != "/data/mb/alice@test.sqlite3" {
		t.Errorf("Resolve = %q, %v", path, ok)
	}
	if _, ok := accts.Authenticate("alice@test", "secret"); !ok {
		t.Error("Authenticate(correct) should succeed")
	}
	if _, ok := accts.Authenticate("alice@test", "wrong"); ok {
		t.Error("Authenticate(wrong) should fail")
	}
}

func TestLoadMissingDataDir(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(p, []byte(`{"hostname":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Error("Load should fail when data_dir is missing")
	}
}
