# hermEX Mail Server

hermEX is a modular, native Microsoft Exchange Server replacement written
entirely in Go. It speaks the Exchange client protocols on the wire (IMAP, POP3,
SMTP/LMTP, CalDAV, CardDAV, ActiveSync, EWS, MAPI/HTTP with RPC/HTTP and ROP, and
NSPI, the global address list), so existing Outlook, mobile, and standards-based
clients connect unmodified. The server daemons, the webmail UI, the admin
tooling, and every sync interface are pure Go.

## Why?

Why not?

## Status

Active development. Not yet production-hardened.

## Architecture

The project draws one hard line:

- **External client protocols are wire-compatible with Microsoft Exchange.**
  What an Outlook or mobile client sees on the wire matches Exchange behavior:
  IMAP, POP3, SMTP, CalDAV, CardDAV, ActiveSync (WBXML), EWS (SOAP), MAPI/HTTP +
  RPC/HTTP + ROP, and NSPI.
- **Everything internal is original.** The IPC, the daemon boundaries, and the
  config surface are the project's own design.

The one deliberate exception is the **physical mailbox store**
(`internal/objectstore`): it mirrors a proven per-mailbox SQLite schema (generic
property tables + content-addressed CID files). Each mailbox is a directory
holding `objects.sqlite3` + `imapindex.sqlite3` + `cid/` + `eml/`.

A single TLS front door (`cmd/gateway`) reverse-proxies every HTTP-based
protocol (`/ews`, `/Microsoft-Server-ActiveSync`, `/mapi/*`, `/rpc`, `/dav`) and
serves the webmail SPA at `/`, so the whole stack is reachable behind one FQDN.

### Components

| Layer             | Packages                                                                                                                                         |
|-------------------|--------------------------------------------------------------------------------------------------------------------------------------------------|
| MAPI core         | `internal/mapi` (property model), `internal/ext` (MS-wire serialization), `internal/ndr` (RPC NDR), `internal/lzxpress` (ROP buffer compression) |
| Mailbox store     | `internal/objectstore` (sole store), `internal/ics` (IDSET/GLOBSET sync codec)                                                                   |
| Format conversion | `internal/oxcmail` (MIME ↔ MAPI), `internal/oxcical` (iCalendar), `internal/oxvcard` (vCard), `internal/mime`, `internal/smime`                  |
| Protocol servers  | `internal/{smtp,imap,pop3,dav,activesync,ews,mapihttp,nspi,rop}`                                                                                 |
| Mail flow         | `internal/mta` (delivery), `internal/relay` (outbound spool), `internal/antispam`, `internal/dkimsign`, `internal/mtasts`                        |
| Directory & auth  | `internal/directory` (MariaDB-backed), `internal/ldapauth` + `internal/ldapsync` (AD/LDAP sync)                                                  |
| Web & admin       | `internal/webmail2` (React SPA) + `internal/webmail2api`, `internal/admin` (operator panel)                                                      |

## Development

Development is Docker-based and driven entirely through the `Makefile`, which
wraps `docker compose` and runs the toolchain in the dev container (the host Go
toolchain has no MariaDB, so DB-backed tests skip and silently hide failures).

```sh
make up                                   # start dev env (MariaDB + Mongo + toolchain + all services)
make build                                # compile every binary into bin/
make gate                                 # fmt-check + vet + full test, the pre-commit gate
make test PKG=./internal/objectstore      # one package
make test PKG=./internal/objectstore RUN=TestCreateMessage   # one test
make test-host PKG=./internal/rop         # host quick feedback (DB-backed tests skip)
make rebuild SVC=webmail2                 # rebuild + restart one service after a code change
make down                                 # stop dev env
```

All test targets bake in `-count=1` (Go's test cache returns stale results
otherwise). Run `make gate` clean before every commit.

### Webmail2 SPA

The webmail2 service serves a **prebuilt** React bundle from
`internal/webmail2/dist/` (bind-mounted into the container); the Go image does
not run Vite. After changing `internal/webmail2/src/`, rebuild the bundle on the
host, and the running container serves it on the next request:

```sh
cd internal/webmail2
npm run build        # regenerate dist/
npm run lint         # eslint, max-warnings 0
npm run typecheck    # tsc --noEmit
npm test             # vitest
```

`make rebuild SVC=webmail2` rebuilds only the Go backend, not the bundle.

### Service ports

`make up` exposes the 8140-8149 host block; webmail2 runs alongside at 8150.

| Service       | Host | Container |
|---------------|------|-----------|
| SMTP (mta)    | 8140 | 25        |
| POP3          | 8141 | 110       |
| MariaDB       | 8142 | 3306      |
| IMAP          | 8143 | 143       |
| DAV           | 8145 | 8080      |
| ActiveSync    | 8146 | 8080      |
| EWS           | 8147 | 8080      |
| MAPI/HTTP     | 8148 | 8080      |
| Gateway (TLS) | 8149 | 8080      |
| Webmail2      | 8150 | 8080      |

Mongo (the log sink) has no host port. `cmd/admin serve` (the operator panel) is
not in the default compose; run it manually. It listens on `:8081` and requires
`admin_secret`.

### Key facts

- **Database:** MariaDB (`email`) via `go-sql-driver/mysql`; password hashing `crypt_sha512`. Tests use a separate, auto-created `hermex_test` database.
- **Mailbox store:** `internal/objectstore`, per-mailbox SQLite, addressed by built-in `PrivateFID_*` folder constants, never by name lookup.
- **Auth & accounts:** `internal/directory` backed by MariaDB. Config JSON holds infrastructure only (DB DSN, hostname, data_dir), never accounts or credentials.
- **Mail construction:** `internal/oxcmail.Export()` is the single path from a MAPI object to MIME bytes; outgoing mail is never hand-rolled.
- **Inbound filtering:** delivery scores each message through `internal/antispam` (SPF/DKIM/DMARC auth + a Bayes classifier + rules), then narrows the verdict by operator and recipient allow-block tiers.
- **Logging:** a self-healing MongoDB sink. If Mongo is down, events spill to disk and replay on reconnect, and the mail path never hard-depends on it.

## Layout

| Path        | Purpose                                                                                                                                        |
|-------------|------------------------------------------------------------------------------------------------------------------------------------------------|
| `cmd/`      | Service executables (mta, imap, pop3, webmail2, dav, activesync, ews, mapihttp, gateway, admin, fetchmail, antispam-bootstrap, antispam-rules) |
| `internal/` | Shared libraries: MAPI core, mailbox store, format conversion, protocol servers, mail flow, directory                                          |
| `docker/`   | Dev and service container images                                                                                                               |

## License

Licensed under the MIT License. See the [LICENSE](LICENSE) file.

## Acknowledgements

hermEX is a Go rewrite of the [gromox](https://gromox.com/) project.
