# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.2.0] - 2026-09-27

### Added

- Outlook meetings and series export as iTIP iCalendar ([MS-OXCICAL]): the method follows the message class, a series carries its RRULE, excluded days and modified occurrences, and a modified occurrence carries the body of its exception attachment. The full AppointmentRecurrencePattern is decoded for this.
- Organizer-side counter proposals ([MS-OXOCAL] 3.1.4.8.5.3): a COUNTER records the proposed time on the attendee and the proposal count on the meeting, and a later plain response withdraws it.
- A delivered meeting cancellation is applied to the calendar, marking the meeting or the one occurrence as cancelled, behind a new per-mailbox setting that webmail shows. Webmail shows a cancelled meeting as cancelled.
- Webmail sends a moved occurrence and cancels a deleted occurrence for the meeting's attendees.
- iTIP bodies are rendered from the stored calendar object, and scheduling mail is built in one shared package.
- Webmail can move, drag, edit and delete one occurrence of a series.
- The browser S/MIME key is kept in a password-sealed vault on the server instead of browser storage, and an existing browser key moves into it on the next unlock.

### Changed

- Webmail edits calendar events, tasks and contacts in place, and EWS, CalDAV and CardDAV update an item in place, so its id and unmodelled properties survive an edit. EWS derives the change key from the item's stored version.
- An edited mail message gets a new IMAP UID, and a message's recipients can be replaced in place.
- Imported events keep their Outlook time zone, zones are named the Windows way, and a VTIMEZONE is generated for them.
- Large webmail pages and components are split into hooks and view components, and unused SPA code and packages are removed.
- npm packages that ship Go sources stay out of the Go module.
- The SPA test runner moves to vitest 5, and the SPA declares its Node floor.

### Fixed

- A MAPI-submitted message keeps its attachments, and a MAPI-submitted meeting message carries its iCalendar, so a counter proposal leaves as METHOD:COUNTER with the proposed time.
- A COUNTER imports as a counter-proposal response, and a response matches a meeting organized in Outlook through its global object id.
- A recipient-row write advances the message's change number, so synced clients see attendee responses.
- Monthly and yearly recurrences expand their day pins (ordinal BYDAY, BYSETPOS, BYMONTHDAY, BYMONTH), and a zoned series expands on its zone's wall clock.
- Meeting mail is processed in every daemon that delivers it, an invitation carries the stored meeting under the UID attendees hold, and only the organizer of a meeting whose request went out sends a cancellation.
- A read never provisions another user's mailbox.
- Drafts keep their Cc and Bcc recipients, autosave while typing, and are removed once sent.
- Webmail restores a setting and reports the failure when a save fails, and keeps stored settings when the page could not read them.
- Webmail sends one reply, note, meeting answer or calendar per click, and the rich text editor keeps the caret where the user types.
- Contacts keep their details on edit, vCard photos become the contact picture, and optional attendees stay optional.
- Dependency advisories in the SPA development tree are resolved.

## [0.1.0] - 2026-09-25

The first numbered release. It covers the whole history up to this point.

### Added

- Exchange client protocols on the wire: IMAP, POP3, SMTP (inbound, submission and delivery), CalDAV, CardDAV, ActiveSync (WBXML), EWS (SOAP), MAPI/HTTP with RPC/HTTP and ROP, and NSPI for the global address list.
- A per-mailbox SQLite object store with content-addressed property data, ICS synchronization state, public folders, and a Recoverable Items soft-delete shared by every protocol.
- MIME, iCalendar, vCard and task conversion to and from MAPI, S/MIME, recurrence, conversations and the master category list, stored once and read identically by every client.
- Inbound filtering: SPF, DKIM and DMARC checks, a Bayes classifier and rule-based scoring, per-recipient allow and block rules, ClamAV scanning and a quarantine with notifications.
- Outbound delivery through a relay spool with DKIM signing, MTA-STS, DANE and TLS reporting, plus storage of inbound DMARC and TLS reports and optional DMARC aggregate reports to the domains that ask for them.
- A directory in MariaDB with sha512-crypt passwords, TOTP second factor, app passwords for client protocols, send-as and send-on-behalf grants, and AD/LDAP sync.
- A single TLS gateway for every HTTP protocol with per-SNI certificates and ACME issuance.
- The webmail SPA (React) with shared mailboxes, calendar, contacts, tasks, notes and per-user settings.
- The admin panel and the `hermex-admin` CLI for domains, users, aliases, mailing lists, devices, DKIM, DNS checks, the mail queue, quarantine, reports and operator settings, all applied without a restart.
- Failed-login, per-client request and connection limits, a push wake bus for IDLE, Ping and streaming clients, health endpoints and a central log store that spills to disk when MongoDB is down.
- One release number in the `VERSION` file, shown by every daemon, the admin panel and the webmail, and `make release` to set the next one.
