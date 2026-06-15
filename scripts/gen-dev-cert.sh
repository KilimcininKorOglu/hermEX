#!/usr/bin/env bash
# Generate a self-signed development TLS certificate for the hermEX stack.
#
# The cert/key land in docker-data/tls/ (gitignored) and are consumed by the
# gateway (HERMEX_TLS_CERT/HERMEX_TLS_KEY) and, when configured, the mail
# daemons. It is valid for localhost / hermex.test / 127.0.0.1 so a local curl
# --cacert and an Outlook test against a hosts-file FQDN both verify. This is a
# DEV cert only; production uses a real CA-issued certificate.
set -euo pipefail

dir="${1:-docker-data/tls}"
mkdir -p "$dir"

openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 \
	-keyout "$dir/key.pem" -out "$dir/cert.pem" -days 365 -nodes \
	-subj "/CN=hermex.test" \
	-addext "subjectAltName=DNS:localhost,DNS:hermex.test,IP:127.0.0.1"

chmod 600 "$dir/key.pem"
echo "wrote $dir/cert.pem and $dir/key.pem (self-signed, 365d, SAN localhost/hermex.test/127.0.0.1)"
