package rpchttp

import (
	"time"

	"hermex/internal/mapi"
	"hermex/internal/ndr"
	"hermex/internal/notify"
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

// The long-poll holds the call until a notification arrives or the wait interval
// elapses. The interval is held just under the [MS-OXCRPC] client expectation so the
// reply beats the client's own timeout; the cadence bounds how long an Execute can
// wait behind a poll for the shared store lock.
const (
	asyncWaitInterval = 297 * time.Second
	asyncPollCadence  = 5 * time.Second
)

// AsyncEMSMDB is the AsyncEMSMDB RPC interface stub: the EcDoAsyncWaitEx long-poll
// that reports whether a notification is queued for the session. It shares the
// EMSMDB stub's session table, resolving the async context handle to a session by
// the GUID the handle carries.
type AsyncEMSMDB struct {
	ems          *EMSMDB
	waitInterval time.Duration    // how long a parked wait holds before a timeout reply
	cadence      time.Duration    // how often the parked wait polls the shared store
	waker        notify.Registrar // push wake source; nil keeps the parked wait on its cadence only
}

// NewAsyncEMSMDB returns an AsyncEMSMDB stub over the given EMSMDB stub's sessions.
func NewAsyncEMSMDB(ems *EMSMDB) *AsyncEMSMDB {
	return &AsyncEMSMDB{ems: ems, waitInterval: asyncWaitInterval, cadence: asyncPollCadence}
}

// SetWaker wires the push wake source so a parked EcDoAsyncWaitEx wakes the instant
// the session's mailbox changes rather than on the next cadence poll. A nil waker
// leaves the parked wait on its cadence (the degradation floor).
func (a *AsyncEMSMDB) SetWaker(w notify.Registrar) { a.waker = w }

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
// yields FLAG_NOTIFICATION_PENDING; an async handle naming no live session, or one
// owned by another user, is rejected.
//
// When nothing is pending, the call PARKS: a goroutine polls the shared store off the
// IN goroutine and delivers the reply on the OUT channel once a notification arrives
// or the wait interval elapses (flags_out 0). Parking returns faultParked so the
// dispatcher frames no synchronous reply. Only one wait parks per session; a second
// while one is parked returns flags_out 0 immediately rather than stacking a poller.
// Without a transport (a direct unit test, sess.vc nil) the wait cannot park, so it
// answers from the immediate poll.
func (a *AsyncEMSMDB) asyncWaitEx(sess *Session, stub []byte) ([]byte, uint32) {
	in, err := pullAsyncWaitEx(stub)
	if err != nil {
		return nil, ndr.FaultNdr
	}
	s, ok := a.ems.lookup(in.acxh.GUID)
	if !ok || s.user != sess.User {
		return pushAsyncWaitExOut(0, ecRejected), 0
	}
	if s.rop != nil && s.rop.PollForChange() {
		return pushAsyncWaitExOut(flagNotificationPending, ecSuccess), 0
	}
	if sess.vc == nil || !s.waiting.CompareAndSwap(false, true) {
		// No transport to park on, or a wait is already parked for this session.
		return pushAsyncWaitExOut(0, ecSuccess), 0
	}
	// Capture the framing on the IN goroutine before the poller starts.
	go a.parkAndReply(s, sess.vc, sess.curCallID, sess.curContextID, sess.maxFrag)
	return nil, faultParked
}

// parkAndReply polls the session's shared store until a notification is queued or the
// wait interval elapses, then frames the EcDoAsyncWaitEx reply (FLAG_NOTIFICATION_
// PENDING on a notification, 0 on timeout) to match the parked request and delivers it
// on the OUT channel. It abandons the reply if the virtual connection tears down. The
// poll reuses the same PollForChange wake primitive the MAPI/HTTP NotificationWait
// drives, so a delivery made through any store handle wakes it.
//
// The reply rides the OUT channel concurrently with the IN goroutine's synchronous
// fragments. send() preserves PDU framing, so the worst case is PDU-order interleaving
// (this reply spliced between two fragments of an Execute response), never byte
// corruption — and only when an Execute response exceeds ourMaxFrag (5840) and so
// fragments. In the [MS-OXCRPC] topology AsyncEMSMDB binds as its own interface on a
// connection that carries no Execute traffic, so that window does not arise in practice.
func (a *AsyncEMSMDB) parkAndReply(s *emsmdbSession, vc *vconn, callID uint32, contextID uint16, maxFrag int) {
	defer s.waiting.Store(false)
	deadline := time.Now().Add(a.waitInterval)
	flagsOut := uint32(0)
	// Register the session's mailbox for a push wake before the first poll, so a
	// change that lands during the wait wakes it at once. A nil waker (push disabled)
	// or a session with no opened store leaves wake nil, falling back to the cadence.
	var wake <-chan struct{}
	if a.waker != nil && s.rop != nil {
		ch, cancel := a.waker.Register(s.rop.MailboxDir())
		defer cancel()
		wake = ch
	}
	for {
		if s.rop != nil && s.rop.PollForChange() {
			flagsOut = flagNotificationPending
			break
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break // timeout: flags_out stays 0
		}
		select {
		case <-vc.closed:
			return // the connection tore down; abandon the parked reply
		case <-wake:
			// a push wake — loop and PollForChange observes the change
		case <-time.After(min(a.cadence, remaining)):
		}
	}
	for _, pdu := range fragmentResponse(callID, contextID, pushAsyncWaitExOut(flagsOut, ecSuccess), maxFrag) {
		vc.send(pdu)
	}
}
