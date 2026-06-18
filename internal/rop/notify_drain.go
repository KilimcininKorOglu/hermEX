package rop

import (
	"slices"

	"hermex/internal/ext"
)

// notifyBufferCap bounds one Execute response's ROP buffer, matching the reference's
// fixed 0x8000 (32 KB) working buffer. The notify drain stops and emits RopPending
// once the next RopNotify would push the response past this cap, carrying the
// remaining notifications over to the next Execute (the internal spec §5).
const notifyBufferCap = 0x8000

// queuedNotify is one classified event awaiting serialization as a RopNotify. It
// pairs the subscription's handle and logon id (echoed as the NotificationHandle and
// LogonId) with the event. The queue persists on the session across Execute calls so
// an event that overflows one response is delivered by the next — never dropped: the
// folder snapshot has already advanced past it, so it cannot be re-detected.
type queuedNotify struct {
	handle  uint32
	logonID uint8
	n       notification
}

// poll detects mailbox changes for the session's subscriptions and drains the
// resulting notifications into the Execute response. It mirrors the reference's
// end-of-Execute notify drain but is sourced by polling the shared store rather
// than an async push queue, since hermEX has no central store daemon to push from
// (the internal spec §9). It runs after the ROP batch on every Execute —
// including an empty one, which is how a wake-up Execute collects
// pending notifications — and is a no-op when the session has no subscriptions or
// pending events.
func (s *Session) poll(out *ext.Push) {
	s.enqueueChanges()
	s.drainNotifications(out)
}

// enqueueChanges polls every subscription and appends each matching event to the
// session notify queue. Subscriptions are visited in handle order for a deterministic
// batch; a folder- or message-scoped one polls its single folder, a whole-store one
// sweeps every content folder. A store error skips that subscription without advancing
// its baseline, so the next poll retries the same diff rather than losing a change.
func (s *Session) enqueueChanges() {
	subs := make([]uint32, 0)
	for h, o := range s.handles {
		if o.kind == kindSubscription && o.store != nil {
			subs = append(subs, h)
		}
	}
	slices.Sort(subs)
	for _, h := range subs {
		o := s.handles[h]
		switch {
		case o.sub.wholeStore:
			s.enqueueWholeStore(o)
		case o.sub.folderID != 0:
			o.subSnapshot = s.pollFolder(o, o.sub.folderID, o.subSnapshot)
		}
	}
}

// pollFolder diffs one folder against its prior snapshot, enqueues every event the
// subscription matches, and returns the refreshed snapshot. On a store error it keeps
// the old baseline so the next poll retries the same diff rather than dropping a change.
func (s *Session) pollFolder(o *object, folderID int64, prev folderSnapshot) folderSnapshot {
	events, snap, err := detectContentChanges(o.store, folderID, prev)
	if err != nil {
		return prev
	}
	for i := range events {
		scopeMessageID, typeBit := classifyScope(&events[i])
		if o.sub.matches(folderID, scopeMessageID, typeBit) {
			s.pending = append(s.pending, queuedNotify{handle: o.sub.handle, logonID: o.sub.logonID, n: events[i]})
		}
	}
	return snap
}

// enqueueWholeStore polls every content folder for a whole-store subscription,
// keeping one snapshot per folder. A folder created since the last poll is diffed
// against a nil baseline, so a message that arrives in it surfaces as a create; a
// folder that has vanished is dropped from the baseline — its folder-level delete
// event is a later increment, so here only its messages stop being polled.
func (s *Session) enqueueWholeStore(o *object) {
	folders, err := o.store.ListFolders()
	if err != nil {
		return
	}
	if o.subFolders == nil {
		o.subFolders = make(map[int64]folderSnapshot, len(folders))
	}
	live := make(map[int64]bool, len(folders))
	for _, f := range folders {
		live[f.ID] = true
		o.subFolders[f.ID] = s.pollFolder(o, f.ID, o.subFolders[f.ID])
	}
	for fid := range o.subFolders {
		if !live[fid] {
			delete(o.subFolders, fid)
		}
	}
}

// drainNotifications serializes queued notifications into the response until the next
// RopNotify would overflow notifyBufferCap, at which point it emits one RopPending and
// stops — leaving the unsent notifications queued for the next Execute (§5). A
// notification whose subscription handle was released in the meantime is dropped,
// matching the reference (a released handle has no object to notify). Each RopNotify
// is serialized into a scratch buffer first so the cap is checked before any bytes
// reach the response (ext.Push cannot rewind a partial write).
func (s *Session) drainNotifications(out *ext.Push) {
	for len(s.pending) > 0 {
		qn := s.pending[0]
		if s.get(qn.handle) == nil {
			s.pending = s.pending[1:]
			continue
		}
		scratch := ext.NewPush(ext.FlagUTF16)
		if err := pushNotify(scratch, qn.handle, qn.logonID, &qn.n); err != nil {
			s.pending = s.pending[1:] // an unserializable event is dropped rather than wedging the queue
			continue
		}
		if out.Len()+scratch.Len() > notifyBufferCap {
			pushPending(out, 0) // session index 0 — one session per connection in v1
			return
		}
		out.Raw(scratch.Bytes())
		s.pending = s.pending[1:]
	}
}

// pushPending appends a RopPending ([MS-OXCROPS] 2.2.14.3, the internal spec
// §5): the 0x6E marker plus the session index. It tells the client that more
// notifications are waiting and it should call Execute again.
func pushPending(out *ext.Push, sessionIndex uint16) {
	out.Uint8(ropPending)
	out.Uint16(sessionIndex)
}
