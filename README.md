# hermEX Mail Server

hermEX is a modular native Microsoft Exchange Server replacement written
entirely in Go. It implements IMAP, POP3, SMTP/LMTP, CalDAV, CardDAV, ActiveSync, EWS,
MAPI/HTTP with ROP, and NSPI (global address list). Server, webmail UI, admin CLI,
and all sync interfaces are Go.

## Why?

Why not?

## Status

Active development.

## Development

Development is Docker-based.

```sh
# Bring up the whole dev environment: one MariaDB + Go toolchain + all services
docker compose -f hermex-compose.yml up -d

# Build + test in the container (always use -count=1)
docker compose -f hermex-compose.yml exec dev go build ./...
docker compose -f hermex-compose.yml exec dev go test -count=1 ./...

# Single package or test
docker compose -f hermex-compose.yml exec dev go test -count=1 ./internal/objectstore
docker compose -f hermex-compose.yml exec dev go test -count=1 -run TestCreateMessage ./internal/objectstore

# Lint / vet
docker compose -f hermex-compose.yml exec dev gofmt -l .
docker compose -f hermex-compose.yml exec dev go vet ./...

# Rebuild a service after code change
docker compose -f hermex-compose.yml build webmail && docker compose -f hermex-compose.yml up -d --no-deps webmail
```

### Service ports

| Service       | Host | Container |
|---------------|------|-----------|
| SMTP (mta)    | 8140 | 25        |
| POP3          | 8141 | 110       |
| MariaDB       | 8142 | 3306      |
| IMAP          | 8143 | 143       |
| Webmail       | 8144 | 8080      |
| DAV           | 8145 | 8080      |
| ActiveSync    | 8146 | 8080      |
| EWS           | 8147 | 8080      |
| MAPI/HTTP     | 8148 | 8080      |
| Gateway (TLS) | 8149 | 8080      |

### Key facts

- **Database:** MariaDB (`email`) via `go-sql-driver/mysql`. Password hashing: `crypt_sha512`.
- **Mailbox store:** `internal/objectstore` — per-mailbox SQLite (`objects.sqlite3` + `imapindex.sqlite3` + `cid/` + `eml/`).
- **Auth:** `internal/directory` backed by MariaDB. Config JSON holds infra only (DB DSN, hostname, data_dir).
- **Mail construction:** `internal/oxcmail.Export()` is the single path from MAPI object to MIME bytes.

## Layout

| Path        | Purpose                                                                                        |
|-------------|------------------------------------------------------------------------------------------------|
| `cmd/`      | Service executables (mta, imap, pop3, webmail, dav, activesync, ews, mapihttp, gateway, admin) |
| `internal/` | Shared libraries — mapi, ext, objectstore, mime, oxcmail, protocol implementations             |
| `docker/`   | Dev and service container images                                                               |

## License

To be determined.

## Acknowledgements

hermEX is a Go rewrite of the [gromox](https://gromox.com/) project.
