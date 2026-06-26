package imap

// QUOTA (RFC 2087). hermEX has one quota root per mailbox — the store root,
// addressed by the empty name "" — so the resource model is a single STORAGE
// limit read from the objectstore quota properties against the live mailbox size.

// writeQuota emits an untagged QUOTA response for the mailbox's single root. The
// STORAGE values are in units of 1024 octets (RFC 2087); an unlimited mailbox
// (no limit set) reports an empty resource list.
func (c *conn) writeQuota(root string) {
	q, _ := c.st.GetQuota()
	used, _ := c.st.MailboxSize()
	limitKB := q.StorageKB
	if limitKB == 0 {
		limitKB = q.ReceiveKB // fall back to the hard receive limit
	}
	if limitKB == 0 {
		c.untagged("QUOTA %s ()", quoteString(root))
		return
	}
	c.untagged("QUOTA %s (STORAGE %d %d)", quoteString(root), used/1024, limitKB)
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
	c.untagged(`QUOTAROOT %s ""`, quoteString(mailbox))
	c.writeQuota("")
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
	c.writeQuota("")
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
