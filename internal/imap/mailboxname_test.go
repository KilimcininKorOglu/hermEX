package imap

import (
	"fmt"
	"strings"
	"testing"

	"hermex/internal/objectstore"
)

// injected is a mailbox name carrying a real CRLF plus a complete forged
// untagged response. Rendered inside a quoted string it becomes an extra
// protocol line in the client's parse stream.
const injected = "x\r\n* BAD forged\r\n"

// TestCreateRefusesAControlCharacterInAMailboxName proves the folder tree never
// takes a name that would break the response framing. A literal argument carries
// raw octets, so CREATE is the door a CR or LF walks through.
func TestCreateRefusesAControlCharacterInAMailboxName(t *testing.T) {
	c, _ := startServer(t)
	c.mustOK("a1", "LOGIN alice secret")

	fmt.Fprintf(c.conn, "a2 CREATE {%d}\r\n", len(injected))
	if cont := c.line(); !strings.HasPrefix(cont, "+") {
		t.Fatalf("CREATE continuation = %q, want +", cont)
	}
	fmt.Fprintf(c.conn, "%s\r\n", injected)
	if _, status := c.collect("a2"); status != "NO" {
		t.Errorf("CREATE with an embedded CRLF = %s, want NO", status)
	}

	// The name is not in the tree, so nothing renders it later either.
	for _, l := range c.mustOK("a3", `LIST "" *`) {
		if strings.Contains(l, "forged") {
			t.Errorf("the refused name still appears in LIST: %q", l)
		}
	}
}

// TestRenameRefusesAControlCharacterInAMailboxName covers the second door into
// the folder tree.
func TestRenameRefusesAControlCharacterInAMailboxName(t *testing.T) {
	c, _ := startServer(t)
	c.mustOK("a1", "LOGIN alice secret")
	c.mustOK("a2", "CREATE plain")

	fmt.Fprintf(c.conn, "a3 RENAME plain {%d}\r\n", len(injected))
	if cont := c.line(); !strings.HasPrefix(cont, "+") {
		t.Fatalf("RENAME continuation = %q, want +", cont)
	}
	fmt.Fprintf(c.conn, "%s\r\n", injected)
	if _, status := c.collect("a3"); status != "NO" {
		t.Errorf("RENAME to a name with an embedded CRLF = %s, want NO", status)
	}
}

// TestAStoredControlCharacterNameGoesOutAsALiteral covers the name IMAP did not
// create: every other protocol writes to the same folder tree, so the renderer
// must stay safe on its own. A quoted string cannot carry a CRLF, so the name is
// sent as a length-prefixed literal and the client reads it as data rather than
// parsing the forged line inside it.
func TestAStoredControlCharacterNameGoesOutAsALiteral(t *testing.T) {
	c, path := startServer(t)
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateFolder(nil, injected); err != nil {
		t.Fatal(err)
	}
	st.Close()

	c.mustOK("a1", "LOGIN alice secret")
	lines := c.mustOK("a2", `LIST "" *`)

	var listing string
	for _, l := range lines {
		if strings.Contains(l, "forged") {
			listing = l
		}
	}
	if listing == "" {
		t.Fatalf("the stored folder never appeared in LIST: %v", lines)
	}
	// c.line inlines a literal, so the forged text is present either way; what
	// proves the fix is the {n} framing that told the client to read it as data.
	if !strings.Contains(listing, fmt.Sprintf("{%d}", len(injected))) {
		t.Errorf("LIST line = %q, want the name framed as a literal", listing)
	}
	// A quoted rendering would end the line at the name's own CR, leaving the rest
	// of the folder name behind as its own untagged response.
	for _, l := range lines {
		if strings.HasPrefix(l, "* BAD") {
			t.Errorf("an injected untagged response reached the client: %q", l)
		}
	}
}
