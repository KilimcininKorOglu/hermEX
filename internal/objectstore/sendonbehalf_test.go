package objectstore

import (
	"slices"
	"testing"
)

// TestSendOnBehalfRoundTrip proves the list survives the store and that it is a
// list of its own: writing it must not disturb the send-as list, because the two
// grants put different things on a message and a surface that read one for the
// other would name the wrong sender.
func TestSendOnBehalfRoundTrip(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if list, err := st.GetSendOnBehalf(); err != nil || list != nil {
		t.Fatalf("an unset list read as (%v, %v), want (nil, nil)", list, err)
	}

	mustSet(t, st.SetSendAs, []string{"as@hermex.test"})
	mustSet(t, st.SetSendOnBehalf, []string{"behalf@hermex.test", "second@hermex.test"})

	wantList(t, st.GetSendOnBehalf, []string{"behalf@hermex.test", "second@hermex.test"}, "send-on-behalf")
	// Writing one list must leave the other alone: the two grants put different
	// things on a message, so a surface reading one for the other names the wrong
	// sender.
	wantList(t, st.GetSendAs, []string{"as@hermex.test"}, "send-as")

	mustSet(t, st.SetSendOnBehalf, nil)
	wantList(t, st.GetSendOnBehalf, nil, "the cleared send-on-behalf")
}

// mustSet writes a grant list and fails the test when the store refuses it.
func mustSet(t *testing.T, set func([]string) error, list []string) {
	t.Helper()
	if err := set(list); err != nil {
		t.Fatal(err)
	}
}

// wantList reads a grant list and compares it with what the test expects.
func wantList(t *testing.T, read func() ([]string, error), want []string, what string) {
	t.Helper()
	got, err := read()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !slices.Equal(got, want) {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}
