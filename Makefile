# hermEX build & workflow entrypoint.
#
# All build, test, lint and container commands go through this Makefile — do not
# invoke `go` or `docker` directly. Build outputs land in bin/.
#
# Common use:
#   make build                                  # build every command into bin/
#   make gate                                   # fmt-check + vet + full test (dev container)
#   make test PKG=./internal/rop                # one package, dev container
#   make test PKG=./internal/rop RUN=TestCopyToSubObjects
#   make test-host PKG=./internal/rop RUN=TestX # host quick feedback (DB tests skip)
#   make up / make down                         # dev environment lifecycle

COMPOSE := docker compose -f hermex-compose.yml
BIN     := bin
CMDS    := mta imap pop3 webmail dav activesync ews mapihttp gateway admin fetchmail

# Test/lint scope. Override PKG (and optionally RUN) for a subset.
PKG ?= ./internal/... ./cmd/...
RUN ?=
RUNFLAG := $(if $(RUN),-run '$(RUN)',)

.PHONY: all build test test-host test-race vet fmt fmt-check gate tidy up down rebuild clean help compose-check

all: build

## build: compile every command binary into bin/
build:
	@mkdir -p $(BIN)
	@for c in $(CMDS); do \
		echo "  build $$c"; \
		go build -o $(BIN)/$$c ./cmd/$$c || exit 1; \
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

## fmt-check: list files needing gofmt (dev container); empty output means clean
fmt-check:
	$(COMPOSE) exec -T dev gofmt -l internal cmd

## gate: fmt-check + vet + full test — the pre-commit gate
gate: fmt-check vet test

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

## rebuild: rebuild and restart a single service, e.g. make rebuild SVC=webmail
rebuild:
	@test -n "$(SVC)" || { echo "set SVC=<service>"; exit 2; }
	$(COMPOSE) build $(SVC) && $(COMPOSE) up -d --no-deps $(SVC)

## compose-check: validate the compose file syntax
compose-check:
	$(COMPOSE) config -q

## clean: remove built binaries
clean:
	rm -rf $(BIN)

## help: list targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'
