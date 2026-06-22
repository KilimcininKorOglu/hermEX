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

	// settingsFn, when set via WatchSettings, returns the current editable Config
	// and its monotonic version; the reloader applies it when the version advances.
	settingsFn  func() (*Config, int64, bool)
	settingsVer int64

	// accessFn, when set via WatchAccess, returns the current allow/block list and a
	// content hash; the reloader applies it when the hash changes (a hash, not a
	// counter, so admin CRUD cannot forget to bump it and a delete is detected too).
	accessFn  func() (*AccessList, uint64, bool)
	accessVer uint64
}

// WatchSettings makes the reloader hot-apply edited settings on its tick: fn
// returns the current Config and its version token, and the reloader calls
// SetConfig when the version has advanced. It is kept out of the antispam package's
// dependencies — the caller (which owns the settings store) supplies fn. The
// current version is recorded now so already-loaded settings are not re-applied on
// the first tick.
func (r *Reloader) WatchSettings(fn func() (*Config, int64, bool)) {
	r.settingsFn = fn
	if _, ver, ok := fn(); ok {
		r.settingsVer = ver
	}
}

// WatchAccess makes the reloader hot-apply edited sender allow/block rules on its
// tick: fn returns the current AccessList and a content hash, and the reloader
// calls SetAccess when the hash changes. Like WatchSettings it is injected so the
// antispam package keeps no database dependency, and it records the current hash
// now so the already-loaded rules are not re-applied on the first tick.
func (r *Reloader) WatchAccess(fn func() (*AccessList, uint64, bool)) {
	r.accessFn = fn
	if _, h, ok := fn(); ok {
		r.accessVer = h
	}
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
	if r.settingsFn != nil {
		if cfg, ver, ok := r.settingsFn(); ok && ver > r.settingsVer {
			r.scorer.SetConfig(cfg)
			r.settingsVer = ver
			reloaded = append(reloaded, "settings")
		}
	}
	if r.accessFn != nil {
		if list, h, ok := r.accessFn(); ok && h != r.accessVer {
			r.scorer.SetAccess(list)
			r.accessVer = h
			reloaded = append(reloaded, "access rules")
		}
	}
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
