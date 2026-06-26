-- QRESYNC (RFC 7162) expunge ledger. When a message leaves the live IMAP view its
-- (folder, uid) is recorded here with the modseq at removal time, so a reconnecting
-- client that supplies a prior modseq can be told which UIDs VANISHED EARLIER. The
-- live index row is gone by then, so this is the only record of the removal.
CREATE TABLE vanished (
	folder_id INTEGER NOT NULL,
	uid INTEGER NOT NULL,
	modseq INTEGER NOT NULL,
	PRIMARY KEY (folder_id, uid));
CREATE INDEX vanished_fid_modseq ON vanished(folder_id, modseq);
