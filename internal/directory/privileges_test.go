package directory

import "testing"

// TestPrivilegesFromBits proves the privilege_bits decoding: POP3/IMAP, SMTP and
// CHGPASSWD are plain bits, while WEB/EAS/DAV follow the DETAIL1 opt-out
// convention — granted by default, revoked only when DETAIL1 is set and the
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
