// Command antispam-rules refreshes the SpamAssassin ruleset hermEX scores with.
// Point -from at a directory of .cf rule files — an sa-update output or a checkout
// of the upstream rules — and it concatenates, validates, and writes the single
// ruleset file the MTA loads from data_dir (data_dir/antispam-rules.cf).
//
// Fetching and verifying the upstream rules is delegated to the operator's own
// sa-update (which does GPG/SHA verification) or git, so this tool never fetches
// at runtime; it only vendors a trusted local directory into the loadable form.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"hermex/internal/antispam"
)

func main() {
	from := flag.String("from", "", "directory of SpamAssassin .cf rule files (e.g. an sa-update output)")
	out := flag.String("out", "", "output ruleset path, e.g. <data_dir>/antispam-rules.cf (default stdout)")
	flag.Parse()

	if *from == "" {
		log.Fatal("antispam-rules: -from <dir> is required")
	}

	data, err := antispam.ConcatRulesDir(*from)
	if err != nil {
		log.Fatalf("antispam-rules: %v", err)
	}

	// Validate and report coverage before writing, so a broken or wrong-directory
	// refresh is caught rather than silently shipping an empty ruleset.
	rs := antispam.ParseSARules(string(data))
	rules, metas := rs.RuleCount()
	log.Printf("antispam-rules: %d rules, %d metas evaluable; dropped %d rules and %d metas (network/plugin/incompatible)",
		rules, metas, rs.SkippedRules, rs.DroppedMetas)
	if rules == 0 {
		log.Fatalf("antispam-rules: %s produced no evaluable rules — wrong directory?", *from)
	}

	header := fmt.Sprintf("# hermEX anti-spam ruleset, refreshed from %s\n"+
		"# Apache SpamAssassin rules (Apache-2.0). Only header/body/rawbody/uri/meta\n"+
		"# rules are evaluated; network/plugin rules are ignored.\n\n", *from)
	payload := append([]byte(header), data...)

	if *out == "" {
		os.Stdout.Write(payload)
		return
	}
	if err := writeAtomic(*out, payload); err != nil {
		log.Fatalf("antispam-rules: write %s: %v", *out, err)
	}
	log.Printf("antispam-rules: wrote %s — the MTA picks it up automatically within a minute (no restart)", *out)
}

// writeAtomic writes data to path via a temp file in the same directory, then
// renames it into place so a reader never sees a partial ruleset.
func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
