package directory

import "testing"

// TestSavingUnchangedValuesStillFindsTheRow saves each value a second time. MariaDB
// counts only rows a statement changed, so an update that writes what the row
// already holds affects nothing; found must still report the row, or an operator
// who saves an unchanged form is told the account or organization does not exist.
func TestSavingUnchangedValuesStillFindsTheRow(t *testing.T) {
	d := freshSQL(t)
	root := t.TempDir()
	domID := mustCreateDomain(t, d, root, "same.test")
	seedUser(t, d, root, "u@same.test", "password1")
	orgID, err := d.CreateOrg("Same", "unchanged")
	mustNoErr(t, "create the organization", err)

	for i := range 2 {
		found, err := d.UpdateOrg(orgID, "Same", "unchanged")
		mustNoErr(t, "update the organization", err)
		wantEq(t, "UpdateOrg found the organization on save "+string(rune('1'+i)), found, true)

		found, err = d.AssignDomainToOrg(domID, orgID)
		mustNoErr(t, "assign the domain", err)
		wantEq(t, "AssignDomainToOrg found the domain on save "+string(rune('1'+i)), found, true)

		found, err = d.SetUserLocale("u@same.test", "Europe/Istanbul", "tr")
		mustNoErr(t, "set the locale", err)
		wantEq(t, "SetUserLocale found the user on save "+string(rune('1'+i)), found, true)

		found, err = d.RequirePasswordChange("u@same.test", true)
		mustNoErr(t, "flag the account", err)
		wantEq(t, "RequirePasswordChange found the user on save "+string(rune('1'+i)), found, true)
	}

	// A missing row is still reported as missing.
	found, err := d.UpdateOrg(orgID+999, "Same", "unchanged")
	mustNoErr(t, "update a missing organization", err)
	wantEq(t, "UpdateOrg found a missing organization", found, false)
	found, err = d.AssignDomainToOrg(domID+999, orgID)
	mustNoErr(t, "assign a missing domain", err)
	wantEq(t, "AssignDomainToOrg found a missing domain", found, false)
	found, err = d.SetUserLocale("ghost@same.test", "UTC", "en")
	mustNoErr(t, "set a missing user's locale", err)
	wantEq(t, "SetUserLocale found a missing user", found, false)
	found, err = d.RequirePasswordChange("ghost@same.test", true)
	mustNoErr(t, "flag a missing account", err)
	wantEq(t, "RequirePasswordChange found a missing user", found, false)
}
