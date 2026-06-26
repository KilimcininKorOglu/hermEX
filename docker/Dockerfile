# Multi-stage build for the hermEX mail daemons (static, pure-Go binaries).
FROM golang:1.26 AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-buildvcs=false
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -o /out/hermex-mta ./cmd/mta \
 && go build -trimpath -o /out/hermex-pop3 ./cmd/pop3 \
 && go build -trimpath -o /out/hermex-imap ./cmd/imap \
 && go build -trimpath -o /out/hermex-webmail2 ./cmd/webmail2 \
 && go build -trimpath -o /out/hermex-dav ./cmd/dav \
 && go build -trimpath -o /out/hermex-admin ./cmd/admin \
 && go build -trimpath -o /out/hermex-activesync ./cmd/activesync \
 && go build -trimpath -o /out/hermex-ews ./cmd/ews \
 && go build -trimpath -o /out/hermex-mapi ./cmd/mapihttp \
 && go build -trimpath -o /out/hermex-gateway ./cmd/gateway \
 && go build -trimpath -o /out/hermex-notify ./cmd/notify \
 && go build -trimpath -o /out/hermex-fetchmail ./cmd/fetchmail

# Minimal runtime image. NOTE: this image runs as root so the daemons can bind
# the privileged mail ports (25/110); a hardened production image should run as
# nonroot with CAP_NET_BIND_SERVICE or listen on high ports behind a proxy.
FROM gcr.io/distroless/static-debian12
COPY --from=build /out/ /usr/local/bin/
