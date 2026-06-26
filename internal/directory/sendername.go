package directory

import (
	"database/sql"
	"errors"
	"regexp"
	"strings"
)

// senderNamePlaceholders are the sender profile fields a per-tenant outgoing
// display-name template may reference, in substitution order.
var senderNamePlaceholders = []string{"name", "company", "title", "department", "office"}

var (
	reSenderParenGroup = regexp.MustCompile(`\(([^()]*)\)`)
	reSenderWhitespace = regexp.MustCompile(`\s{2,}`)
)

// FormatSenderName builds an outgoing From display name by substituting the {name},
// {company}, {title}, {department}, {office} placeholders in tpl with vals, then
// tidying the result: a parenthetical group left empty by an absent value is removed
// and dangling separators an absent value left at an edge are trimmed, and runs of
// whitespace are collapsed. An empty or whitespace-only template returns "", meaning
// the From display name is left untouched.
func FormatSenderName(tpl string, vals map[string]string) string {
	if strings.TrimSpace(tpl) == "" {
		return ""
	}
	out := tpl
	for _, key := range senderNamePlaceholders {
		out = strings.ReplaceAll(out, "{"+key+"}", vals[key])
	}
	// Drop a parenthetical group emptied by absent values; trim separators an absent
	// value left dangling at a group's edge (e.g. "(Acme - )" becomes "(Acme)").
	out = reSenderParenGroup.ReplaceAllStringFunc(out, func(g string) string {
		inner := strings.Trim(g[1:len(g)-1], " \t-|,/")
		if inner == "" {
			return ""
		}
		return "(" + inner + ")"
	})
	out = reSenderWhitespace.ReplaceAllString(out, " ")
	return strings.Trim(out, " \t-|,/")
}

// GetDomainNameTemplates returns a domain's outgoing display-name templates for
// internal and external recipients; an empty string means that direction is not
// customized. An unknown domain yields two empty strings.
func (d *SQLDirectory) GetDomainNameTemplates(domain string) (internal, external string, err error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	err = d.db.QueryRow(
		`SELECT outgoing_name_tpl_internal, outgoing_name_tpl_external FROM domains WHERE domainname = ?`,
		domain).Scan(&internal, &external)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return internal, external, err
}

// SetDomainNameTemplates stores a domain's outgoing display-name templates (trimmed;
// empty disables that direction).
func (d *SQLDirectory) SetDomainNameTemplates(domain, internal, external string) error {
	domain = strings.ToLower(strings.TrimSpace(domain))
	_, err := d.db.Exec(
		`UPDATE domains SET outgoing_name_tpl_internal = ?, outgoing_name_tpl_external = ? WHERE domainname = ?`,
		strings.TrimSpace(internal), strings.TrimSpace(external), domain)
	return err
}
