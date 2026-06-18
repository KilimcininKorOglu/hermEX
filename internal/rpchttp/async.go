package rpchttp

import (
	"hermex/internal/mapi"
	"hermex/internal/ndr"
)

// AsyncEMSMDB interface identity ([MS-OXCRPC] 1.10): the notification long-poll
// interface, bound separately from the store/ROP EMSMDB interface.
var (
	// AsyncEMSMDBUUID is the AsyncEMSMDB interface UUID
	// 5261574a-4572-206e-b268-6b199213b4e4.
	AsyncEMSMDBUUID = mapi.GUID{
		Data1: 0x5261574A, Data2: 0x4572, Data3: 0x206E,
		Data4: [8]byte{0xB2, 0x68, 0x6B, 0x19, 0x92, 0x13, 0xB4, 0xE4},
	}
	// AsyncEMSMDBVersion is the interface version 0.1 (0x00010000).
	AsyncEMSMDBVersion uint32 = 0x00010000
)

// opEcDoAsyncWaitEx is the single opnum the AsyncEMSMDB interface dispatches.
const opEcDoAsyncWaitEx uint16 = 0

// asyncHandleType is the context-handle discriminator for an async (notification)
// handle, distinct from the EMSMDB store handle ([MS-OXCRPC] 2.2.1). EcDoAsyncConnectEx
// re-tags the session GUID with it; the GUID alone keys the session.
const asyncHandleType uint32 = 3

// flagNotificationPending is the EcDoAsyncWaitEx flags_out bit reporting that a
// notification is queued for the session ([MS-OXCRPC] 2.2.2.2.15).
const flagNotificationPending uint32 = 0x00000001

// ecRejected is returned when an async wait names an unknown session or one that
// belongs to another authenticated user.
const ecRejected uint32 = 0x000007EE

// AsyncEMSMDB is the AsyncEMSMDB RPC interface stub: the EcDoAsyncWaitEx long-poll
// that reports whether a notification is queued for the session. It shares the
// EMSMDB stub's session table, resolving the async context handle to a session by
// the GUID the handle carries.
type AsyncEMSMDB struct {
	ems *EMSMDB
}

// NewAsyncEMSMDB returns an AsyncEMSMDB stub over the given EMSMDB stub's sessions.
func NewAsyncEMSMDB(ems *EMSMDB) *AsyncEMSMDB {
	return &AsyncEMSMDB{ems: ems}
}

// Handle is the IfaceHandler the dispatcher calls for an AsyncEMSMDB request.
func (a *AsyncEMSMDB) Handle(sess *Session, opnum uint16, stub []byte) ([]byte, uint32) {
	switch opnum {
	case opEcDoAsyncWaitEx:
		return a.asyncWaitEx(sess, stub)
	default:
		return nil, ndr.FaultOpRngError
	}
}

// asyncWaitExIn is the decoded EcDoAsyncWaitEx request ([MS-OXCRPC] 2.2.2.2.15):
// the async context handle plus a reserved flags word.
type asyncWaitExIn struct {
	acxh    ContextHandle
	flagsIn uint32
}

// pullAsyncWaitEx decodes the EcDoAsyncWaitEx request stub.
func pullAsyncWaitEx(stub []byte) (asyncWaitExIn, error) {
	p := ndr.NewPull(stub)
	var in asyncWaitExIn
	var err error
	if in.acxh, err = pullCtxHandle(p); err != nil {
		return in, err
	}
	in.flagsIn, err = p.Uint32()
	return in, err
}

// pushAsyncWaitExOut marshals the EcDoAsyncWaitEx response: flags_out + result.
func pushAsyncWaitExOut(flagsOut, result uint32) []byte {
	out := ndr.NewPush()
	out.Uint32(flagsOut)
	out.Uint32(result)
	return out.Bytes()
}

// asyncWaitEx handles EcDoAsyncWaitEx (opnum 0): it reports whether a notification
// is queued for the session named by the async context handle. A queued notification
// yields FLAG_NOTIFICATION_PENDING; an empty queue yields 0. An async handle naming
// no live session, or one owned by another user, is rejected.
//
// v1 answers from the immediate poll. The long-poll that parks the call until a
// notification arrives or the wait interval elapses is a later increment; until then
// a client re-polls, which is wire-valid (an empty wait returns flags_out 0, the same
// value the parked wait returns on timeout).
func (a *AsyncEMSMDB) asyncWaitEx(sess *Session, stub []byte) ([]byte, uint32) {
	in, err := pullAsyncWaitEx(stub)
	if err != nil {
		return nil, ndr.FaultNdr
	}
	s, ok := a.ems.lookup(in.acxh.GUID)
	if !ok || s.user != sess.User {
		return pushAsyncWaitExOut(0, ecRejected), 0
	}
	flagsOut := uint32(0)
	if s.rop != nil && s.rop.PollForChange() {
		flagsOut = flagNotificationPending
	}
	return pushAsyncWaitExOut(flagsOut, ecSuccess), 0
}
