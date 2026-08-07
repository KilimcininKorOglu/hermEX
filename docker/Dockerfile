# Multi-stage build for the hermEX mail daemons (static, pure-Go binaries).
FROM golang:1.26 AS build
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

# Minimal runtime image. NOTE: this image runs as root so the daemons can bind
# the privileged mail ports (25/110); a hardened production image should run as
# nonroot with CAP_NET_BIND_SERVICE or listen on high ports behind a proxy.
FROM gcr.io/distroless/static-debian12
COPY --from=build /out/ /usr/local/bin/
