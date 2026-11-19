# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.3.2] - 2026-11-19

### Added

- `install.sh install` sets up the first mail domain: it asks for the domain (or takes `--domain`), creates its `postmaster@` system administrator with a password shown once, turns on antivirus scanning, generates the DKIM key and saves the DNS records to publish to `/root/hermex-dns-<domain>.txt`.
- `install.sh install` offers Let's Encrypt certificates (`--acme-email`, `--agree-tos`) when the host name already resolves to the server, and then an MTA-STS policy in testing mode (`--mtasts`, RFC 8461) when `mta-sts.<domain>` points at the server.
- The admin panel can be published under its own host name (`admin_hostname`): the gateway routes only that name to the panel and obtains its certificate. `install.sh install` asks for it (`--admin-hostname`) and adds its CNAME to the DNS records file; with no name the panel stays reachable only through an SSH tunnel.
- `hermex-admin` commands `generate-dkim`, `enable-dkim`, `dns-records`, `set-av-scan`, `set-tls-acme` and `set-mtasts`, so a domain's mail can be set up without the panel. `generate-dkim` keeps an existing key, and `set-mtasts enforce` needs `--confirm-enforce`, as in the panel.

### Fixed

- A fresh install stopped at the firewall step: firewalld names the 587/tcp service `smtp-submission`, not `submission`.
- The DNS records prescribed for a domain served by `mail.<domain>` no longer include a `mail` CNAME, which RFC 1034 section 3.6.2 forbids beside the server's own address records.
- The CardDAV `path=/dav` TXT record (RFC 6764 section 6) is prescribed as its own record instead of only in a note.

### Changed

- Tests now fail on implementations they passed before: ActiveSync Find, FolderCreate, FolderDelete, FolderUpdate, EmptyFolderContents and MeetingResponse, and the failed-login logging checks of ActiveSync, DAV, EWS and MAPI/HTTP, which now also reject a logged base64 Basic credential.

## [0.3.1] - 2026-11-19

### Added

- The message submission port (RFC 6409, `submission_addr`, `:587` on a new bare-metal install): STARTTLS, and MAIL is refused with 530 until AUTH succeeds. `install.sh` opens it in the firewall.

### Security

- Builds use Go 1.26.11, golang.org/x/net v0.63.0 and klauspost/compress v1.20.4, which close 17 vulnerabilities reachable from the 0.3.0 binaries (net/http, crypto/x509, x/net, compress).
- Webmail uses DOMPurify 3.4.18, which closes five DOM XSS advisories in the sanitizer that every mail-derived HTML sink goes through; the lint and test tools are updated past their advisories.

## [0.3.0] - 2026-11-19

### Added

- Bare-metal installation: `install.sh` installs MariaDB, MongoDB and ClamAV, a service user, one systemd unit per daemon and the firewall rules on RHEL/AlmaLinux/Rocky 9, Debian 12 and Ubuntu 22.04/24.04, then installs the release. `update` takes a database dump, swaps the release and migrates; `rollback` restores the previous release and its dump, and refuses a rollback across a mailbox-store migration. Each tagged release carries `hermex-linux-amd64.tar.gz`, `hermex-linux-arm64.tar.gz` and `SHA256SUMS`.
- Every daemon can take its own health address (`health_addrs`) and its own log spill file, so all of them run on one host.
- EWS calendar support: calendar items are served by FindItem (with CalendarView expansion of recurring series), GetItem and SyncFolderItems, and can be created, edited, moved, copied and deleted, including single occurrences and series a MAPI client wrote. Tasks and sticky notes are created and edited through the shared task model.
- EWS search folders, FindItem and FindFolder restrictions, extended properties on reads and writes, InternetMessageHeaders, CalendarPermissions, and counter proposals from a meeting response.
- MAPI search folders, folder entry ids, rights and counts, the store's size and owner, the replica id map, the connection locale, RopGetPropertiesList and RopReadRecipients past the inline table, and the TNEF form for recipients that take rich information.
- ActiveSync moves calendar, contact, task and note items, answers one occurrence of a series, proposes new meeting times, honors MaxAmbiguousRecipients and sends read receipts.
- Read receipts (RFC 8098) and delivery reports are read and written, and webmail prompts for read receipts by the reader's setting.
- Incoming winmail.dat (TNEF) is read into the message, OpenPGP/MIME messages stay intact, vCard 2.1 is read, and attached contacts travel as vCards.
- Webmail saved searches are store search folders, folders are addressed by id, meeting mail offers Outlook's answer choices, and messages can be viewed as source.
- The exclusive sent-copy setting files the copy only in the represented mailbox.
- The admin panel shares the webmail's design language, manages Directory Sync connections and domain bindings and the Live status targets, and shows times in the operator's chosen zone.
- `hermex-admin add-health-target` registers a Live status target from the command line.

### Changed

- Account creation, LDAP sync and room creation create the mailbox with its standard folders at once; maintenance reads never create one.
- Failed-login, connection and HTTP request limits count an IPv6 client by its /64 and evict the least recently used client when their tables are full.
- Every file the server writes goes through one durable atomic replacement helper.
- Builds target linux/amd64 and linux/arm64 only.

### Fixed

- Two daemons opening a new relay spool or mailbox at once no longer fail with SQLITE_BUSY or read a half-built schema.
- Every protocol refuses writes into a mailbox over its quota with that protocol's quota error.
- Writes stay inside the folder their item id or endpoint names, and requests for a mailbox that does not exist are refused.
- Free/busy windows are bounded on every surface, BusyStatus values outside the defined set are refused, and every occurrence of a series is counted.
- Folder names stay unique among siblings on create, rename and move across all protocols.
- ActiveSync answers each failure with its MS-ASCMD status, keeps a cached Ping heartbeat at the 60-second floor, and updates its shared sync state inside one transaction.
- Meeting replies, cancellations and delegate answers keep the right series, instance and response, and a meeting in another mailbox is answered only under a send grant.
- Out-of-office windows that end before they start are refused on every surface.
- A daily series that began years before the window is expanded.
- Many admin panel, webmail and MAPI fixes for time zones, delegates, attachments and table columns.

### Security

- The SMTP DATA body, IMAP literals and IDLE lines, fetchmail responses, MIME part counts, ICS uploads and relay turns are bounded.
- A submission or transaction past 1000 recipients is refused, and a DMARC report goes to at most 10 destinations.
- The From of every client-built or scheduled message passes the send-as check; a PLAIN login cannot act as another user.
- The SSRF guard refuses every non-global IANA block, zoned and shorthand hosts, and IPv6 addresses that embed a private IPv4 host.
- MAPI/HTTP and RPC/HTTP sessions are bound to the authenticated user.
- The ActiveSync policy key is given only to a compliant device that acknowledges it.
- MTA-STS policies are read only when served as text/plain, and pkcs7 is bumped for BER length bounds.

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
