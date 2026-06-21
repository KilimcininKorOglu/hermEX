package antispam

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// reload.go hot-swaps the data_dir ruleset and Bayesian model into a Scorer when
// their files change, so a ruleset refresh or a model retrain takes effect without
// restarting the MTA — mail flow never pauses. It polls the files' modification
// times; the swap into the Scorer is atomic (SetRules/SetModel).

// Reloader watches the data_dir anti-spam files and applies changes to a Scorer.
type Reloader struct {
	scorer    *Scorer
	rulesPath string
	modelPath string
	rulesMod  time.Time
	modelMod  time.Time
	log       func(format string, v ...any)
}

// NewReloader builds a Reloader for dataDir, recording the files' current
// modification times so it reloads only on a later change (the caller is expected
// to have done the initial load at startup). A nil log discards messages.
func NewReloader(s *Scorer, dataDir string, log func(string, ...any)) *Reloader {
	if log == nil {
		log = func(string, ...any) {}
	}
	r := &Reloader{
		scorer:    s,
		rulesPath: filepath.Join(dataDir, RulesFileName),
		modelPath: filepath.Join(dataDir, ModelFileName),
		log:       log,
	}
	r.rulesMod, _ = fileModTime(r.rulesPath)
	r.modelMod, _ = fileModTime(r.modelPath)
	return r
}

// Run polls every interval and applies any change, until ctx is cancelled.
func (r *Reloader) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if names := r.reloadOnce(); len(names) > 0 {
				r.log("anti-spam: reloaded %s from data_dir without a restart", strings.Join(names, " and "))
			}
		}
	}
}

// reloadOnce reloads each file whose modification time has advanced since the last
// check, swapping it into the Scorer, and returns the names of what reloaded. A
// read/parse error is logged and the previous value is kept (fail-safe).
func (r *Reloader) reloadOnce() []string {
	var reloaded []string
	if mod, ok := fileModTime(r.rulesPath); ok && mod.After(r.rulesMod) {
		switch rs, err := LoadRulesFile(r.rulesPath); {
		case err != nil:
			r.log("anti-spam: ruleset reload failed, keeping the current rules: %v", err)
		case rs != nil:
			r.scorer.SetRules(rs)
			r.rulesMod = mod
			reloaded = append(reloaded, "ruleset")
		}
	}
	if mod, ok := fileModTime(r.modelPath); ok && mod.After(r.modelMod) {
		switch m, err := LoadModelFile(r.modelPath); {
		case err != nil:
			r.log("anti-spam: model reload failed, keeping the current model: %v", err)
		case m != nil:
			r.scorer.SetModel(m)
			r.modelMod = mod
			reloaded = append(reloaded, "model")
		}
	}
	return reloaded
}

func fileModTime(path string) (time.Time, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}, false
	}
	return fi.ModTime(), true
}
