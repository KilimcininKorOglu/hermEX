package rpchttp

import (
	"testing"

	"hermex/internal/ndr"
)

// TestAContextHandleServesOnlyItsOwner names alice's context handle from bob's
// signed-in session. The handle is a server counter the client echoes back
// ([MS-OXCRPC] 3.1.4.1), so another account can guess it; a handle is bound to
// the session that opened it, and naming it from any other account must fault
// with context mismatch rather than run ROPs in, notify for, or close alice's
// mailbox session.
func TestAContextHandleServesOnlyItsOwner(t *testing.T) {
	ems := NewEMSMDB(nil)
	bob := &Session{User: "bob@hermex.test", Mailbox: provisionedDir(t)}
	cxh := connectAt(t, ems, "alice@hermex.test", provisionedDir(t))

	disc := ndr.NewPush()
	pushCtxHandle(disc, cxh)
	for name, call := range map[string]struct {
		opnum uint16
		stub  []byte
	}{
		"EcDoRpcExt2":        {opEcDoRpcExt2, buildRpcExt2Stub(cxh, nil)},
		"EcDoAsyncConnectEx": {opEcDoAsyncConnectEx, buildAsyncConnectExStub(cxh)},
		"EcDoDisconnect":     {opEcDoDisconnect, disc.Bytes()},
	} {
		if _, fault := ems.Handle(bob, call.opnum, call.stub); fault != ndr.FaultContextMismatch {
			t.Errorf("%s with alice's handle from bob: fault = %#x, want context mismatch", name, fault)
		}
	}
	if _, ok := ems.lookup("alice@hermex.test", cxh.GUID); !ok {
		t.Fatal("bob's calls closed alice's session")
	}
	// The owner keeps the handle, whatever case the address is written in.
	if _, fault := ems.Handle(&Session{User: "Alice@HERMEX.test"}, opEcDoRpcExt2, buildRpcExt2Stub(cxh, nil)); fault != 0 {
		t.Errorf("the owner's own handle faulted %#x", fault)
	}
}
