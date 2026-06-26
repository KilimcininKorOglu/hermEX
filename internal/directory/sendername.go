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

// outgoingNameProptags maps each template placeholder to the directory proptag whose
// value fills it (standard MAPI person properties, kept numeric to match the
// directory's other property code).
var outgoingNameProptags = map[string]uint32{
	"name":       0x3001001F, // PR_DISPLAY_NAME
	"company":    0x3A16001F, // PR_COMPANY_NAME
	"title":      0x3A17001F, // PR_TITLE
	"department": 0x3A18001F, // PR_DEPARTMENT_NAME
	"office":     0x3A19001F, // PR_OFFICE_LOCATION
}

// OutgoingDisplayNames returns the From display names a sender's outgoing mail should
// carry to internal and external recipients, formatted from the sender's domain
// templates and profile. An empty string for a direction means "leave the From name
// untouched" (no template for that direction, or the template rendered empty). A
// sender whose domain has no templates yields two empty strings with no profile read.
func (d *SQLDirectory) OutgoingDisplayNames(from string) (internal, external string, err error) {
	intTpl, extTpl, err := d.GetDomainNameTemplates(addrDomain(from))
	if err != nil || (intTpl == "" && extTpl == "") {
		return "", "", err
	}
	props, err := d.GetUserProperties(from)
	if err != nil {
		return "", "", err
	}
	vals := make(map[string]string, len(outgoingNameProptags))
	for key, tag := range outgoingNameProptags {
		vals[key] = props[tag]
	}
	return FormatSenderName(intTpl, vals), FormatSenderName(extTpl, vals), nil
}
