package imap

import (
	"hermex/internal/logging"
	"hermex/internal/objectstore"
)

// QUOTA (RFC 2087). hermEX has one quota root per mailbox, the store root,
// addressed by the empty name "", so the resource model is a single STORAGE
// limit read from the objectstore quota properties against the live mailbox size.

// writeQuota emits an untagged QUOTA response for the mailbox's single root. The
// STORAGE values are in units of 1024 octets (RFC 2087); an unlimited mailbox
// (no limit set) reports an empty resource list. A limit or usage that cannot be
// read is recorded and reported false, so the caller answers NO instead of telling
// the client it has no limit.
func (c *conn) writeQuota(root string) bool {
	q, err := c.st.GetQuota()
	if err != nil {
		c.quotaReadFailed(err)
		return false
	}
	used, err := c.st.MailboxSize()
	if err != nil {
		c.quotaReadFailed(err)
		return false
	}
	c.emitQuota(root, q, used)
	return true
}

// quotaReadFailed records a quota or mailbox-size read that failed.
func (c *conn) quotaReadFailed(err error) {
	c.event(logging.LevelError, "quota.read.fail", logging.Fields{"error": err.Error()})
}

// emitQuota writes the QUOTA response for root from the stored limits and the
// mailbox size in bytes.
func (c *conn) emitQuota(root string, q objectstore.QuotaLimits, used int64) {
	limitKB := q.StorageKB
	if limitKB == 0 {
		limitKB = q.ReceiveKB // fall back to the hard receive limit
	}
	if limitKB == 0 {
		c.untagged("QUOTA %s ()", safeQuoted(root))
		return
	}
	c.untagged("QUOTA %s (STORAGE %d %d)", safeQuoted(root), used/1024, limitKB)
}

// cmdGetQuotaRoot handles GETQUOTAROOT: it names the quota root(s) for a mailbox
// (always the single root "") and reports that root's quota.
func (c *conn) cmdGetQuotaRoot(tag string, args []token) {
	if c.state == stateNotAuth {
		c.no(tag, "must authenticate first")
		return
	}
	mailbox, ok := arg0(args)
	if !ok {
		c.bad(tag, "GETQUOTAROOT requires a mailbox")
		return
	}
	// RFC 9208 4.3: a mailbox that does not exist is answered NO.
	if _, _, _, found := c.resolveACLFolder(mailbox); !found {
		c.no(tag, "no such mailbox")
		return
	}
	c.untagged(`QUOTAROOT %s ""`, safeQuoted(mailbox))
	if !c.writeQuota("") {
		c.no(tag, "cannot read the mailbox quota")
		return
	}
	c.ok(tag, "GETQUOTAROOT completed")
}

// cmdGetQuota handles GETQUOTA: it reports the named quota root. Only the single
// root "" exists.
func (c *conn) cmdGetQuota(tag string, args []token) {
	if c.state == stateNotAuth {
		c.no(tag, "must authenticate first")
		return
	}
	root, ok := arg0(args)
	if !ok {
		c.bad(tag, "GETQUOTA requires a quota root")
		return
	}
	if root != "" {
		c.no(tag, "no such quota root")
		return
	}
	if !c.writeQuota("") {
		c.no(tag, "cannot read the mailbox quota")
		return
	}
	c.ok(tag, "GETQUOTA completed")
}

// cmdSetQuota handles SETQUOTA: changing a mailbox quota is an administrative
// operation, so an IMAP client cannot raise its own quota.
func (c *conn) cmdSetQuota(tag string) {
	if c.state == stateNotAuth {
		c.no(tag, "must authenticate first")
		return
	}
	c.no(tag, "setting quota is not permitted")
}
