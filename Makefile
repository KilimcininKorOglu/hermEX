# hermEX build & workflow entrypoint.
#
# All build, test, lint and container commands go through this Makefile, do not
# invoke `go` or `docker` directly. Build outputs land in bin/.
#
# Common use:
#   make build                                  # build every command into bin/
#   make gate                                   # fmt-check + vet + full test (dev container)
#   make test PKG=./internal/rop                # one package, dev container
#   make test PKG=./internal/rop RUN=TestCopyToSubObjects
#   make test-host PKG=./internal/rop RUN=TestX # host quick feedback (DB tests skip)
#   make lint PKG=./internal/rop/...            # golangci-lint on the host, not in gate
#   make audit-deps                             # dependency advisories, host; monthly and pre-release
#   make up / make down                         # dev environment lifecycle

COMPOSE := docker compose -f hermex-compose.yml
BIN     := bin
CMDS    := mta imap pop3 webmail2 dav activesync ews mapihttp gateway notify admin fetchmail antispam-bootstrap antispam-rules

# Source state stamped into every binary this Makefile builds, and into every
# container image it builds through compose. The images build from a context with
# .git excluded and with VCS stamping off, so without this a deployed binary
# carries no marker of where it came from. The -dirty suffix is not cosmetic: a
# bare sha on a binary built from a modified tree claims a source state that was
# never built.
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
GIT_DIRTY  := $(shell git diff --quiet 2>/dev/null || echo -dirty)
export HERMEX_COMMIT     := $(GIT_COMMIT)$(GIT_DIRTY)
export HERMEX_BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X hermex/internal/buildinfo.Commit=$(HERMEX_COMMIT) -X hermex/internal/buildinfo.BuildTime=$(HERMEX_BUILD_TIME)

# The identity the mail daemons run as. They must not run as root, and the id has
# to be this user's: the mail data is a bind mount owned on the host, so a fixed
# uid would make it unwritable. The compose file falls back to the image's nonroot
# user when these are unset (a bare `docker compose up`).
export HERMEX_UID := $(shell id -u)
export HERMEX_GID := $(shell id -g)

# Test/lint scope. Override PKG (and optionally RUN) for a subset.
PKG ?= ./internal/... ./cmd/...
RUN ?=
RUNFLAG := $(if $(RUN),-run '$(RUN)',)

.PHONY: all build test test-host test-race vet fmt fmt-check gate lint lint-modernize require-golangci-lint audit-deps require-govulncheck tidy up down images rebuild clean help compose-check dump-db dump-mail restore-db version

all: build

## build: compile every command binary into bin/
build:
	@mkdir -p $(BIN)
	@for c in $(CMDS); do \
		echo "  build $$c"; \
		go build -ldflags '$(LDFLAGS)' -o $(BIN)/$$c ./cmd/$$c || exit 1; \
	done
	@echo "built $(words $(CMDS)) binaries -> $(BIN)/"

## test: canonical gate in the dev container (override PKG / RUN for a subset)
test:
	$(COMPOSE) exec -T dev go test -count=1 $(RUNFLAG) $(PKG)

## test-host: host quick-feedback test run (DB-backed tests skip; same PKG/RUN)
test-host:
	go test -count=1 $(RUNFLAG) $(PKG)

## test-race: race-detector test run in the dev container (same PKG/RUN); needs cgo
test-race:
	$(COMPOSE) exec -T -e CGO_ENABLED=1 dev go test -race -count=1 $(RUNFLAG) $(PKG)

## vet: go vet in the dev container
vet:
	$(COMPOSE) exec -T dev go vet ./internal/... ./cmd/...

## fmt: gofmt -w over the source tree (dev container)
fmt:
	$(COMPOSE) exec -T dev gofmt -w internal cmd

## fmt-check: fail when any file needs gofmt (dev container). gofmt -l only lists
## and always exits 0, so the gate must check the output is empty itself.
fmt-check:
	@out="$$($(COMPOSE) exec -T dev gofmt -l internal cmd)"; \
	if [ -n "$$out" ]; then echo "gofmt needed on:"; echo "$$out"; exit 1; fi

## gate: fmt-check + vet + full test, the pre-commit gate
gate: fmt-check vet test

## lint: golangci-lint over PKG, on the HOST (deliberately not part of gate)
# This is a host target like test-host: the dev image carries the Go toolchain
# only, so there is no golangci-lint inside the container to exec into. Lint stays
# out of `gate` on purpose, since a lint sweep is its own reviewed commit rather
# than a delivery blocker, and .golangci.yml lifts the report caps so a run here
# shows every finding instead of the default 50-per-linter slice.
lint: require-golangci-lint
	golangci-lint run $(PKG)

## lint-modernize: only the modernize analyzer over PKG (same host rules as lint)
# modernize findings are HINT severity and invisible to `go vet`, so a gate-clean
# tree can still carry them; this target is the authoritative list.
lint-modernize: require-golangci-lint
	golangci-lint run --default=none --enable=modernize $(PKG)

# Report the missing tool and how to get it, rather than a bare "command not
# found" from the recipe. .golangci.yml declares version: "2", so v2 is required.
require-golangci-lint:
	@command -v golangci-lint >/dev/null || { \
		echo "golangci-lint is not on PATH (host tool; the dev image carries none)"; \
		echo "install: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest"; \
		exit 2; }

## audit-deps: check both dependency trees against published advisories (HOST)
# There is no CI and no bot watching this repository, so the window between a CVE
# being published and this project hearing about it is exactly as long as the gap
# between runs of this target. Run it before every release and at least monthly;
# it is deliberately not part of `gate`, because an advisory published overnight
# is not a reason to block an unrelated commit, and because it reaches the network
# while the gate must not.
#
# Go and npm are both covered: the SPA bundle an operator serves is built from the
# npm tree, so scanning only go.mod would leave the code that runs in a user's
# browser unwatched. govulncheck reports only advisories that reach a symbol this
# code actually calls, so a finding here is a real reachable path, not a version
# match.
audit-deps: require-govulncheck
	govulncheck ./internal/... ./cmd/...
	cd internal/webmail2 && npm audit

# Report the missing tool and how to get it, matching require-golangci-lint.
require-govulncheck:
	@command -v govulncheck >/dev/null || { \
		echo "govulncheck is not on PATH (host tool; the dev image carries none)"; \
		echo "install: go install golang.org/x/vuln/cmd/govulncheck@latest"; \
		exit 2; }

## tidy: sync go.mod/go.sum in the dev container (downloads any new dependency)
# The -e flag is required: the module cache bind-mounts under docker-data/ are
# visible inside the /src tree, so a plain `go mod tidy` (which walks the whole
# module) trips over their @version paths. -e proceeds past those and still
# resolves go.mod/go.sum correctly; the package build/test targets use explicit
# ./internal/... ./cmd/... patterns and are unaffected.
tidy:
	$(COMPOSE) exec -T dev go mod tidy -e

## up: start the dev environment (MariaDB + toolchain + mail services)
up:
	$(COMPOSE) up -d

## down: stop the dev environment
down:
	$(COMPOSE) down

## images: rebuild every service image with the source stamp
# A bare `docker compose build` leaves HERMEX_COMMIT unset, so the binaries in
# those images report "unknown" and a running container cannot say what it was
# built from. This target is the whole-stack counterpart to `rebuild`.
images:
	$(COMPOSE) build

## rebuild: rebuild and restart a single service, e.g. make rebuild SVC=webmail2
rebuild:
	@test -n "$(SVC)" || { echo "set SVC=<service>"; exit 2; }
	$(COMPOSE) build $(SVC) && $(COMPOSE) up -d --no-deps $(SVC)

## dump-db: write a compressed dump of the whole directory database to docker-data/backup/
# The directory database is the only copy of things the system generates and
# cannot re-derive: every domain's DKIM signing key, every uploaded TLS private
# key, and the accounts, aliases, admin roles and policy around them. It lives on
# one bind mount with no replication, so losing docker-data/db loses all of it.
# Take a dump before an upgrade, and on a schedule; keep it off this host.
#
# NOTHING in this repository schedules this or copies the result anywhere: the
# output lands beside the live database on the same disk, so it survives a
# clumsy delete and nothing else. An off-host, scheduled copy of
# docker-data/backup and docker-data/mailboxes is the operator's to build.
#
# The dump is written and compressed in separate steps rather than piped, so a
# failure of either is caught: a pipeline hides the dump's exit status and would
# leave a valid gzip file holding a truncated database. The result is verified
# and only then named, so a file under docker-data/backup is always a complete
# dump rather than something a restore discovers is unusable.
dump-db:
	@mkdir -p docker-data/backup
	@out=docker-data/backup/hermex-$$(date +%Y%m%d-%H%M%S).sql; \
	$(COMPOSE) exec -T db mariadb-dump -uroot -phermexstack \
		--single-transaction --routines --events --databases email > $$out.part || \
		{ rm -f $$out.part; echo "dump failed; nothing written"; exit 1; }; \
	gzip $$out.part || { rm -f $$out.part $$out.part.gz; exit 1; }; \
	gzip -t $$out.part.gz || { rm -f $$out.part.gz; echo "the dump did not verify; nothing written"; exit 1; }; \
	mv $$out.part.gz $$out.gz; \
	echo "wrote $$out.gz ($$(wc -c < $$out.gz) bytes, verified)"

## dump-mail: write a consistent copy of every mailbox's mail content to docker-data/backup/
# dump-db covers the accounts and the key material; this covers the mail. Each
# mailbox's two SQLite files are snapshotted inside a read transaction and its
# content files copied, so the dump is safe to take while the services are
# running. The regenerable wire-form cache is deliberately left out.
#
# The result mirrors the live layout, so restoring is copying a directory back
# into place with the mail services stopped, not running an importer. Keep it off
# this host, like the database dump.
dump-mail:
	@mkdir -p docker-data/backup
	@out=docker-data/backup/mail-$$(date +%Y%m%d-%H%M%S); \
	$(COMPOSE) exec -T dev go run ./cmd/admin -config docker-data/config.json backup-mail all $$out

## restore-db: load a dump back, e.g. make restore-db DUMP=docker-data/backup/hermex-....sql.gz
# This REPLACES the current directory: every account, key and policy in the dump
# wins. Stop the mail services first so nothing writes underneath it.
restore-db:
	@test -n "$(DUMP)" || { echo "set DUMP=<path to a .sql.gz from make dump-db>"; exit 2; }
	@test -f "$(DUMP)" || { echo "no such dump: $(DUMP)"; exit 2; }
	gzip -dc "$(DUMP)" | $(COMPOSE) exec -T db mariadb -uroot -phermexstack
	@echo "restored $(DUMP)"

## compose-check: validate the compose file syntax
compose-check:
	$(COMPOSE) config -q

## version: report the source state this Makefile would stamp into a build
version:
	@echo "commit     $(HERMEX_COMMIT)"
	@echo "build time $(HERMEX_BUILD_TIME)"
	@echo "run as     $(HERMEX_UID):$(HERMEX_GID)"

## clean: remove built binaries
clean:
	rm -rf $(BIN)

## help: list targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'
