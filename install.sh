#!/usr/bin/env bash
# hermEX bare-metal installer and updater.
#
#   install.sh install --hostname mail.example.com [--version X.Y.Z | --from FILE]
#              [--domain example.com] [--acme-email you@example.com --agree-tos [--mtasts]]
#   install.sh update  [--version X.Y.Z | --from FILE]
#   install.sh rollback
#   install.sh status
#
# install  sets up MariaDB, MongoDB and ClamAV from their packages, creates the
#          hermex system user, the directory database, /etc/hermex/config.json
#          (only when absent), the systemd units and the firewall rules, then
#          installs and starts the release. It then asks for the first mail
#          domain (or takes --domain), creates its postmaster administrator,
#          DKIM key and antivirus setting, writes the DNS records to publish to
#          /root/hermex-dns-<domain>.txt, and offers Let's Encrypt certificates and an MTA-STS policy in testing mode.
# update   backs up the directory database, stops every daemon, switches to the
#          new release, applies its schema migrations and starts it again.
# rollback switches back to the release before the last update and restores the
#          database backup that update took, since the newer release may have
#          migrated the schema past what the older one accepts.
# status   prints the installed release and each daemon's health.
#
# Releases come from GitHub (linux/amd64 and linux/arm64), verified against the
# release's SHA256SUMS. --from installs a local archive instead; the SHA256SUMS
# file must sit next to it.
#
# Supported: RHEL, AlmaLinux and Rocky Linux 9; Debian 12; Ubuntu 22.04 and
# 24.04. These are the releases the package repositories below are verified for.
#
# No umask is set: the package managers and the tools this script runs create
# files other system users must read. Every file this script writes itself gets
# an explicit mode instead.

set -Eeuo pipefail

readonly REPO="KilimcininKorOglu/hermEX"
readonly PREFIX="/opt/hermex"
readonly ETC="/etc/hermex"
readonly STATE="/var/lib/hermex"
readonly CONFIG="${ETC}/config.json"
readonly SVC_USER="hermex"
readonly MARIADB_SERIES="11.8"
readonly MONGODB_SERIES="8.0"
readonly KEEP_RELEASES=3
readonly MONGO_CACHE_GB=1

# Daemons: name, binary, extra arguments, whether it binds a port below 1024,
# and its /healthz port on the loopback. The name is the one the daemon reports
# itself as (config health_addrs key and Live status name).
readonly DAEMONS=(
	"mta|hermex-mta||bind|8701"
	"imap|hermex-imap||bind|8702"
	"pop3|hermex-pop3||bind|8703"
	"webmail2|hermex-webmail2|||8704"
	"dav|hermex-dav|||8705"
	"activesync|hermex-activesync|||8706"
	"ews|hermex-ews|||8707"
	"mapi|hermex-mapi|||8708"
	"notify|hermex-notify|||8709"
	"gateway|hermex-gateway||bind|8710"
	"admin|hermex-admin|serve||8711"
	"fetchmail|hermex-fetchmail|||8712"
)

OS_FAMILY="" OS_ID="" OS_CODENAME="" ARCH="" WORK=""
ARG_HOSTNAME="" ARG_VERSION="" ARG_FROM="" ARG_DOMAIN="" ARG_ACME_EMAIL="" ARG_AGREE_TOS="" ARG_MTASTS=""

log() { printf '==> %s\n' "$*"; }
die() {
	printf 'install.sh: %s\n' "$*" >&2
	exit 1
}
on_error() { die "failed at line $1: $2"; }
trap 'on_error "$LINENO" "$BASH_COMMAND"' ERR
# cleanup must end with status 0: under errtrace a failing EXIT trap command
# fires the ERR trap and turns a clean run into a reported failure.
cleanup() {
	if [[ -n "$WORK" && -d "$WORK" ]]; then rm -rf "$WORK"; fi
}
trap cleanup EXIT

# write_file PATH MODE OWNER: writes stdin to PATH atomically (temporary file in
# the same directory, flushed, then renamed over the target), so a crash never
# leaves a half-written file.
write_file() {
	local path="$1" mode="$2" owner="$3" tmp
	tmp="$(mktemp "${path}.XXXXXX")"
	cat >"$tmp"
	chmod "$mode" "$tmp"
	chown "$owner" "$tmp"
	sync "$tmp"
	mv -f "$tmp" "$path"
}

require_root() { [[ $EUID -eq 0 ]] || die "run as root"; }

detect_os() {
	# shellcheck disable=SC1091
	. /etc/os-release
	OS_ID="$ID"
	OS_CODENAME="${VERSION_CODENAME:-}"
	case "$ID:${VERSION_ID%%.*}" in
	rhel:9 | almalinux:9 | rocky:9) OS_FAMILY="rhel" ;;
	debian:12) OS_FAMILY="debian" ;;
	ubuntu:22 | ubuntu:24) OS_FAMILY="debian" ;;
	*) die "unsupported distribution: $PRETTY_NAME (supported: RHEL/AlmaLinux/Rocky 9, Debian 12, Ubuntu 22.04/24.04)" ;;
	esac
	case "$(uname -m)" in
	x86_64) ARCH="amd64" ;;
	aarch64 | arm64) ARCH="arm64" ;;
	*) die "unsupported architecture: $(uname -m)" ;;
	esac
}

parse_args() {
	while (($#)); do
		case "$1" in
		--hostname) ARG_HOSTNAME="${2:?--hostname needs a value}" && shift ;;
		--version) ARG_VERSION="${2:?--version needs a value}" && shift ;;
		--from) ARG_FROM="${2:?--from needs a value}" && shift ;;
		--domain) ARG_DOMAIN="${2:?--domain needs a value}" && shift ;;
		--acme-email) ARG_ACME_EMAIL="${2:?--acme-email needs a value}" && shift ;;
		--agree-tos) ARG_AGREE_TOS="yes" ;;
		--mtasts) ARG_MTASTS="yes" ;;
		*) die "unknown option: $1" ;;
		esac
		shift
	done
	[[ -z "$ARG_VERSION" || "$ARG_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "--version takes X.Y.Z"
	[[ -z "$ARG_VERSION" || -z "$ARG_FROM" ]] || die "--version and --from exclude each other"
}

# ---------------------------------------------------------------- packages ---

install_rhel_packages() {
	if [[ "$OS_ID" == "rhel" ]]; then
		dnf -y install "https://dl.fedoraproject.org/pub/epel/epel-release-latest-9.noarch.rpm"
	else
		dnf -y install epel-release
	fi
	dnf -y module enable "mariadb:${MARIADB_SERIES}"
	write_file "/etc/yum.repos.d/mongodb-org-${MONGODB_SERIES}.repo" 0644 root:root <<EOF
[mongodb-org-${MONGODB_SERIES}]
name=MongoDB ${MONGODB_SERIES}
baseurl=https://repo.mongodb.org/yum/redhat/9/mongodb-org/${MONGODB_SERIES}/\$basearch/
gpgcheck=1
enabled=1
gpgkey=https://pgp.mongodb.com/server-${MONGODB_SERIES}.asc
EOF
	dnf -y install mariadb-server mongodb-org clamd clamav-freshclam openssl tar curl policycoreutils
}

# add_apt_key URL DEST: stores a repository signing key in the binary form apt
# reads from a signed-by path, whether the source serves it armored or not.
add_apt_key() {
	local url="$1" dest="$2" raw
	raw="$(mktemp)"
	curl -fsSL "$url" -o "$raw"
	if grep -q -- '-----BEGIN PGP' "$raw"; then
		gpg --batch --yes --dearmor -o "$dest" "$raw"
	else
		install -m 0644 "$raw" "$dest"
	fi
	chmod 0644 "$dest"
	rm -f "$raw"
}

install_debian_packages() {
	export DEBIAN_FRONTEND=noninteractive
	apt-get update
	apt-get install -y ca-certificates curl gnupg openssl tar
	install -d -m 0755 /etc/apt/keyrings
	add_apt_key "https://mariadb.org/mariadb_release_signing_key.pgp" /etc/apt/keyrings/mariadb-keyring.gpg
	write_file /etc/apt/sources.list.d/mariadb.list 0644 root:root <<EOF
deb [signed-by=/etc/apt/keyrings/mariadb-keyring.gpg] https://dlm.mariadb.com/repo/mariadb-server/${MARIADB_SERIES}/repo/${OS_ID} ${OS_CODENAME} main
EOF
	local packages=(mariadb-server clamav-daemon clamav-freshclam)
	if mongo_available; then
		add_mongo_apt_repo
		packages+=(mongodb-org)
	fi
	apt-get update
	apt-get install -y "${packages[@]}"
}

add_mongo_apt_repo() {
	local component="main"
	[[ "$OS_ID" == "ubuntu" ]] && component="multiverse"
	add_apt_key "https://pgp.mongodb.com/server-${MONGODB_SERIES}.asc" /etc/apt/keyrings/mongodb-${MONGODB_SERIES}.gpg
	write_file /etc/apt/sources.list.d/mongodb-org-${MONGODB_SERIES}.list 0644 root:root <<EOF
deb [signed-by=/etc/apt/keyrings/mongodb-${MONGODB_SERIES}.gpg] https://repo.mongodb.org/apt/${OS_ID} ${OS_CODENAME}/mongodb-org/${MONGODB_SERIES} ${component}
EOF
}

# mongo_available reports whether MongoDB publishes packages for this platform.
# It publishes none for Debian on arm64; there hermEX runs without the central
# log store and every daemon logs to the journal instead.
mongo_available() { ! [[ "$OS_ID" == "debian" && "$ARCH" == "arm64" ]]; }

mongo_uri() { if mongo_available; then echo "mongodb://127.0.0.1:27017"; fi; }

clamd_socket() {
	if [[ "$OS_FAMILY" == "rhel" ]]; then
		echo "/run/clamd.scan/clamd.sock"
	else
		echo "/var/run/clamav/clamd.ctl"
	fi
}

clamd_unit() {
	if [[ "$OS_FAMILY" == "rhel" ]]; then echo "clamd@hermex.service"; else echo "clamav-daemon.service"; fi
}

# setup_clamav fetches the signature database before clamd starts, since clamd
# refuses to start without one and an unreachable clamd temp-fails inbound mail.
setup_clamav() {
	if [[ "$OS_FAMILY" == "rhel" ]]; then
		write_file /etc/clamd.d/hermex.conf 0644 root:root <<EOF
# Written by hermEX install.sh: the clamd instance hermEX scans mail with.
LogSyslog yes
User clamscan
LocalSocket $(clamd_socket)
LocalSocketGroup virusgroup
LocalSocketMode 660
DatabaseDirectory /var/lib/clamav
EOF
		usermod -aG virusgroup "$SVC_USER"
		systemd-tmpfiles --create clamd.scan.conf
	fi
	log "fetching the ClamAV signature database (the first run downloads all of it)"
	systemctl stop clamav-freshclam.service || true
	# clamd reads the database as another user. An earlier version of this script
	# ran freshclam under umask 027 and left the files unreadable to it.
	freshclam --quiet
	find /var/lib/clamav -maxdepth 1 -type f -name '*.c[lv]d' -exec chmod 0644 {} +
	systemctl enable --now clamav-freshclam.service
	systemctl enable --now "$(clamd_unit)"
}

setup_services() {
	systemctl enable --now mariadb.service
	if mongo_available; then
		limit_mongo_cache || true
		systemctl enable --now mongod.service
	else
		log "MongoDB publishes no packages for ${OS_ID} ${ARCH}: no central log store, the daemons log to the journal"
	fi
}

# limit_mongo_cache caps the WiredTiger cache at MONGO_CACHE_GB. MongoDB holds
# only the logs here, yet by default takes half of the memory less 1 GB, beside
# clamd and every daemon. It edits mongod.conf only when no cache size is set, so
# an operator's own value stays, and returns 0 only when it changed the file.
limit_mongo_cache() {
	local conf=/etc/mongod.conf
	[[ -f "$conf" ]] || die "$conf is missing"
	grep -q 'cacheSizeGB' "$conf" && return 1
	grep -q '^storage:' "$conf" || die "$conf has no storage section to add the cache size to"
	awk -v gb="$MONGO_CACHE_GB" '
		{ print }
		/^storage:/ { print "  wiredTiger:"; print "    engineConfig:"; print "      cacheSizeGB: " gb }
	' "$conf" | write_file "$conf" 0644 root:root
	log "capped the MongoDB cache at ${MONGO_CACHE_GB} GB in $conf"
}

# ------------------------------------------------------------------ layout ---

create_user() {
	getent group "$SVC_USER" >/dev/null || groupadd --system "$SVC_USER"
	getent passwd "$SVC_USER" >/dev/null ||
		useradd --system --gid "$SVC_USER" --home-dir "$STATE" --no-create-home --shell /usr/sbin/nologin "$SVC_USER"
}

create_dirs() {
	install -d -m 0755 -o root -g root "$PREFIX" "$PREFIX/releases"
	install -d -m 0750 -o root -g "$SVC_USER" "$ETC" "$ETC/tls"
	install -d -m 0750 -o "$SVC_USER" -g "$SVC_USER" "$STATE" "$STATE/mail" "$STATE/acme" "$STATE/spill"
	install -d -m 0700 -o root -g root "$STATE/backup"
}

random_secret() { openssl rand -hex 32; }

# create_database makes the directory database and its account. It runs only
# with a fresh configuration, which is where the generated password is kept.
create_database() {
	local password="$1"
	mariadb --protocol=socket -u root <<EOF
CREATE DATABASE IF NOT EXISTS email CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER IF NOT EXISTS 'hermex'@'localhost' IDENTIFIED BY '${password}';
ALTER USER 'hermex'@'localhost' IDENTIFIED BY '${password}';
GRANT ALL PRIVILEGES ON email.* TO 'hermex'@'localhost';
FLUSH PRIVILEGES;
EOF
}

# create_internal_cert issues the self-signed certificate every listener starts
# with, so IMAPS, POP3S, SMTPS, the gateway and the loopback hops between the
# gateway and its backends all speak TLS from the first start. A certificate the
# admin panel stores or obtains through ACME replaces it per server name without
# a restart.
create_internal_cert() {
	local host="$1"
	[[ -s "$ETC/tls/cert.pem" ]] && return 0
	openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 825 \
		-subj "/CN=${host}" -addext "subjectAltName=DNS:${host},IP:127.0.0.1" \
		-keyout "$ETC/tls/key.pem.new" -out "$ETC/tls/cert.pem.new" 2>/dev/null
	chown root:"$SVC_USER" "$ETC/tls/key.pem.new" "$ETC/tls/cert.pem.new"
	chmod 0640 "$ETC/tls/key.pem.new" "$ETC/tls/cert.pem.new"
	mv -f "$ETC/tls/key.pem.new" "$ETC/tls/key.pem"
	mv -f "$ETC/tls/cert.pem.new" "$ETC/tls/cert.pem"
}

health_addrs_json() {
	local entry name port sep=""
	for entry in "${DAEMONS[@]}"; do
		IFS='|' read -r name _ _ _ port <<<"$entry"
		printf '%s\n    "%s": "127.0.0.1:%s"' "$sep" "$name" "$port"
		sep=","
	done
}

# write_config creates config.json once. An existing file is never rewritten:
# it holds key_secret, and losing it leaves every stored DKIM and TLS private key
# undecryptable.
write_config() {
	local host="$1" db_password
	if [[ -e "$CONFIG" ]]; then
		log "keeping the existing $CONFIG"
		return 0
	fi
	db_password="$(random_secret)"
	create_database "$db_password"
	write_file "$CONFIG" 0640 root:"$SVC_USER" <<EOF
{
  "database_dsn": "hermex:${db_password}@tcp(127.0.0.1:3306)/email?charset=utf8mb4",
  "data_dir": "${STATE}/mail",
  "hostname": "${host}",
  "tls_cert": "${ETC}/tls/cert.pem",
  "tls_key": "${ETC}/tls/key.pem",
  "smtp_addr": ":25",
  "smtps_addr": ":465",
  "submission_addr": ":587",
  "pop3_addr": ":110",
  "pop3s_addr": ":995",
  "imap_addr": ":143",
  "imaps_addr": ":993",
  "gateway_addr": ":443",
  "webmail2_addr": "127.0.0.1:8601",
  "dav_addr": "127.0.0.1:8602",
  "activesync_addr": "127.0.0.1:8603",
  "ews_addr": "127.0.0.1:8604",
  "mapi_addr": "127.0.0.1:8605",
  "mta_http_addr": "127.0.0.1:8606",
  "notify_addr": "127.0.0.1:8607",
  "admin_addr": "127.0.0.1:8608",
  "gateway_backend_webmail": "https://127.0.0.1:8601",
  "gateway_backend_dav": "https://127.0.0.1:8602",
  "gateway_backend_activesync": "https://127.0.0.1:8603",
  "gateway_backend_ews": "https://127.0.0.1:8604",
  "gateway_backend_mapi": "https://127.0.0.1:8605",
  "gateway_backend_mta": "https://127.0.0.1:8606",
  "notify_url": "https://127.0.0.1:8607",
  "webmail2_dist": "${PREFIX}/current/webmail2",
  "acme_storage": "${STATE}/acme",
  "mongo_uri": "$(mongo_uri)",
  "log_database": "hermex",
  "log_spill_dir": "${STATE}/spill",
  "clamd_addr": "unix://$(clamd_socket)",
  "health_addrs": {$(health_addrs_json)
  },
  "webmail2_secret": "$(random_secret)",
  "admin_secret": "$(random_secret)",
  "key_secret": "$(random_secret)",
  "digest_secret": "$(random_secret)",
  "notify_secret": "$(random_secret)"
}
EOF
	log "wrote $CONFIG; back it up separately: its key_secret decrypts the stored DKIM and TLS keys"
}

# ----------------------------------------------------------------- release ---

download() {
	local url="$1" dest="$2"
	curl -fsSL --retry 3 --proto '=https' --tlsv1.2 -o "$dest" "$url" || die "download failed: $url"
}

# fetch_release leaves the verified archive at $WORK/hermex.tar.gz.
fetch_release() {
	local asset="hermex-linux-${ARCH}.tar.gz" base
	WORK="$(mktemp -d "${PREFIX}/.fetch.XXXXXX")"
	if [[ -n "$ARG_FROM" ]]; then
		[[ -f "$ARG_FROM" ]] || die "no such archive: $ARG_FROM"
		cp "$ARG_FROM" "$WORK/$asset"
		cp "$(dirname "$ARG_FROM")/SHA256SUMS" "$WORK/SHA256SUMS" || die "SHA256SUMS must sit next to $ARG_FROM"
	else
		base="https://github.com/${REPO}/releases/latest/download"
		[[ -n "$ARG_VERSION" ]] && base="https://github.com/${REPO}/releases/download/v${ARG_VERSION}"
		download "$base/$asset" "$WORK/$asset"
		download "$base/SHA256SUMS" "$WORK/SHA256SUMS"
	fi
	(cd "$WORK" && grep -E "  ${asset}\$" SHA256SUMS | sha256sum -c --status -) ||
		die "checksum mismatch for $asset; refusing to install it"
	mv "$WORK/$asset" "$WORK/hermex.tar.gz"
}

# unpack_release extracts the verified archive into the work directory (under
# $PREFIX, never /tmp, so SELinux labels it like the rest of the tree) and prints
# its build id: the release number, or number-dev+commit for an untagged build.
# Nothing under releases/ is touched yet.
unpack_release() {
	local build
	tar -xzf "$WORK/hermex.tar.gz" -C "$WORK"
	build="$(tr -d '[:space:]' <"$WORK/hermex/BUILD")"
	[[ "$build" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-dev\+[0-9a-f]+(-dirty)?)?$ ]] || die "the archive carries no valid BUILD id"
	echo "$build"
}

# place_release moves the unpacked build to releases/<build>. The directory the
# running daemons execute from is never replaced: that build is installed
# already, and the callers stop before reaching here.
place_release() {
	local build="$1" dest="$PREFIX/releases/$1"
	[[ "$build" != "$(current_version)" ]] || die "refusing to replace the running release $build"
	rm -rf "$dest"
	mv "$WORK/hermex" "$dest"
	chown -R root:root "$dest"
	chmod -R u=rwX,go=rX "$dest"
	if command -v restorecon >/dev/null; then restorecon -R "$dest"; fi
}

current_version() { [[ -L "$PREFIX/current" ]] && basename "$(readlink "$PREFIX/current")" || true; }

# activate points $PREFIX/current at a release with an atomic rename, so a
# daemon never sees a half-switched tree.
activate() {
	local version="$1"
	ln -sfn "$PREFIX/releases/$version" "$PREFIX/.current.new"
	mv -Tf "$PREFIX/.current.new" "$PREFIX/current"
}

# prune_releases keeps the newest KEEP_RELEASES releases by install time (a
# build id does not sort by age), and always the running one and the rollback
# target recorded on the first line of previous-release.
prune_releases() {
	local keep="" current
	current="$(current_version)"
	if [[ -s "$STATE/previous-release" ]]; then read -r keep <"$STATE/previous-release"; fi
	find "$PREFIX/releases" -mindepth 1 -maxdepth 1 -type d -printf '%T@ %f\n' | sort -rn |
		tail -n "+$((KEEP_RELEASES + 1))" | while read -r _ v; do
		[[ "$v" == "$current" || "$v" == "$keep" ]] || rm -rf "${PREFIX:?}/releases/$v"
	done
}

# ----------------------------------------------------------------- systemd ---

write_unit() {
	local name="$1" binary="$2" args="$3" bind="$4" caps=""
	[[ "$bind" == "bind" ]] && caps=$'AmbientCapabilities=CAP_NET_BIND_SERVICE\nCapabilityBoundingSet=CAP_NET_BIND_SERVICE'
	[[ "$bind" == "bind" ]] || caps="CapabilityBoundingSet="
	write_file "/etc/systemd/system/hermex-${name}.service" 0644 root:root <<EOF
[Unit]
Description=hermEX ${name}
PartOf=hermex.target
After=network-online.target mariadb.service mongod.service $(clamd_unit)
Wants=network-online.target mariadb.service mongod.service $(clamd_unit)

[Service]
User=${SVC_USER}
Group=${SVC_USER}
ExecStart=${PREFIX}/current/bin/${binary} -config ${CONFIG} ${args}
Restart=on-failure
RestartSec=5
LimitNOFILE=65536
${caps}
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes
ReadWritePaths=${STATE}

[Install]
WantedBy=hermex.target
EOF
}

write_units() {
	local entry name binary args bind
	for entry in "${DAEMONS[@]}"; do
		IFS='|' read -r name binary args bind _ <<<"$entry"
		write_unit "$name" "$binary" "$args" "$bind"
	done
	write_file /etc/systemd/system/hermex.target 0644 root:root <<EOF
[Unit]
Description=hermEX mail server
After=network-online.target

[Install]
WantedBy=multi-user.target
EOF
	systemctl daemon-reload
	systemctl enable hermex.target
	for entry in "${DAEMONS[@]}"; do
		IFS='|' read -r name _ <<<"$entry"
		systemctl enable "hermex-${name}.service"
	done
}

stop_all() { systemctl stop hermex.target; }
start_all() { systemctl start hermex.target; }

# wait_healthy polls every daemon's /healthz for up to 60 seconds and fails with
# the names of those still down.
wait_healthy() {
	local entry name port down deadline=$((SECONDS + 60))
	while :; do
		down=""
		for entry in "${DAEMONS[@]}"; do
			IFS='|' read -r name _ _ _ port <<<"$entry"
			curl -fsS --max-time 3 "http://127.0.0.1:${port}/healthz" >/dev/null 2>&1 || down+=" $name"
		done
		[[ -z "$down" ]] && return 0
		((SECONDS < deadline)) || die "not healthy after 60s:${down}; see journalctl -u 'hermex-*'"
		sleep 2
	done
}

admin_cli() { runuser -u "$SVC_USER" -- "$PREFIX/current/bin/hermex-admin" -config "$CONFIG" "$@"; }

register_health_targets() {
	local entry name port
	for entry in "${DAEMONS[@]}"; do
		IFS='|' read -r name _ _ _ port <<<"$entry"
		admin_cli add-health-target "$name" "http://127.0.0.1:${port}/healthz" >/dev/null
	done
}

# ---------------------------------------------------------------- firewall ---

open_firewall() {
	# firewalld names the 587/tcp service smtp-submission.
	local services=(smtp smtps smtp-submission imap imaps pop3 pop3s https)
	local ports=(25 465 587 143 993 110 995 443)
	if systemctl is-active --quiet firewalld; then
		local s
		for s in "${services[@]}"; do firewall-cmd -q --permanent --add-service="$s"; done
		firewall-cmd -q --reload
		log "firewalld: opened ${services[*]}"
	elif command -v ufw >/dev/null && ufw status | grep -q '^Status: active'; then
		local p
		for p in "${ports[@]}"; do ufw allow "${p}/tcp" >/dev/null; done
		log "ufw: opened ${ports[*]}/tcp"
	else
		log "no active firewalld or ufw; open TCP ${ports[*]} on your firewall"
	fi
}

# ---------------------------------------------------------------- commands ---

backup_database() {
	local dest
	dest="$STATE/backup/email-$(date -u +%Y%m%dT%H%M%SZ).sql.gz"
	install -m 0600 /dev/null "$dest.tmp"
	mariadb-dump --single-transaction --routines --triggers email | gzip >"$dest.tmp"
	sync "$dest.tmp"
	mv -f "$dest.tmp" "$dest"
	echo "$dest"
}

cmd_install() {
	local host version
	host="${ARG_HOSTNAME:-$(hostname -f)}"
	[[ "$host" == *.* ]] || die "--hostname must be the server's fully qualified name"
	[[ -L "$PREFIX/current" ]] && die "hermEX is already installed; use update"
	log "installing hermEX on $host (${OS_ID} ${OS_CODENAME:-} ${ARCH})"
	if [[ "$OS_FAMILY" == "rhel" ]]; then install_rhel_packages; else install_debian_packages; fi
	create_user
	create_dirs
	setup_services
	setup_clamav
	create_internal_cert "$host"
	write_config "$host"
	fetch_release
	version="$(unpack_release)"
	place_release "$version"
	activate "$version"
	write_units
	admin_cli ensure-schema >/dev/null
	start_all
	wait_healthy
	register_health_targets
	open_firewall
	log "hermEX $version is running"
	setup_mail "$host"
	print_next_steps
}

# ask PROMPT DEFAULT: prints the answer read from the terminal, or DEFAULT when
# there is no terminal, so an unattended install takes the flags and defaults.
ask() {
	local answer=""
	if [[ -r /dev/tty ]] && { : </dev/tty; } 2>/dev/null; then
		read -r -p "$1${2:+ [$2]}: " answer </dev/tty || true
	fi
	echo "${answer:-$2}"
}

# setup_mail HOST: creates the first domain with its postmaster account (a
# system administrator), turns its antivirus scanning on, gives it a DKIM key
# and writes the DNS records it needs to /root, then offers ACME certificates.
setup_mail() {
	local host="$1" domain admin password records
	domain="${ARG_DOMAIN:-$(ask "First mail domain (empty to skip)" "${host#*.}")}"
	[[ -n "$domain" ]] || return 0
	admin="postmaster@${domain}"
	password="$(openssl rand -base64 18)"
	admin_cli create-domain "$domain" >/dev/null
	admin_cli create-user "$admin" "$password" >/dev/null
	admin_cli grant-admin "$admin" system >/dev/null
	admin_cli set-av-scan "$domain" on >/dev/null
	admin_cli generate-dkim "$domain" >/dev/null
	log "created ${domain} and ${admin} (system administrator)"
	printf '\n%s password (shown once, stored nowhere): %s\n\n' "$admin" "$password"
	if setup_acme "$host" "$admin"; then
		setup_mtasts "$host" "$domain"
		# The gateway picks its certificate source and names at startup.
		systemctl restart hermex-gateway.service
		log "the gateway obtains its Let's Encrypt certificates now"
	fi
	write_records "$domain"
}

# write_records DOMAIN: saves the DNS records the domain needs to /root and
# prints them. It runs after the ACME and MTA-STS choices, since publishing
# MTA-STS adds its records to the list.
write_records() {
	local domain="$1" records="/root/hermex-dns-${1}.txt"
	{
		echo "# DNS records for ${domain}: type, name, value (tab separated)."
		echo "# After the DKIM record is published, start signing with:"
		echo "#   $0 admin enable-dkim ${domain}"
		admin_cli dns-records "$domain"
	} | write_file "$records" 0600 root:root
	log "DNS records to publish, also saved to ${records}:"
	column -t -s $'\t' "$records" 2>/dev/null || cat "$records"
}

# resolves_here NAME: whether NAME's IPv4 address is one of this server's.
resolves_here() {
	local addr
	addr="$(getent ahostsv4 "$1" | awk 'NR==1{print $1}')"
	[[ -n "$addr" ]] && ip -4 -o addr show | grep -qF " ${addr}/"
}

# setup_acme HOST CONTACT: switches the front door to Let's Encrypt when the
# operator accepts the CA's terms and HOST already resolves to this server, since
# the CA validates by connecting to it. It returns 1 when ACME stays off; the
# caller restarts the gateway.
setup_acme() {
	local host="$1" email agree
	email="${ARG_ACME_EMAIL:-$(ask "Let's Encrypt contact email (empty to keep the self-signed certificate)" "$2")}"
	[[ -n "$email" ]] || return 1
	agree="${ARG_AGREE_TOS:-$(ask "Accept the Let's Encrypt terms (https://letsencrypt.org/repository/)? yes/no" "no")}"
	if [[ "$agree" != "yes" ]]; then
		log "ACME not enabled; the self-signed certificate stays"
		return 1
	fi
	if ! resolves_here "$host"; then
		log "ACME not enabled: ${host} does not resolve to this server; publish its A record, then run: $0 admin set-tls-acme ${email} --agree-tos && systemctl restart hermex-gateway"
		return 1
	fi
	admin_cli set-tls-acme "$email" --agree-tos >/dev/null
}

# setup_mtasts HOST DOMAIN: publishes the MTA-STS policy in testing mode (RFC
# 8461: senders report TLS failures but still deliver) when the operator agrees.
# The policy is served from mta-sts.DOMAIN over HTTPS, so that name must already
# point at this server for the gateway to get its certificate; otherwise the
# steps to take later are printed.
setup_mtasts() {
	local host="$1" domain="$2" answer
	answer="${ARG_MTASTS:-$(ask "Publish MTA-STS in testing mode for ${domain}? yes/no" "no")}"
	[[ "$answer" == "yes" ]] || return 0
	if ! resolves_here "mta-sts.${domain}"; then
		log "MTA-STS not enabled: publish mta-sts.${domain} CNAME ${host}, then run: $0 admin set-mtasts testing && systemctl restart hermex-gateway && $0 admin dns-records ${domain}"
		return 0
	fi
	admin_cli set-mtasts testing >/dev/null
	log "MTA-STS published in testing mode"
}

print_next_steps() {
	cat <<EOF

Next steps:
  1. Publish the DNS records listed above (saved under /root/hermex-dns-*.txt),
     then enable DKIM signing: $0 admin enable-dkim <domain>
  2. The admin panel is reached through an SSH tunnel (ssh -L 8608:127.0.0.1:8608
     root@<server>, then https://127.0.0.1:8608/admin/ui/ in a browser; the
     certificate is the self-signed internal one).
  3. Back up $CONFIG off this server: without it the stored keys are lost.
EOF
}

cmd_update() {
	local old new dump
	old="$(current_version)"
	[[ -n "$old" ]] || die "hermEX is not installed; use install"
	fetch_release
	new="$(unpack_release)"
	if [[ "$new" == "$old" ]]; then
		log "release $new is already installed"
		return 0
	fi
	place_release "$new"
	write_units
	stop_all
	# An install made before the cache cap existed gets it on its next update.
	if mongo_available && limit_mongo_cache; then systemctl restart mongod.service; fi
	# The dump is taken with every daemon stopped, so it is the exact state the
	# old release left and a rollback returns to it.
	dump="$(backup_database)"
	log "database backed up to $dump"
	printf '%s\n%s\n' "$old" "$dump" | write_file "$STATE/previous-release" 0600 root:root
	activate "$new"
	admin_cli ensure-schema >/dev/null
	start_all
	wait_healthy
	register_health_targets
	prune_releases
	log "updated hermEX $old -> $new"
}

# check_rollback_schema refuses a rollback across a migration of the relay spool
# or the mailbox stores. Those live in files on this server, are migrated in
# place by the newer release and are not in the database dump, so the older
# release would refuse every migrated store as newer than itself.
check_rollback_schema() {
	local previous="$PREFIX/releases/$1/SCHEMA" current="$PREFIX/releases/$2/SCHEMA" db
	[[ -s "$previous" && -s "$current" ]] || die "a release lacks its SCHEMA list; cannot tell whether a rollback is safe"
	for db in relay objects index; do
		[[ "$(grep "^$db " "$previous")" == "$(grep "^$db " "$current")" ]] ||
			die "$2 migrated the $db schema past $1; the mailboxes cannot be rolled back with the database dump"
	done
}

cmd_rollback() {
	local previous dump current
	[[ -s "$STATE/previous-release" ]] || die "no update to roll back"
	{
		read -r previous
		read -r dump
	} <"$STATE/previous-release"
	current="$(current_version)"
	[[ -d "$PREFIX/releases/$previous" ]] || die "release $previous is no longer on disk"
	[[ -s "$dump" ]] || die "the database backup $dump is missing"
	check_rollback_schema "$previous" "$current"
	log "rolling back $current -> $previous and restoring $dump"
	stop_all
	# The dump holds only what the old schema had; recreating the database first
	# also removes the tables and rows the newer release's migrations added.
	mariadb -e 'DROP DATABASE email; CREATE DATABASE email CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;'
	gunzip -c "$dump" | mariadb email
	activate "$previous"
	start_all
	wait_healthy
	rm -f "$STATE/previous-release"
	log "rolled back to $previous; mail received since the update is still in the mailboxes"
}

cmd_status() {
	local entry name port state
	echo "release: $(current_version)"
	for entry in "${DAEMONS[@]}"; do
		IFS='|' read -r name _ _ _ port <<<"$entry"
		state="$(systemctl is-active "hermex-${name}.service" || true)"
		if curl -fsS --max-time 3 "http://127.0.0.1:${port}/healthz" >/dev/null 2>&1; then
			printf '  %-11s %-8s healthy\n' "$name" "$state"
		else
			printf '  %-11s %-8s NOT HEALTHY\n' "$name" "$state"
		fi
	done
}

main() {
	local command="${1:-}"
	[[ $# -gt 0 ]] && shift
	case "$command" in
	install | update | rollback | status) ;;
	admin)
		require_root
		# exec hands the exit status and output to hermex-admin itself; a usage
		# error there is not a failure of this script and must not be reported
		# as one by the ERR trap.
		trap - ERR EXIT
		exec runuser -u "$SVC_USER" -- "$PREFIX/current/bin/hermex-admin" -config "$CONFIG" "$@"
		;;
	*) die "usage: $0 install|update|rollback|status|admin [options]" ;;
	esac
	require_root
	parse_args "$@"
	detect_os
	"cmd_${command}"
}

main "$@"
