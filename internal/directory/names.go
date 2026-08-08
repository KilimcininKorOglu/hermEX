package directory

import (
	"fmt"
	"strings"
)

// ValidateAddress reports whether a mailbox address may be provisioned. Both of
// its parts become path components: provisioning derives the maildir from the
// address and then creates, and on deletion recursively removes, that directory.
// filepath.Join resolves a ".." segment instead of refusing it, so a local part
// like "../../tmp/x" would put the mailbox outside the data root and later delete
// from there. The check lives here because every provisioning path (admin API,
// admin UI, the CLI and LDAP sync) reaches disk through this package.
func ValidateAddress(address string) error {
	address = strings.ToLower(strings.TrimSpace(address))
	at := strings.LastIndexByte(address, '@')
	if at <= 0 || at == len(address)-1 {
		return fmt.Errorf("directory: %q is not an email address", address)
	}
	if err := validPathComponent(address[:at], "local part"); err != nil {
		return err
	}
	return ValidateDomain(address[at+1:])
}

// ValidateDomain reports whether a domain name may be provisioned. A domain
// names the directory holding its public store, so it carries the same path
// constraints as an address, narrowed to the characters a host name may hold.
func ValidateDomain(domain string) error {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if err := validPathComponent(domain, "domain"); err != nil {
		return err
	}
	for _, r := range domain {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.':
		default:
			return fmt.Errorf("directory: domain %q contains an invalid character", domain)
		}
	}
	if strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") || strings.Contains(domain, "..") {
		return fmt.Errorf("directory: domain %q has an empty label", domain)
	}
	return nil
}

// validPathComponent rejects the values that would make a name escape, or be
// unrepresentable in, the directory built from it.
func validPathComponent(s, what string) error {
	if s == "" {
		return fmt.Errorf("directory: the %s is empty", what)
	}
	if s == "." || s == ".." {
		return fmt.Errorf("directory: %q is not a usable %s", s, what)
	}
	if strings.ContainsAny(s, `/\`) {
		return fmt.Errorf("directory: the %s may not contain a path separator", what)
	}
	for _, r := range s {
		if r <= ' ' || r == 0x7f {
			return fmt.Errorf("directory: the %s contains a control character", what)
		}
	}
	return nil
}
