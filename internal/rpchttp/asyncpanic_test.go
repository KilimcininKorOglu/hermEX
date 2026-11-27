package rpchttp

import (
	"testing"
	"time"
)

// TestAParkedWaitThatPanicsAnswersAsATimeout parks an EcDoAsyncWaitEx and makes
// its mailbox poll panic. The poller runs on its own goroutine with nothing above
// it, so an unrecovered panic would end the gateway for every Outlook client. The
// wait must instead be answered as a timeout (flags_out 0), which the client
// renews, and the session's wait slot must be free for that renewal.
func TestAParkedWaitThatPanicsAnswersAsATimeout(t *testing.T) {
	ems := NewEMSMDB(nil)
	async := NewAsyncEMSMDB(ems)
	async.cadence = 10 * time.Millisecond
	async.waitInterval = 2 * time.Second
	async.poll = func(*emsmdbSession) bool { panic("change detector bug") }
	const user = "alice@hermex.test"
	cxh, _ := connectLogonRegister(t, ems, user, provisionedDir(t))
	acxh := asyncConnect(t, ems, user, cxh)

	vc := testVconn()
	if _, fault := async.asyncWaitEx(&Session{User: user, vc: vc, maxFrag: 4096}, buildAsyncWaitExStub(acxh)); fault != faultParked {
		t.Fatalf("quiet-mailbox wait did not park")
	}
	if flags, result := parseAsyncWaitExOut(t, readParkedReply(t, vc)); flags != 0 || result != ecSuccess {
		t.Errorf("reply after the panic = (flags %#x, result %#x), want the timeout answer (0, ecSuccess)", flags, result)
	}

	// The renewal parks again rather than being dropped as a second wait.
	deadline := time.Now().Add(2 * time.Second)
	for {
		vc2 := testVconn()
		_, fault := async.asyncWaitEx(&Session{User: user, vc: vc2, maxFrag: 4096}, buildAsyncWaitExStub(acxh))
		if fault == faultParked {
			if flags, result := parseAsyncWaitExOut(t, readParkedReply(t, vc2)); flags != 0 || result != ecSuccess {
				t.Errorf("renewed reply after panic = (flags %#x, result %#x), want (0, ecSuccess)", flags, result)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the renewed wait did not park: the wait slot stayed taken after the panic")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
