-- Widen fetchmail.src_password so it can hold the wrapped form. The source
-- password is now sealed with the key secret like every other stored credential,
-- and the wrapped value is the 7-byte prefix plus the base64 of a 12-byte nonce,
-- the password and a 16-byte tag, which overflows 255 bytes for a password longer
-- than 154 bytes. Validate caps the password at 512 bytes, whose wrapped form
-- needs 743. MODIFY to the same definition is a no-op, so the file can run again.
ALTER TABLE fetchmail MODIFY COLUMN src_password VARCHAR(1024) NOT NULL DEFAULT '';
