# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
