package antispam

import (
	"bytes"
	"embed"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// sarules_embed.go vendors a baseline SpamAssassin ruleset into the binary and,
// on first run, seeds it into data_dir/antispam-rules.cf — the live ruleset the
// MTA loads thereafter. An operator (or the refresh tool) edits that data_dir
// file to update the rules without rebuilding the binary; the embedded copy is
// only the first-run seed. The vendored .cf files (and their Apache-2.0 LICENSE
// and NOTICE) live in the sarules/ directory.

//go:embed sarules/*.cf
var embeddedRulesFS embed.FS

// RulesFileName is the live SpamAssassin ruleset file under data_dir. It is
// seeded from the embedded baseline on first run and updated in place thereafter.
const RulesFileName = "antispam-rules.cf"

// seededHeader is prepended to the seeded data_dir ruleset so an operator opening
// the file understands its provenance and that it may be edited. The parser
// ignores comment lines.
const seededHeader = `# hermEX anti-spam ruleset (live copy under data_dir).
# Seeded once from the vendored Apache SpamAssassin baseline (Apache-2.0).
# Edit or replace this file to tune the rules; the refresh tool overwrites it.
# Only header/body/rawbody/uri/meta rules are evaluated; network/plugin rules are
# ignored. Changes take effect on the next MTA restart.

`

var (
	embeddedRulesOnce sync.Once
	embeddedRules     *SARuleSet
)

// EmbeddedRules returns the built-in baseline ruleset, parsed once from the
// vendored Apache SpamAssassin .cf files and cached. The result is read-only.
func EmbeddedRules() *SARuleSet {
	embeddedRulesOnce.Do(func() {
		embeddedRules = ParseSARules(string(embeddedRulesText()))
	})
	return embeddedRules
}

// embeddedRulesText concatenates the vendored .cf files in the deterministic
// order embed.FS reports (sorted by name), so the seeded data_dir file is stable.
func embeddedRulesText() []byte {
	entries, _ := embeddedRulesFS.ReadDir("sarules")
	var b bytes.Buffer
	for _, e := range entries {
		if data, err := embeddedRulesFS.ReadFile("sarules/" + e.Name()); err == nil {
			b.Write(data)
			b.WriteByte('\n')
		}
	}
	return b.Bytes()
}

// LoadRules returns the live ruleset the MTA scores with. It seeds
// data_dir/antispam-rules.cf from the embedded baseline on first run, then loads
// from that file, so the rules live in the data folder where they can be seen,
// edited, and refreshed. A seed or read error falls back to the in-memory
// baseline and is returned so the caller can log it; the ruleset is always usable
// (never nil).
func LoadRules(dataDir string) (*SARuleSet, error) {
	path := filepath.Join(dataDir, RulesFileName)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		if err := seedRulesFile(path); err != nil {
			return EmbeddedRules(), err
		}
	}
	rs, err := LoadRulesFile(path)
	if err != nil || rs == nil {
		return EmbeddedRules(), err
	}
	return rs, nil
}

// LoadRulesFile parses a .cf ruleset file. A missing file returns (nil, nil) so
// the caller can fall back to the baseline; any other read error is returned.
func LoadRulesFile(path string) (*SARuleSet, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return ParseSARules(string(data)), nil
}

// seedRulesFile writes the embedded baseline (with a provenance header) to path
// atomically: a temp file in the same directory, then rename.
func seedRulesFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data := append([]byte(seededHeader), embeddedRulesText()...)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
