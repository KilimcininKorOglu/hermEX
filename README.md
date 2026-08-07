# hermEX Mail Server

hermEX is a modular, native Microsoft Exchange Server replacement written
entirely in Go. It speaks the Exchange client protocols on the wire (IMAP, POP3,
SMTP, CalDAV, CardDAV, ActiveSync, EWS, MAPI/HTTP with RPC/HTTP and ROP, and
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
protocol by longest-prefix match (`/autodiscover/`, `/ews/`,
`/microsoft-server-activesync`, `/mapi/`, `/rpc/`, `/rpcwithcert/`, `/dav/`,
`/.well-known/{cal,card}dav`) and serves the webmail SPA at the catch-all `/`, so
the whole stack is reachable behind one FQDN. It terminates TLS from a per-SNI
certificate store with optional ACME issuance (`internal/tlscert`).

### Components

| Layer             | Packages                                                                                                                                         |
|-------------------|--------------------------------------------------------------------------------------------------------------------------------------------------|
| MAPI core         | `internal/mapi` (property model), `internal/ext` (MS-wire serialization), `internal/ndr` (RPC NDR), `internal/lzxpress` (ROP buffer compression) |
| Mailbox store     | `internal/objectstore` (sole store), `internal/ics` (IDSET/GLOBSET sync codec), `internal/publicfolder` (public store)                           |
| Format conversion | `internal/oxcmail` (MIME to MAPI and back), `internal/oxcical` (iCalendar), `internal/oxvcard` (vCard), `internal/oxtask` (tasks), `internal/recurrence`, `internal/conversation`, `internal/mime`, `internal/smime` |
| Protocol servers  | `internal/{smtp,imap,pop3,dav,activesync,ews,mapihttp,nspi,rop}`, transport codecs `internal/{rpchttp,wbxml,oxews,oxmapihttp}`, `internal/easpolicy` |
| Mail flow         | `internal/mta` (delivery), `internal/relay` + `internal/spooler` (outbound), `internal/meeting`, `internal/fetchmail`                            |
| Filtering         | `internal/antispam` (SPF/DKIM/DMARC + Bayes + rules), `internal/antivirus` (clamd), `internal/quarantine`                                        |
| Security & TLS    | `internal/dkimsign`, `internal/mtasts`, `internal/dane`, `internal/tlsrpt`, `internal/tlscert` (per-SNI cert store + ACME), `internal/ssrfguard`  |
| Abuse control     | `internal/authlimit` (failed-login lockout), `internal/httplimit` (per-client request cap); both are DB-backed and tunable without a restart      |
| Directory & auth  | `internal/directory` (MariaDB-backed), `internal/ldapauth` + `internal/ldapsync` (AD/LDAP sync)                                                  |
| Notifications     | `internal/notify` (publisher/consumer) + `internal/notifyd` (SSE relay), a wake bus for IDLE/Ping/streaming across daemons                       |
| Platform          | `internal/config`, `internal/serve` (HTTP daemon base), `internal/lifecycle` (graceful shutdown), `internal/logging` (Mongo sink), `internal/health`, `internal/migrate` (schema runner), `internal/buildinfo` (source stamp), `internal/tlstest` |
| Web & admin       | `internal/webmail2` (React SPA) + `internal/webmail2api`, `internal/admin` (operator panel), `internal/gateway` (single-FQDN front door)         |

## Development

Development is Docker-based and driven entirely through the `Makefile`, which
wraps `docker compose` and runs the toolchain in the dev container (the host Go
toolchain has no MariaDB, so DB-backed tests skip and silently hide failures).
There is no CI pipeline: `make gate` run locally is the only quality gate.

```sh
make up                                   # start dev env (MariaDB + Mongo + ClamAV + toolchain + all services)
make build                                # compile every binary into bin/
make gate                                 # fmt-check + vet + full test, the pre-commit gate
make test PKG=./internal/objectstore      # one package
make test PKG=./internal/objectstore RUN=TestCreateMessage   # one test
make test-host PKG=./internal/rop         # host quick feedback (DB-backed tests skip)
make test-race PKG=./internal/rop         # race detector
make tidy                                 # sync go.mod / go.sum
make rebuild SVC=webmail2                 # rebuild + restart one service after a code change
make images                               # rebuild every service image
make down                                 # stop dev env
```

All test targets bake in `-count=1` (Go's test cache returns stale results
otherwise). Run `make gate` clean before every commit.

`go test` prints a per-package failure mid-stream, so the trailing output of a
full run looks the same whether it passed or not. Read the exit code and grep for
failures rather than the tail of the log.

### Webmail2 SPA

The webmail2 service serves a **prebuilt** React bundle from
`internal/webmail2/dist/` (bind-mounted into the container); the Go image does
not run Vite, and `make gate` never touches the frontend. After changing
`internal/webmail2/src/`, rebuild the bundle on the host, and the running
container serves it on the next request:

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

The database port is published on the loopback interface only, so the dev
credentials are not reachable from the network.

Four services are internal-only and expose no host port: **Mongo** (the log
sink), **ClamAV** (`clamav:3310`, the antivirus engine), **notify** (the SSE push
relay), and **pebble** (an offline ACME test CA, used only in TLS `acme` mode).
Nothing `depends_on` them, so the mail path comes up and serves even when any of
them is down.

`cmd/admin serve` (the operator panel) is not in the default compose; run it
manually. It listens on `:8081` (config `admin_addr`) and requires `admin_secret`.

### Build provenance

The container images build from a context that excludes `.git`, so the toolchain
records nothing about the source. The commit and build time are injected at link
time instead, and every daemon reports them on startup and on its health
endpoint, which is what the admin panel's live status view shows. The `Makefile`
is what supplies those values, so build images with `make images` or
`make rebuild SVC=<service>`; a bare `docker compose build` produces binaries that
report an unknown build. A tree with uncommitted changes is stamped with a
`-dirty` suffix, since a bare commit id would claim a source state that was never
built. `make version` prints the values a build would stamp right now.

## Configuration

`config.json` is the sole config file, loaded by every daemon including the
gateway. `docker/config.example.json` is the template; its placeholders are
deliberately non-functional, so an unedited copy fails to connect rather than
starting with a working default credential.

It holds **infrastructure only**: the database DSN and data directory (both
required), listen addresses, gateway backend URLs, TLS certificate paths, secrets,
and the log sink settings. It holds no accounts and no operator policy; both live
in the directory database. There is no `.env` file and no environment-variable
expansion.

Anything an operator tunes at runtime (rate limits, lockout thresholds, protocol
size caps, retention windows, spam thresholds) is stored in the database, managed
from the admin panel, and re-read by a poll, so a change applies within about a
minute without restarting anything.

## Operations

### Schema migrations

All schema changes go through `internal/migrate`, never ad-hoc DDL. Four schemas
are versioned this way: the directory database, the relay spool, and a mailbox's
two SQLite files. Each owns numbered `.sql` files embedded into the binary, and
the highest file number is that schema's version. A daemon applies the pending
migrations for every database it opens, at startup, so whichever service restarts
first with a new image carries the shared databases forward. A daemon that refuses
to start with a message about the schema being newer than the binary is a stale
image, not a broken database: rebuild that service.

### Backups

The directory database is the only copy of things the system generates and cannot
re-derive: every domain's DKIM signing key, every uploaded TLS private key, and
the accounts, aliases, admin roles and policy around them.

```sh
make dump-db                    # write a compressed dump of the whole directory database
make restore-db DUMP=<file>     # load one back (this replaces the current directory)
hermex-admin export-dkim <domain>   # write one domain's signing key to stdout
```

Both paths run in the operator's shell, so no private key crosses the network.
Take a dump before an upgrade, and keep it off the mail host.

### Administration

`cmd/admin` is both a provisioning CLI and, under its `serve` subcommand, the
operator panel: domains, users, aliases, mailing lists, delegates, devices, DKIM
keys, DNS checks, the mail queue, quarantine, retention and the spam model. Run it
with no arguments for the full subcommand list. The panel is separate from
end-user webmail and is not behind the gateway.

Every daemon can serve a `/healthz` endpoint reporting its dependency state and
its build stamp. It is opt-in per daemon (`health_addr`, empty disables it), and
the panel's live status view aggregates whichever endpoints are listed under
`health_targets`.

## Layout

| Path        | Purpose                                                                                                                                        |
|-------------|------------------------------------------------------------------------------------------------------------------------------------------------|
| `cmd/`      | Service executables (mta, imap, pop3, webmail2, dav, activesync, ews, mapihttp, gateway, notify, admin, fetchmail, antispam-bootstrap, antispam-rules) |
| `internal/` | Shared libraries: MAPI core, mailbox store, format conversion, protocol servers, mail flow, filtering, security/TLS, directory, notifications, platform |
| `docker/`   | Dev and service container images                                                                                                               |

## Key facts

- **Database:** MariaDB (`email`) via `go-sql-driver/mysql`; password hashing is `crypt_sha512` at a high round count, and a successful login re-hashes a credential stored below the current cost. Tests use a separate, auto-created `hermex_test` database.
- **Mailbox store:** `internal/objectstore`, per-mailbox SQLite, addressed by built-in `PrivateFID_*` folder constants, never by name lookup. Opening a mailbox takes a shared advisory lock, so maintenance passes that reclaim storage can tell a live mailbox from an idle one instead of racing a writer.
- **Auth and accounts:** `internal/directory` backed by MariaDB. Address-book queries are scoped to the caller's organization, or to the caller's own domain when the domain belongs to none.
- **Mail construction:** `internal/oxcmail.Export()` is the single path from a MAPI object to MIME bytes; outgoing mail is never hand-rolled. Every send path converges on one delivery function, so the DKIM signer always sees the final bytes.
- **Inbound filtering:** delivery scores each message through `internal/antispam` (SPF/DKIM/DMARC auth + a Bayes classifier + rules), then narrows the verdict by operator and recipient allow-block tiers. Each DNS-dependent check carries a deadline, since the queried nameserver belongs to the sender.
- **Antivirus:** delivery streams each message to ClamAV (`internal/antivirus`); a hit is quarantined (`internal/quarantine`) and the recipient plus domain admins are notified. The scan fails open, so a down clamd never blocks mail.
- **Push:** `internal/notify` + `cmd/notify` are a self-standing SSE wake bus. Long-poll consumers (MAPI/HTTP async, EWS streaming, IMAP IDLE, EAS Ping, DAV push) wake on a mailbox change; a down relay silently drops every consumer back to polling.
- **Logging:** a self-healing MongoDB sink. If Mongo is down, events spill to disk and replay on reconnect, and the mail path never hard-depends on it.

## License

Licensed under the MIT License. See the [LICENSE](LICENSE) file.

## Acknowledgements

hermEX is a Go rewrite of the [gromox](https://gromox.com/) project.
