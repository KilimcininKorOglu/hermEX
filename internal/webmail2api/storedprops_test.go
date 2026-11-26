package webmail2api

import (
	"errors"
	"testing"

	"hermex/internal/logging"
	"hermex/internal/objectstore"
)

// withStoreSink installs a collecting logger on stores opened during one test
// and removes it afterwards; the test baseline has no store logger.
func withStoreSink(t *testing.T) *errSink {
	t.Helper()
	sink := &errSink{}
	objectstore.SetDefaultLogger(logging.New(sink))
	t.Cleanup(func() { objectstore.SetDefaultLogger(nil) })
	return sink
}

// TestStoredCategoriesRecordsAFailedRead proves a category read that fails is
// recorded instead of rendering as "no categories": the view still renders, but
// the operator can see the labels were lost rather than never set.
func TestStoredCategoriesRecordsAFailedRead(t *testing.T) {
	sink := withStoreSink(t)
	st, err := objectstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	if got := storedCategories(st, 1); got != nil {
		t.Errorf("categories from a failed read = %v, want none", got)
	}
	if _, ok := sink.find("categories.read"); !ok {
		t.Error("the failed category read was not recorded")
	}
}

// TestMarkHasPictureRefusesAMissingContact proves the picture flag write reports
// a contact the store does not hold, so the photo upload answers a failure
// instead of a success that left no flag behind.
func TestMarkHasPictureRefusesAMissingContact(t *testing.T) {
	st, err := objectstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := markHasPicture(st, 987654); !errors.Is(err, objectstore.ErrNotFound) {
		t.Errorf("markHasPicture on a missing contact: err = %v, want ErrNotFound", err)
	}
}
