# Multi-stage build for the hermEX mail daemons (static, pure-Go binaries).
# Base images are pinned by tag AND digest. The tag says which version a reader is
# looking at; the digest is what actually makes a rebuild reproducible, since a tag
# is mutable and both of these move. With no registry and no CI, rebuilding from
# source is the only way to reconstruct a previously running artifact, so two builds
# of one commit have to produce the same binaries and the same runtime CVE surface.
# The toolchain tag tracks the go directive in go.mod; update both together.
# Refresh a digest with: docker buildx imagetools inspect <image>:<tag>
FROM golang:1.26.5@sha256:2005724102f45917a63e9d092fc0e4ea56ea575048ce147caad5f5f61502c365 AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-buildvcs=false

# The build context excludes .git (see .dockerignore) and -buildvcs=false turns off
# the toolchain's automatic VCS stamping, so nothing would record what these
# binaries were built from. With no CI and no tags the running binary is the only
# evidence of what is deployed, so the source state is injected at link time
# instead. The Makefile supplies both values; a build driven any other way falls
# back to the package defaults and reports "unknown" rather than something false.
ARG HERMEX_COMMIT=""
ARG HERMEX_BUILD_TIME=""
ENV LDFLAGS="-X hermex/internal/buildinfo.Commit=$HERMEX_COMMIT -X hermex/internal/buildinfo.BuildTime=$HERMEX_BUILD_TIME"

COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags "$LDFLAGS" -o /out/hermex-mta ./cmd/mta \
 && go build -trimpath -ldflags "$LDFLAGS" -o /out/hermex-pop3 ./cmd/pop3 \
 && go build -trimpath -ldflags "$LDFLAGS" -o /out/hermex-imap ./cmd/imap \
 && go build -trimpath -ldflags "$LDFLAGS" -o /out/hermex-webmail2 ./cmd/webmail2 \
 && go build -trimpath -ldflags "$LDFLAGS" -o /out/hermex-dav ./cmd/dav \
 && go build -trimpath -ldflags "$LDFLAGS" -o /out/hermex-admin ./cmd/admin \
 && go build -trimpath -ldflags "$LDFLAGS" -o /out/hermex-activesync ./cmd/activesync \
 && go build -trimpath -ldflags "$LDFLAGS" -o /out/hermex-ews ./cmd/ews \
 && go build -trimpath -ldflags "$LDFLAGS" -o /out/hermex-mapi ./cmd/mapihttp \
 && go build -trimpath -ldflags "$LDFLAGS" -o /out/hermex-gateway ./cmd/gateway \
 && go build -trimpath -ldflags "$LDFLAGS" -o /out/hermex-notify ./cmd/notify \
 && go build -trimpath -ldflags "$LDFLAGS" -o /out/hermex-fetchmail ./cmd/fetchmail

# Minimal runtime image. The daemons run unprivileged: nothing here parses
# attacker-supplied network input as uid 0. The three that listen below port 1024
# (SMTP 25, POP3 110, IMAP 143) get the privilege they need from a namespaced
# sysctl in the compose file rather than from being root; a capability would not
# help, since capabilities are dropped on exec for a non-root user without file
# capabilities. 65532 is the distroless nonroot user; the compose file overrides
# it with the invoking user's id so the bind-mounted data stays writable.
FROM gcr.io/distroless/static-debian12:latest@sha256:a9fcaedd4c9b59e12dd65d954f0b5044f19b0647a8a3712e77205df9e7b102cd
COPY --from=build /out/ /usr/local/bin/
USER 65532:65532
