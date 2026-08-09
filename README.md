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

| Target                    | What it does                                                        |
|---------------------------|---------------------------------------------------------------------|
| `make up`                 | Start the dev environment (MariaDB, Mongo, ClamAV, toolchain, every mail service) |
| `make down`               | Stop the dev environment                                            |
| `make build`              | Compile every command binary into `bin/`                            |
| `make gate`               | `fmt-check` + `vet` + full test, the pre-commit gate                 |
| `make test`               | Full test run in the dev container                                  |
| `make test-host`          | Host quick-feedback run; DB-backed tests skip                       |
| `make test-race`          | Race-detector run in the dev container                              |
| `make fmt`                | `gofmt -w` over the source tree                                     |
| `make fmt-check`          | Fail when any file needs `gofmt`                                    |
| `make vet`                | `go vet`                                                            |
| `make tidy`               | Sync `go.mod` / `go.sum`, downloading any new dependency             |
| `make images`             | Rebuild every service image with the source stamp                   |
| `make rebuild SVC=<name>` | Rebuild and restart one service                                     |
| `make dump-db`            | Write a compressed dump of the whole directory database             |
| `make restore-db DUMP=<file>` | Load a dump back                                                |
| `make version`            | Report the source state a build would stamp                         |
| `make compose-check`      | Validate the compose file syntax                                    |
| `make clean`              | Remove built binaries                                               |
| `make help`               | List every target                                                   |

`test`, `test-host` and `test-race` all take `PKG=` and `RUN=` to narrow the run:

```sh
make test PKG=./internal/objectstore
make test PKG=./internal/objectstore RUN=TestCreateMessage
```

All three bake in `-count=1` (Go's test cache returns stale results otherwise).
Run `make gate` clean before every commit.

`go test` prints a per-package failure mid-stream, so the trailing output of a
full run looks the same whether it passed or not. Read the exit code and grep for
failures rather than the tail of the log.

Some tests need infrastructure and skip without it, which is why a green
`make test-host` proves less than a green `make test`: MariaDB, MongoDB and
clamd are reached through `HERMEX_TEST_MYSQL_DSN`, `HERMEX_TEST_MONGO_URI` and
`HERMEX_TEST_CLAMD_ADDR`, which the dev container sets and the host does not, and
the S/MIME interop tests need `openssl` on PATH. The dev image asserts openssl at
build time, so those four tests always run under `make gate`.

### Webmail2 SPA

The webmail2 service serves a **prebuilt** React bundle from
`internal/webmail2/dist/` (bind-mounted into the container); the Go image does
not run Vite, and `make gate` never touches the frontend. After changing
`internal/webmail2/src/`, rebuild the bundle on the host, and the running
container serves it on the next request:

```sh
cd internal/webmail2
npm run build           # regenerate dist/
npm run dev             # Vite dev server with hot reload
npm run preview         # serve the built bundle locally
npm run lint            # eslint, max-warnings 0
npm run typecheck       # tsc --noEmit
npm test                # vitest, single run
npm run test:watch      # vitest in watch mode
npm run test:coverage   # vitest with coverage
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
gateway. Each daemon takes exactly one flag and no other, so a service is only
ever started one way:

```sh
hermex-<daemon> -config /etc/hermex/config.json
```

That `hermex-` prefix is the name inside the service images. `make build`
compiles the same commands into `bin/` under their bare directory names
(`bin/mta`, `bin/admin`, and so on).

`docker/config.example.json` is the template; its placeholders are deliberately
non-functional, so an unedited copy fails to connect rather than starting with a
working default credential. That holds for the signing secrets too: every daemon
refuses to start while one is still `REPLACE_WITH_A_LONG_RANDOM_STRING`, rather
than signing sessions and wrapping stored private keys with a value published in
this repository. Generate each one separately:

```sh
openssl rand -hex 32
```

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
hermex-admin -config <file> export-dkim <domain>   # one domain's signing key to stdout
```

Both paths run in the operator's shell, so no private key crosses the network.
Take a dump before an upgrade, and keep it off the mail host.

The daemons run unprivileged. `make up` passes your own user and group id to
every mail container, so nothing that parses network input runs as root and the
bind-mounted `docker-data/` tree stays writable by the account that owns it. The
three daemons that listen below port 1024 (SMTP 25, POP3 110, IMAP 143) bind
through a sysctl scoped to their own container rather than through root.

Set `key_secret` in the configuration file. With it, the DKIM and TLS private
keys are encrypted in the database, so a dump on its own no longer hands anyone a
working signing key: opening it needs the configuration file too. Without it the
keys are stored as they always were, and every daemon says so at startup. Keys
written before the secret was set are re-encrypted the next time they are read,
with nothing to run.

The secret is not recoverable. Losing it loses every stored DKIM and TLS key with
it, so back it up separately from the database dump, and export what you need
before changing it.

The mail itself is a separate backup. Each mailbox is its own pair of SQLite
files plus a content tree, none of which the directory dump touches.

```sh
make dump-mail                  # a consistent copy of every mailbox's mail content
hermex-admin -config <file> backup-mail <email|all> <dest-dir>   # one mailbox, or all
```

The copy is taken with the services running: each mailbox's databases are
snapshotted inside a read transaction rather than copied as files, and the
content files are copied afterwards so everything the snapshot references is
present. The cached wire copies are left out on purpose; the store rebuilds one
on the next read of a message, so carrying them would roughly double the backup
for nothing. The result mirrors the live directory layout, which makes restoring
a plain file copy back into place with the mail services stopped.

Both commands write into `docker-data/backup`, which is the same disk as the live
data. **Nothing in this repository schedules them or copies the result anywhere.**
Taking a backup off this host, and doing it on a schedule, is work the operator
has to set up: a host loss, a disk failure or ransomware reaching `docker-data`
destroys the live data and every backup taken so far together. Until that off-host
copy exists, the recovery point is whenever someone last remembered to type the
command. The database dump verifies itself before it is named, so a file under
`docker-data/backup` is a complete dump rather than something a restore discovers
is truncated; that is a check on the copy, not a substitute for having one
elsewhere.

### Administration

`hermex-admin` is both the provisioning CLI and, under its `serve` subcommand,
the operator panel. Every invocation takes `-config <file>` (default
`/etc/hermex/config.json`) before the subcommand.

```sh
hermex-admin -config config.json <command> [args]
```

| Command                                        | Purpose                                                     |
|------------------------------------------------|-------------------------------------------------------------|
| `ensure-schema`                                | Apply pending directory migrations and exit                 |
| `create-domain <domain>`                       | Add a mail domain                                           |
| `create-user <email> <password>`               | Add a mailbox                                               |
| `create-alias <alias> <user-email>`            | Point an address at an existing mailbox                     |
| `create-contact <email> <domain> [name]`       | Add an external mail contact to the address list            |
| `update-contact <email> <name>`                | Rename a contact; an empty name clears it                   |
| `delete-contact <email>`                       | Remove a contact                                            |
| `list-contacts`                                | List every contact                                          |
| `grant-admin <email> <system\|org\|domain> [id]` | Grant an admin role at the given scope                     |
| `list-sessions <email>`                        | Show the account's live webmail and panel sessions          |
| `revoke-sessions <email>`                      | End all of them; the compromise response                    |
| `ldap-sync <org-id>`                           | Import an org's LDAP/AD accounts into the directory         |
| `export-dkim <domain>`                         | Write the domain's DKIM private key to stdout               |
| `sweep-content <email>`                        | Reclaim orphan content files; refuses while the mailbox is in use |
| `prune-eml <email\|all> [days]`                | Drop cached wire copies older than N days (default 30)      |
| `serve`                                        | Run the admin API and panel                                 |

The panel covers domains, users, aliases, mailing lists, delegates, devices, DKIM
keys, DNS checks, the mail queue, quarantine, retention and the spam model. It is
separate from end-user webmail and is not behind the gateway.

Every daemon can serve a `/healthz` endpoint reporting its dependency state and
its build stamp. It is opt-in per daemon (`health_addr`, empty disables it), and
the panel's live status view aggregates whichever endpoints are listed under
`health_targets`.

A serving certificate that is within two weeks of expiry is reported two ways: the
`/healthz` probe marks the daemon degraded, and the certificate poll writes a
warning to the central log every few hours (an error once the certificate has
lapsed). The log path needs no configuration, so a deployment that leaves
`health_addr` empty still learns about a failed renewal.

### Spam model and rules

The Bayes model and the SpamAssassin-style ruleset are seeded by two one-shot
tools rather than fetched at runtime:

```sh
antispam-bootstrap -spam <dir> -ham <dir> [-out model.json]
antispam-rules -from <dir-of-.cf-files> [-out <data_dir>/antispam-rules.cf]
```

`antispam-rules` validates before writing and refuses a directory that yields no
evaluable rules, so a wrong path fails instead of shipping an empty ruleset. The
MTA picks up a new ruleset within a minute, with no restart. Fetching and
verifying the upstream rules stays with the operator's own `sa-update` or a
checkout, which is what does the signature checking.

### External mail retrieval

`fetchmail -config <file>` runs the POP3/IMAP retrieval loop that pulls external
accounts into local mailboxes. The accounts themselves are configured in the
admin panel, not on the command line.

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
