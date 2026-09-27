package directory

import (
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"
)

// UILanguages are the interface languages webmail and the admin panel ship.
var UILanguages = []string{"en", "tr"}

// uiThemes are the stored theme values; "" means the user never chose one.
var uiThemes = []string{"", "light", "dark", "system"}

// ErrInvalidPref reports a preference value outside the accepted set.
var ErrInvalidPref = errors.New("invalid preference value")

// UserPrefs is a user's interface preferences, one record webmail and the admin
// panel both read and write, so a choice made in one follows the user to the other.
type UserPrefs struct {
	// Theme is "light", "dark" or "system", or "" when the user never chose one.
	Theme string
	// Lang is an interface language from UILanguages, or "" to follow the browser.
	Lang string
	// ShowWelcome reports whether the webmail inbox still shows its welcome banner.
	ShowWelcome bool
	// Timezone is an IANA zone name, or "" when the user never chose one.
	Timezone string
}

// UserPrefsUpdate names the preferences a write changes; a nil field keeps its
// stored value.
type UserPrefsUpdate struct {
	Theme       *string
	Lang        *string
	ShowWelcome *bool
	Timezone    *string
}

// UserPrefsStore is the optional directory capability that reads and writes
// UserPrefs. SQLDirectory implements it.
type UserPrefsStore interface {
	GetUserPrefs(username string) (UserPrefs, bool, error)
	SetUserPrefs(username string, u UserPrefsUpdate) (bool, error)
}

// GetUserPrefs returns a user's interface preferences and whether the user exists.
func (d *SQLDirectory) GetUserPrefs(username string) (UserPrefs, bool, error) {
	var p UserPrefs
	err := d.db.QueryRow(`SELECT ui_theme, lang, show_welcome_banner, timezone FROM users WHERE username = ?`,
		strings.ToLower(strings.TrimSpace(username))).Scan(&p.Theme, &p.Lang, &p.ShowWelcome, &p.Timezone)
	if errors.Is(err, sql.ErrNoRows) {
		return UserPrefs{}, false, nil
	}
	if err != nil {
		return UserPrefs{}, false, err
	}
	return p, true, nil
}

// SetUserPrefs writes the preferences u names and leaves the others as stored. It
// refuses a theme, language or time zone outside the accepted set with
// ErrInvalidPref, and reports whether the user exists.
func (d *SQLDirectory) SetUserPrefs(username string, u UserPrefsUpdate) (bool, error) {
	if !ValidPrefs(u) {
		return false, ErrInvalidPref
	}
	// A nil pointer binds SQL NULL, so COALESCE keeps the stored column.
	name := strings.ToLower(strings.TrimSpace(username))
	res, err := d.db.Exec(`UPDATE users SET ui_theme = COALESCE(?, ui_theme), lang = COALESCE(?, lang),
		show_welcome_banner = COALESCE(?, show_welcome_banner), timezone = COALESCE(?, timezone) WHERE username = ?`,
		u.Theme, u.Lang, u.ShowWelcome, u.Timezone, name)
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err != nil || n > 0 {
		return n > 0, err
	}
	// MariaDB counts changed rows, so a write that repeats the stored values
	// affects none; tell that apart from a user that does not exist.
	_, found, err := d.GetUserPrefs(name)
	return found, err
}

// ValidPrefs reports whether every value u names is in its accepted set.
func ValidPrefs(u UserPrefsUpdate) bool {
	if u.Theme != nil && !slices.Contains(uiThemes, *u.Theme) {
		return false
	}
	if u.Lang != nil && *u.Lang != "" && !slices.Contains(UILanguages, *u.Lang) {
		return false
	}
	return u.Timezone == nil || ValidTimezone(*u.Timezone)
}

// ValidTimezone reports whether name is "" (never chosen) or an IANA zone name.
// "Local" is refused, because it names the server's zone, not the user's.
func ValidTimezone(name string) bool {
	if name == "" {
		return true
	}
	if name == "Local" {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}
