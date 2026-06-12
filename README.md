# hermEX Mail Server

hermEX is a fully original, modular native Microsoft Exchange Server replacement written
entirely in Go. It implements the external client protocols an Exchange server must speak —
IMAP, POP3, SMTP/LMTP, CalDAV, CardDAV, ActiveSync, EWS, MAPI/HTTP with ROP, and NSPI for the
global address list — with no PHP and no C++ in the product. Server, webmail UI, admin UI, admin
API, and all sync interfaces are Go.

## Status

Early development. The build order favors externally observable capabilities first: a working
mail vertical (SMTP in, IMAP/POP3 out, minimal webmail) precedes the Exchange-native surfaces
(MAPI/ROP, NSPI, EWS), which come last. See `SLICING-PLAN.md`.

## Design

- External client protocols are reproduced to the wire; internal architecture, IPC, and the
  on-disk store schema are original.
- The logical MAPI model (property tags, named properties, object semantics, ICS state, and the
  property value encoding inside ROP/NSPI/EWS responses) is preserved because the external
  protocols dictate it.
- Correctness is verified against real clients (Thunderbird, Outlook, mobile) and a reference
  oracle running in a test-only Docker container.

See `ARCHITECTURE.md` for the module map and `SLICING-PLAN.md` for the roadmap.

## Development

Development is Docker-based. All runtime data lives under `docker-data/`.

```sh
# build/test (dev profile: Go toolchain + an isolated test database)
docker compose -f hermex-compose.yml --profile dev up -d
docker compose -f hermex-compose.yml exec dev go test ./...

# run the mail server (stack profile: SMTP on 8140, POP3 on 8141, MariaDB on 8142)
docker compose -f hermex-compose.yml --profile stack up -d
```

## Layout

| Path | Purpose |
|------|---------|
| `cmd/` | Service executables (mailstore, indexer, mta, imap, pop3, gateway, dav, webmail, admin) |
| `internal/` | Shared libraries (mapi, ext, store, mime, protocol implementations) |
| `test/` | Oracle diff harness and golden vectors |
| `docker/` | Dev and service container images |

## License

To be determined.
