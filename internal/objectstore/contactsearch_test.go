package objectstore

import (
	"strings"
	"testing"
)

// searchNames returns the display names a query matched, for terse assertions.
func searchNames(t *testing.T, s *Store, query string, limit int) []string {
	t.Helper()
	matches, err := s.SearchContacts(query, limit)
	if err != nil {
		t.Fatalf("SearchContacts(%q): %v", query, err)
	}
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		names = append(names, m.DisplayName)
	}
	return names
}

// TestSearchContactsMatchesNameAndAddress is what recipient autocomplete needs:
// a user types part of a name or part of an address, and either finds the
// contact.
func TestSearchContactsMatchesNameAndAddress(t *testing.T) {
	s := openSeededStore(t)
	storeContact(t, s, 1, "Ada Lovelace", "ada@partner.example")
	storeContact(t, s, 2, "Grace Hopper", "grace@navy.example")

	for _, c := range []struct{ query, want string }{
		{"Lovel", "Ada Lovelace"},
		{"lovel", "Ada Lovelace"}, // the client sends what the user typed, in any case
		{"partner", "Ada Lovelace"},
		{"navy.example", "Grace Hopper"},
		{"Grace", "Grace Hopper"},
	} {
		names := searchNames(t, s, c.query, 10)
		if len(names) != 1 || names[0] != c.want {
			t.Errorf("SearchContacts(%q) = %v, want [%s]", c.query, names, c.want)
		}
	}
	if names := searchNames(t, s, "nobodyhere", 10); len(names) != 0 {
		t.Errorf("a query matching nobody returned %v", names)
	}
}

// TestSearchContactsCarriesTheAddress keeps the result usable: a persona with a
// name and no address cannot be autocompleted into a recipient field.
func TestSearchContactsCarriesTheAddress(t *testing.T) {
	s := openSeededStore(t)
	storeContact(t, s, 3, "Katherine Johnson", "katherine@nasa.example")

	matches, err := s.SearchContacts("Katherine", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("got %d matches, want 1", len(matches))
	}
	// The address lives in the third slot, so all three are searched and read.
	if matches[0].Address != "katherine@nasa.example" {
		t.Errorf("address = %q", matches[0].Address)
	}
}

// TestSearchContactsIgnoresAShortQuery keeps the address book out of the answer
// while the user is still typing: one or two characters match most of it.
func TestSearchContactsIgnoresAShortQuery(t *testing.T) {
	s := openSeededStore(t)
	storeContact(t, s, 1, "Ada Lovelace", "ada@partner.example")

	for _, q := range []string{"", " ", "A", "Ad"} {
		if names := searchNames(t, s, q, 10); len(names) != 0 {
			t.Errorf("SearchContacts(%q) = %v, want nothing", q, names)
		}
	}
	if names := searchNames(t, s, "Ada", 10); len(names) != 1 {
		t.Errorf("a three-character query matched %v", names)
	}
}

// TestSearchContactsTreatsTheQueryAsText is the injection guard for the pattern:
// the query is client-supplied, and LIKE reads % and _ as wildcards, so a query
// of "%" would return the whole address book.
func TestSearchContactsTreatsTheQueryAsText(t *testing.T) {
	s := openSeededStore(t)
	storeContact(t, s, 1, "Ada Lovelace", "ada@partner.example")
	storeContact(t, s, 1, "Percent Sign", "100%@marks.example")

	if names := searchNames(t, s, "%%%", 10); len(names) != 0 {
		t.Errorf("a wildcard query returned %v, want nothing", names)
	}
	// And a query that really holds the character finds the contact that does too.
	if names := searchNames(t, s, "100%", 10); len(names) != 1 || names[0] != "Percent Sign" {
		t.Errorf("SearchContacts(\"100%%\") = %v", names)
	}
	if names := searchNames(t, s, "___", 10); len(names) != 0 {
		t.Errorf("an underscore wildcard returned %v, want nothing", names)
	}
}

// TestSearchContactsHonoursTheLimit keeps one keystroke from returning the whole
// address book.
func TestSearchContactsHonoursTheLimit(t *testing.T) {
	s := openSeededStore(t)
	for _, n := range []string{"Ann Example", "Anna Example", "Anne Example", "Annie Example"} {
		storeContact(t, s, 1, n, strings.ToLower(strings.Split(n, " ")[0])+"@example.test")
	}
	if names := searchNames(t, s, "Ann", 2); len(names) != 2 {
		t.Errorf("got %d matches under a limit of 2: %v", len(names), names)
	}
	if names := searchNames(t, s, "Ann", 0); len(names) != 0 {
		t.Errorf("a limit of zero returned %v", names)
	}
}

// TestSearchContactsFindsNothingWithoutContacts covers the mailbox that has
// never stored a contact e-mail: no named ids are allocated, so the search
// answers without touching the folder.
func TestSearchContactsFindsNothingWithoutContacts(t *testing.T) {
	s := openSeededStore(t)
	if names := searchNames(t, s, "anybody", 10); len(names) != 0 {
		t.Errorf("an empty address book returned %v", names)
	}
}
