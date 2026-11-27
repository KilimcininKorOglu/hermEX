// Package easpolicy models an ActiveSync device security policy (the EASProvisionDoc
// of MS-ASPROV) and the two-layer resolution the server applies: a global default
// overridden per mailbox. A policy is a partial set of fields, an unset field is not
// enforced and inherits the lower layer (the global default, then the device's own
// default), so a mailbox override carries only what it changes.
package easpolicy

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
)

// Kind distinguishes a boolean toggle from a numeric limit, so an editor renders the
// right control and a value can be range-checked.
type Kind int

const (
	Bool Kind = iota // 0 or 1 (a few fields also accept 2 for "allow but audit")
	Num              // a non-negative numeric limit
)

// Field describes one policy field. Name is the MS-ASPROV element name, which is also
// the WBXML token name the wire layer maps it to.
type Field struct {
	Name string
	Kind Kind
}

// Fields is the canonical EASProvisionDoc policy field set in wire (token) order. The
// application allow/block lists (ApprovedApplicationList, UnapprovedInROMApplication-
// List) are intentionally excluded: they are multi-valued application-hash structures,
// a separate v1 limitation from the scalar device policy modeled here.
var Fields = []Field{
	{"DevicePasswordEnabled", Bool},
	{"AlphanumericDevicePasswordRequired", Bool},
	{"RequireStorageCardEncryption", Bool},
	{"PasswordRecoveryEnabled", Bool},
	{"AttachmentsEnabled", Bool},
	{"MinDevicePasswordLength", Num},
	{"MaxInactivityTimeDeviceLock", Num},
	{"MaxDevicePasswordFailedAttempts", Num},
	{"MaxAttachmentSize", Num},
	{"AllowSimpleDevicePassword", Bool},
	{"DevicePasswordExpiration", Num},
	{"DevicePasswordHistory", Num},
	{"AllowStorageCard", Bool},
	{"AllowCamera", Bool},
	{"RequireDeviceEncryption", Bool},
	{"AllowUnsignedApplications", Bool},
	{"AllowUnsignedInstallationPackages", Bool},
	{"MinDevicePasswordComplexCharacters", Num},
	{"AllowWiFi", Bool},
	{"AllowTextMessaging", Bool},
	{"AllowPOPIMAPEmail", Bool},
	{"AllowBluetooth", Num},
	{"AllowIrDA", Bool},
	{"RequireManualSyncWhenRoaming", Bool},
	{"AllowDesktopSync", Bool},
	{"MaxCalendarAgeFilter", Num},
	{"AllowHTMLEmail", Bool},
	{"MaxEmailAgeFilter", Num},
	{"MaxEmailBodyTruncationSize", Num},
	{"MaxEmailHTMLBodyTruncationSize", Num},
	{"RequireSignedSMIMEMessages", Bool},
	{"RequireEncryptedSMIMEMessages", Bool},
	{"RequireSignedSMIMEAlgorithm", Num},
	{"RequireEncryptionSMIMEAlgorithm", Num},
	{"AllowSMIMEEncryptionAlgorithmNegotiation", Num},
	{"AllowSMIMESoftCerts", Bool},
	{"AllowBrowser", Bool},
	{"AllowConsumerEmail", Bool},
	{"AllowRemoteDesktop", Bool},
	{"AllowInternetSharing", Bool},
}

// known indexes Fields by name for O(1) validation.
var known = func() map[string]Field {
	m := make(map[string]Field, len(Fields))
	for _, f := range Fields {
		m[f.Name] = f
	}
	return m
}()

// valueRule is the value set [MS-ASPROV] 2.2.2 states for one numeric field: a
// closed range, or an explicit enumeration when set is non-empty.
type valueRule struct {
	min, max int
	set      []int
}

// allows reports whether v is one of the rule's values.
func (r valueRule) allows(v int) bool {
	if len(r.set) > 0 {
		return slices.Contains(r.set, v)
	}
	return v >= r.min && v <= r.max
}

// numericRules holds every numeric field whose [MS-ASPROV] 2.2.2 element page
// states a value set. A field absent here only has to be non-negative, because
// its page states no upper bound. The two truncation sizes admit -1, which the
// spec defines as "no truncation".
var numericRules = map[string]valueRule{
	"AllowBluetooth":                           {min: 0, max: 2},
	"MinDevicePasswordLength":                  {min: 1, max: 16},
	"MaxDevicePasswordFailedAttempts":          {min: 4, max: 16},
	"MinDevicePasswordComplexCharacters":       {min: 1, max: 4},
	"MaxCalendarAgeFilter":                     {set: []int{0, 4, 5, 6, 7}},
	"MaxEmailAgeFilter":                        {min: 0, max: 5},
	"RequireSignedSMIMEAlgorithm":              {min: 0, max: 1},
	"RequireEncryptionSMIMEAlgorithm":          {min: 0, max: 4},
	"AllowSMIMEEncryptionAlgorithmNegotiation": {min: 0, max: 2},
	"MaxEmailBodyTruncationSize":               {min: -1, max: math.MaxInt},
	"MaxEmailHTMLBodyTruncationSize":           {min: -1, max: math.MaxInt},
}

// numericValid reports whether v is a value the named numeric field may carry.
func numericValid(name string, v int) bool {
	if r, ok := numericRules[name]; ok {
		return r.allows(v)
	}
	return v >= 0
}

// Policy is a partial device policy: a field present in the map is enforced at its
// value; an absent field is not enforced and inherits the lower layer.
type Policy map[string]int

// Validate rejects a field name not in the canonical set (so an unknown key can never be
// stored and then silently ignored at provisioning) and an out-of-range value: a toggle
// must be 0 or 1, and a numeric field must be in the value set its [MS-ASPROV] element
// page states (numericRules), or non-negative where the page states none. A value
// outside it would reach a device in a document it may reject wholesale.
func (p Policy) Validate() error {
	for name, v := range p {
		f, ok := known[name]
		if !ok {
			return fmt.Errorf("unknown sync-policy field %q", name)
		}
		switch f.Kind {
		case Bool:
			if v != 0 && v != 1 {
				return fmt.Errorf("sync-policy field %q is a toggle: want 0 or 1, got %d", name, v)
			}
		case Num:
			if !numericValid(name, v) {
				return fmt.Errorf("sync-policy field %q is out of range: got %d", name, v)
			}
		}
	}
	return nil
}

// Merge resolves the global default beneath a mailbox override: the result holds every
// field set in either, with the override winning where both set the same field. Neither
// input is modified. A nil layer contributes nothing.
func Merge(base, override Policy) Policy {
	out := make(Policy, len(base)+len(override))
	maps.Copy(out, base)
	maps.Copy(out, override)
	return out
}

// Clone returns an independent copy, or nil for a nil/empty policy so storage does not
// persist an empty object.
func (p Policy) Clone() Policy {
	if len(p) == 0 {
		return nil
	}
	out := make(Policy, len(p))
	maps.Copy(out, p)
	return out
}

// Key returns a stable policy-generation token for p, a value that changes whenever the
// policy's content changes, so a device holding an old token can be detected as stale and
// forced to re-provision. An empty policy returns "1", the baseline token an unconfigured
// server has always issued, so existing devices are not churned until a policy is actually
// set. The token is a uint32 rendered as decimal, matching the 4-byte policy key the wire
// packs; 0 (no key) and 1 (the baseline) are reserved, so a non-empty policy never maps to
// them. Iteration follows the canonical field order, not Go's randomized map order, so the
// same policy always hashes the same.
func Key(p Policy) string {
	if len(p) == 0 {
		return "1"
	}
	var buf []byte
	for _, f := range Fields {
		if v, ok := p[f.Name]; ok {
			buf = append(buf, f.Name...)
			buf = append(buf, '=')
			buf = strconv.AppendInt(buf, int64(v), 10)
			buf = append(buf, ';')
		}
	}
	sum := fnv1a32(buf)
	if sum <= 1 {
		sum += 2
	}
	return strconv.FormatUint(uint64(sum), 10)
}

// fnv1a32 is 32-bit FNV-1a over b, the same function hash/fnv's New32a computes,
// written out so the key has no write call whose error would have to be discarded.
func fnv1a32(b []byte) uint32 {
	const offset, prime = 2166136261, 16777619
	h := uint32(offset)
	for _, c := range b {
		h ^= uint32(c)
		h *= prime
	}
	return h
}
