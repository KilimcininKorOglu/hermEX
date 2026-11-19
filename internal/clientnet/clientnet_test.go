package clientnet

import "testing"

// TestKeyNamesTheClient pins what counts as one client. Every expected value comes
// from the rule, not from the code: the port never matters, an IPv4 host is itself
// whichever way it is written, and every address of one IPv6 /64 (RFC 6177's
// smallest site assignment) is one client while the next /64 is another.
func TestKeyNamesTheClient(t *testing.T) {
	cases := []struct{ in, want string }{
		{"198.51.100.7:5000", "198.51.100.7"},
		{"198.51.100.7", "198.51.100.7"},
		{"[::ffff:198.51.100.7]:1", "198.51.100.7"},
		{"[2001:db8:1:2::1]:5000", "2001:db8:1:2::/64"},
		{"[2001:db8:1:2:ffff:ffff:ffff:ffff]:9", "2001:db8:1:2::/64"},
		{"2001:db8:1:3::1", "2001:db8:1:3::/64"},
		{"not-an-ip:25", "not-an-ip"},
		{"", ""},
	}
	for _, c := range cases {
		if got := Key(c.in); got != c.want {
			t.Errorf("Key(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
