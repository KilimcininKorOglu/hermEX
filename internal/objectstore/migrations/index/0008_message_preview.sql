-- Two more denormalized projections on the index row, for the same reason the
-- subject and sender already live here: a folder listing must not read every
-- message's wire form to render a row.
--
-- preview is the first line of the body, shown under the subject in a message
-- list. has_attach drives the paperclip: it counts only attachments a reader
-- would call attachments, so a signature logo referenced by the HTML body does
-- not mark every message as carrying one.
--
-- Both default to the empty answer, so a store migrated from an earlier version
-- lists exactly as it did until its rows are backfilled.
ALTER TABLE messages ADD COLUMN preview TEXT NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN has_attach INTEGER NOT NULL DEFAULT 0;
