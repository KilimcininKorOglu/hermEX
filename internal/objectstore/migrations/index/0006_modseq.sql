-- CONDSTORE/QRESYNC (RFC 7162) per-message modification sequence. modseq is an
-- IMAP-local monotonic counter, one sequence space per folder (folders.highest_modseq),
-- advanced on every flag change and append so a client can sync deltas by MODSEQ.
-- This lives only in the IMAP index, not the object store, so it does not perturb
-- the MAPI/ICS change-number stream. Existing rows seed at 1 (a CONDSTORE mailbox
-- must report a non-zero HIGHESTMODSEQ).
ALTER TABLE folders ADD COLUMN highest_modseq INTEGER NOT NULL DEFAULT 1;
ALTER TABLE messages ADD COLUMN modseq INTEGER NOT NULL DEFAULT 1;
