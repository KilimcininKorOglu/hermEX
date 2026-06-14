package objectstore

import (
	"bytes"
	"testing"
)

// TestSmimeIdentityRoundTrip checks that an S/MIME identity (binary PKCS#12 and
// certificate) survives a set/get round trip byte-for-byte, that a fresh store
// reports none, and that Clear removes it.
func TestSmimeIdentityRoundTrip(t *testing.T) {
	s := openSeededStore(t)

	if _, ok, err := s.GetSmimeIdentity(); err != nil || ok {
		t.Fatalf("fresh store: ok=%v err=%v, want no identity", ok, err)
	}
	id := SmimeIdentity{P12: []byte("\x00fake-p12-bytes\xff"), Cert: []byte("\x30\x82fake-cert\x00")}
	if err := s.SetSmimeIdentity(id); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetSmimeIdentity()
	if err != nil || !ok {
		t.Fatalf("GetSmimeIdentity ok=%v err=%v", ok, err)
	}
	if !bytes.Equal(got.P12, id.P12) || !bytes.Equal(got.Cert, id.Cert) {
		t.Errorf("identity mismatch: got %+v want %+v", got, id)
	}
	if err := s.ClearSmimeIdentity(); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GetSmimeIdentity(); ok {
		t.Error("identity still present after clear")
	}
}

// TestRecipientCertStore checks the address→certificate store: put, case-
// insensitive get, list, and delete.
func TestRecipientCertStore(t *testing.T) {
	s := openSeededStore(t)

	if err := s.PutRecipientCert("Bob@Hermex.Test", []byte("bob-der")); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRecipientCert("carol@hermex.test", []byte("carol-der")); err != nil {
		t.Fatal(err)
	}
	der, ok, err := s.GetRecipientCert("bob@hermex.test") // case-insensitive
	if err != nil || !ok || !bytes.Equal(der, []byte("bob-der")) {
		t.Fatalf("get bob = %q ok=%v err=%v", der, ok, err)
	}
	if all, err := s.ListRecipientCerts(); err != nil || len(all) != 2 {
		t.Fatalf("list = %v (err %v), want 2 entries", all, err)
	}
	if err := s.DeleteRecipientCert("bob@hermex.test"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GetRecipientCert("bob@hermex.test"); ok {
		t.Error("bob certificate still present after delete")
	}
	if all, _ := s.ListRecipientCerts(); len(all) != 1 {
		t.Errorf("after delete, list has %d entries, want 1", len(all))
	}
}
