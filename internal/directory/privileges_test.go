package directory

import (
	"testing"

	"hermex/internal/logging"
)

// TestPrivilegesFromBits proves the privilege_bits decoding: POP3/IMAP, SMTP and
// CHGPASSWD are plain bits, while WEB/EAS/DAV follow the DETAIL1 opt-out
// convention, granted by default, revoked only when DETAIL1 is set and the
// service bit is clear. This is the wire contract the reference shares, so a
// legacy row (DETAIL1 unset) must read every detail service granted.
func TestPrivilegesFromBits(t *testing.T) {
	cases := []struct {
		name string
		bits uint32
		want ServicePrivileges
	}{
		{
			name: "legacy row (no bits): plain services off, detail services on",
			bits: 0,
			want: ServicePrivileges{Web: true, EAS: true, DAV: true},
		},
		{
			name: "plain bits set",
			bits: privIMAPPOP3 | privSMTP | privChgPasswd,
			want: ServicePrivileges{POP3IMAP: true, SMTP: true, ChgPasswd: true, Web: true, EAS: true, DAV: true},
		},
		{
			name: "detail1 set, all detail bits set: all detail services granted",
			bits: privDetail1 | privWeb | privEAS | privDAV,
			want: ServicePrivileges{Web: true, EAS: true, DAV: true},
		},
		{
			name: "detail1 set, web bit cleared: only web revoked",
			bits: privDetail1 | privEAS | privDAV,
			want: ServicePrivileges{Web: false, EAS: true, DAV: true},
		},
		{
			name: "detail1 set, all detail bits clear: all detail services revoked",
			bits: privDetail1,
			want: ServicePrivileges{Web: false, EAS: false, DAV: false},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := privilegesFromBits(c.bits); got != c.want {
				t.Errorf("privilegesFromBits(%012b) = %+v, want %+v", c.bits, got, c.want)
			}
		})
	}
}

// TestPrivilegeBitsRoundTrip proves the admin write (privilegeBitsFor) and the
// protocol read (privilegesFromBits) are inverse over the managed services, so a
// service disabled in the admin form is exactly what the protocol later reads and
// enforces, not silently re-granted by the DETAIL1 default.
func TestPrivilegeBitsRoundTrip(t *testing.T) {
	cases := []UserUpdate{
		{}, // every service off
		{POP3IMAP: true, SMTP: true, ChgPasswd: true, Web: true, EAS: true, DAV: true}, // every service on
		{POP3IMAP: true, Web: true},        // a plain+detail mix
		{SMTP: true, EAS: true},            // a different mix
		{Web: true, EAS: false, DAV: true}, // one detail service revoked
	}
	for _, u := range cases {
		got := privilegesFromBits(uint32(privilegeBitsFor(u)))
		want := ServicePrivileges{
			POP3IMAP:  u.POP3IMAP,
			SMTP:      u.SMTP,
			ChgPasswd: u.ChgPasswd,
			Web:       u.Web,
			EAS:       u.EAS,
			DAV:       u.DAV,
		}
		if got != want {
			t.Errorf("round-trip of %+v = %+v, want %+v", u, got, want)
		}
	}
}

// TestPrivilegesOfAnUnknownAccount proves an address no account owns answers
// "unknown" with no service granted, so a protocol cannot read the detail-service
// default as permission for a name that does not exist.
func TestPrivilegesOfAnUnknownAccount(t *testing.T) {
	d := freshSQL(t)
	privs, ok := d.Privileges("ghost@hermex.test")
	wantEq(t, "Privileges known for an unknown account", ok, false)
	wantEq(t, "privileges of an unknown account", privs, ServicePrivileges{})
}

// TestPrivilegesErrorIsLoggedAndReturnsUnknown proves a database error inside
// Privileges still answers (ServicePrivileges{}, false) while recording the failure.
func TestPrivilegesErrorIsLoggedAndReturnsUnknown(t *testing.T) {
	db := openTestDB(t)
	d := NewSQL(db)
	sink := &captureSink{}
	d.SetLogger(logging.New(sink))
	mustClose(t, db)

	privs, ok := d.Privileges("alice@hermex.test")
	wantEq(t, "Privileges ok on closed DB", ok, false)
	wantEq(t, "Privileges zero struct on closed DB", privs, ServicePrivileges{})

	e := wantWarnEvent(t, sink, "directory.privileges.fail")
	wantEq(t, "event user", e.Fields["user"], "alice@hermex.test")
}
