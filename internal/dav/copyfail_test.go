package dav

import (
	"errors"
	"slices"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcmail"
)

// TestAFailedOverwritingCopyKeepsTheDestination copies an object onto an existing
// one (Overwrite: T) and makes writing the copy fail. The object already at the
// destination must still be there: a request that could not complete must not
// destroy what it was going to replace.
func TestAFailedOverwritingCopyKeepsTheDestination(t *testing.T) {
	st, err := objectstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, st)
	fid := int64(mapi.PrivateFIDContacts)
	seed := func(subject string) int64 {
		id, err := st.CreateMessage(fid, &oxcmail.Message{Props: mapi.PropertyValues{
			{Tag: mapi.PrMessageClass, Value: "IPM.Contact"}, {Tag: mapi.PrSubject, Value: subject},
		}})
		if err != nil {
			t.Fatalf("seed %s: %v", subject, err)
		}
		return id
	}
	src, dst := seed("source"), seed("destination")

	prev := createCopy
	createCopy = func(*objectstore.Store, int64, *oxcmail.Message) error { return errors.New("disk full") }
	defer func() { createCopy = prev }()

	ends := copyMoveEnds{srcFID: fid, dstFID: fid,
		src: objectstore.FolderObject{ID: src}, dst: objectstore.FolderObject{ID: dst}, dstExists: true}
	if err := (&Server{}).writeCopy(st, ends, &oxcmail.Message{}, true); err == nil {
		t.Fatal("the failed copy was reported as done")
	}
	live, err := st.ListFolderObjects(fid)
	if err != nil {
		t.Fatal(err)
	}
	for name, id := range map[string]int64{"destination": dst, "source": src} {
		if !slices.ContainsFunc(live, func(o objectstore.FolderObject) bool { return o.ID == id }) {
			t.Errorf("the %s object left the collection after a failed copy", name)
		}
	}
}
