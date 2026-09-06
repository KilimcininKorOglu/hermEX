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

	mustNoErr(t, "put bob certificate", s.PutRecipientCert("Bob@Hermex.Test", []byte("bob-der")))
	mustNoErr(t, "put carol certificate", s.PutRecipientCert("carol@hermex.test", []byte("carol-der")))

	der, ok, err := s.GetRecipientCert("bob@hermex.test") // case-insensitive
	mustNoErr(t, "get bob certificate", err)
	if !ok || !bytes.Equal(der, []byte("bob-der")) {
		t.Fatalf("get bob = %q ok=%v", der, ok)
	}
	wantEq(t, "certificate count", len(mustListRecipientCerts(t, s)), 2)

	mustNoErr(t, "delete bob certificate", s.DeleteRecipientCert("bob@hermex.test"))
	_, ok, _ = s.GetRecipientCert("bob@hermex.test")
	wantEq(t, "bob certificate present after delete", ok, false)
	wantEq(t, "certificate count after delete", len(mustListRecipientCerts(t, s)), 1)
}

// mustListRecipientCerts lists the stored recipient certificates.
func mustListRecipientCerts(t *testing.T, s *Store) map[string][]byte {
	t.Helper()
	all, err := s.ListRecipientCerts()
	mustNoErr(t, "list recipient certificates", err)
	return all
}
