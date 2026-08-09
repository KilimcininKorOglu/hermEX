module hermex

go 1.26.5

require (
	blitiri.com.ar/go/spf v1.5.1
	github.com/SherClockHolmes/webpush-go v1.4.0
	github.com/caddyserver/certmagic v0.25.4
	github.com/emersion/go-msgauth v0.7.0
	github.com/go-ldap/ldap/v3 v3.4.13
	github.com/go-sql-driver/mysql v1.10.0
	github.com/klauspost/compress v1.18.7
	github.com/miekg/dns v1.1.72
	// Deliberately a fork. The nominal "pkcs7" project (fullsailor/pkcs7) is
	// unmaintained and no longer builds against a current Go toolchain, so every
	// Go consumer of CMS is on one fork or another; this one is kept current by
	// the step-ca maintainers, who use it in their own certificate authority.
	// Moving to the obvious-looking canonical name would be a regression, not a
	// cleanup. It parses attacker-supplied ASN.1 in internal/smime, so a bump is
	// a security-relevant change: read the diff rather than taking the tag.
	github.com/smallstep/pkcs7 v0.2.1
	go.mongodb.org/mongo-driver/v2 v2.7.0
	golang.org/x/net v0.56.0
	golang.org/x/sync v0.21.0
	golang.org/x/text v0.39.0
	modernc.org/sqlite v1.52.0
	software.sslmate.com/src/go-pkcs12 v0.7.2
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/Azure/go-ntlmssp v0.1.1 // indirect
	github.com/caddyserver/zerossl v0.1.5 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/go-asn1-ber/asn1-ber v1.5.8-0.20250403174932-29230038a667 // indirect
	github.com/golang-jwt/jwt/v5 v5.2.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/klauspost/cpuid/v2 v2.3.0 // indirect
	github.com/libdns/libdns v1.1.1 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mholt/acmez/v3 v3.1.6 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/xdg-go/pbkdf2 v1.0.0 // indirect
	github.com/xdg-go/scram v1.2.0 // indirect
	github.com/xdg-go/stringprep v1.0.4 // indirect
	github.com/youmark/pkcs8 v0.0.0-20240726163527-a2c0da244d78 // indirect
	github.com/zeebo/blake3 v0.2.4 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.uber.org/zap v1.27.1 // indirect
	go.uber.org/zap/exp v0.3.0 // indirect
	golang.org/x/crypto v0.53.0 // indirect
	golang.org/x/mod v0.37.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/tools v0.47.0 // indirect
	modernc.org/libc v1.72.3 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)
