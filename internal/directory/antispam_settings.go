package directory

import (
	"database/sql"
	"errors"
	"time"
)

// AntispamSettings is the operator-editable anti-spam tuning: the signal weights,
// the spam threshold, and the comma-separated DNS blocklist zones. It is stored as
// a single row. UpdatedAt is a millisecond version token the MTA polls to detect
// an edit (not a display time). The fields are kept as plain ints here so this
// data layer does not depend on the antispam package; the MTA maps them.
type AntispamSettings struct {
	SPFFail     int
	SPFSoftFail int
	DKIMFail    int
	DMARCFail   int
	DNSBLHit    int
	BayesSpam   int
	SARulesHit  int
	Threshold   int
	Zones       string
	// BayesProb is the Bayes spam-probability cutoff (0..1) and SAThreshold the summed
	// SpamAssassin-rule score, each at or above which that signal contributes its
	// weight. Kept as plain floats here so the data layer does not depend on the
	// antispam package; the MTA maps them into the scorer's Config.
	BayesProb   float64
	SAThreshold float64
	UpdatedAt   int64
}

// GetAntispamSettings returns the stored settings; found is false when none have
// been saved yet, so the caller seeds the built-in defaults on first run.
func (d *SQLDirectory) GetAntispamSettings() (AntispamSettings, bool, error) {
	var s AntispamSettings
	err := d.db.QueryRow(
		`SELECT spf_fail, spf_softfail, dkim_fail, dmarc_fail, dnsbl_hit, bayes_spam, sa_rules_hit, threshold, zones, bayes_prob, sa_threshold, updated_at
		   FROM antispam_settings WHERE id = 1`).Scan(
		&s.SPFFail, &s.SPFSoftFail, &s.DKIMFail, &s.DMARCFail, &s.DNSBLHit, &s.BayesSpam, &s.SARulesHit, &s.Threshold, &s.Zones, &s.BayesProb, &s.SAThreshold, &s.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AntispamSettings{}, false, nil
	}
	if err != nil {
		return AntispamSettings{}, false, err
	}
	return s, true, nil
}

// SetAntispamSettings upserts the single settings row and stamps a fresh
// millisecond updated_at, so the MTA's poller observes the change and hot-reloads.
func (d *SQLDirectory) SetAntispamSettings(s AntispamSettings) error {
	_, err := d.db.Exec(
		`INSERT INTO antispam_settings
		   (id, spf_fail, spf_softfail, dkim_fail, dmarc_fail, dnsbl_hit, bayes_spam, sa_rules_hit, threshold, zones, bayes_prob, sa_threshold, updated_at)
		 VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON DUPLICATE KEY UPDATE
		   spf_fail=VALUES(spf_fail), spf_softfail=VALUES(spf_softfail), dkim_fail=VALUES(dkim_fail),
		   dmarc_fail=VALUES(dmarc_fail), dnsbl_hit=VALUES(dnsbl_hit), bayes_spam=VALUES(bayes_spam),
		   sa_rules_hit=VALUES(sa_rules_hit), threshold=VALUES(threshold), zones=VALUES(zones),
		   bayes_prob=VALUES(bayes_prob), sa_threshold=VALUES(sa_threshold), updated_at=VALUES(updated_at)`,
		s.SPFFail, s.SPFSoftFail, s.DKIMFail, s.DMARCFail, s.DNSBLHit, s.BayesSpam, s.SARulesHit, s.Threshold, s.Zones, s.BayesProb, s.SAThreshold, time.Now().UnixMilli())
	return err
}
