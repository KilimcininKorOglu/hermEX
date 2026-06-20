package activesync

import (
	"net/http"
	"strconv"

	"hermex/internal/easpolicy"
	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// provisionToken maps each EASProvisionDoc policy field to its WBXML token. Every
// easpolicy.Field name must appear here; TestProvisionTokenCoverage guards that.
var provisionToken = map[string]wbxml.Tag{
	"DevicePasswordEnabled":                    wbxml.PVDevicePasswordEnabled,
	"AlphanumericDevicePasswordRequired":       wbxml.PVAlphanumericDevicePasswordRequired,
	"RequireStorageCardEncryption":             wbxml.PVRequireStorageCardEncryption,
	"PasswordRecoveryEnabled":                  wbxml.PVPasswordRecoveryEnabled,
	"AttachmentsEnabled":                       wbxml.PVAttachmentsEnabled,
	"MinDevicePasswordLength":                  wbxml.PVMinDevicePasswordLength,
	"MaxInactivityTimeDeviceLock":              wbxml.PVMaxInactivityTimeDeviceLock,
	"MaxDevicePasswordFailedAttempts":          wbxml.PVMaxDevicePasswordFailedAttempts,
	"MaxAttachmentSize":                        wbxml.PVMaxAttachmentSize,
	"AllowSimpleDevicePassword":                wbxml.PVAllowSimpleDevicePassword,
	"DevicePasswordExpiration":                 wbxml.PVDevicePasswordExpiration,
	"DevicePasswordHistory":                    wbxml.PVDevicePasswordHistory,
	"AllowStorageCard":                         wbxml.PVAllowStorageCard,
	"AllowCamera":                              wbxml.PVAllowCamera,
	"RequireDeviceEncryption":                  wbxml.PVRequireDeviceEncryption,
	"AllowUnsignedApplications":                wbxml.PVAllowUnsignedApplications,
	"AllowUnsignedInstallationPackages":        wbxml.PVAllowUnsignedInstallationPackages,
	"MinDevicePasswordComplexCharacters":       wbxml.PVMinDevicePasswordComplexCharacters,
	"AllowWiFi":                                wbxml.PVAllowWiFi,
	"AllowTextMessaging":                       wbxml.PVAllowTextMessaging,
	"AllowPOPIMAPEmail":                        wbxml.PVAllowPOPIMAPEmail,
	"AllowBluetooth":                           wbxml.PVAllowBluetooth,
	"AllowIrDA":                                wbxml.PVAllowIrDA,
	"RequireManualSyncWhenRoaming":             wbxml.PVRequireManualSyncWhenRoaming,
	"AllowDesktopSync":                         wbxml.PVAllowDesktopSync,
	"MaxCalendarAgeFilter":                     wbxml.PVMaxCalendarAgeFilter,
	"AllowHTMLEmail":                           wbxml.PVAllowHTMLEmail,
	"MaxEmailAgeFilter":                        wbxml.PVMaxEmailAgeFilter,
	"MaxEmailBodyTruncationSize":               wbxml.PVMaxEmailBodyTruncationSize,
	"MaxEmailHTMLBodyTruncationSize":           wbxml.PVMaxEmailHTMLBodyTruncationSize,
	"RequireSignedSMIMEMessages":               wbxml.PVRequireSignedSMIMEMessages,
	"RequireEncryptedSMIMEMessages":            wbxml.PVRequireEncryptedSMIMEMessages,
	"RequireSignedSMIMEAlgorithm":              wbxml.PVRequireSignedSMIMEAlgorithm,
	"RequireEncryptionSMIMEAlgorithm":          wbxml.PVRequireEncryptionSMIMEAlgorithm,
	"AllowSMIMEEncryptionAlgorithmNegotiation": wbxml.PVAllowSMIMEEncryptionAlgorithmNegotiation,
	"AllowSMIMESoftCerts":                      wbxml.PVAllowSMIMESoftCerts,
	"AllowBrowser":                             wbxml.PVAllowBrowser,
	"AllowConsumerEmail":                       wbxml.PVAllowConsumerEmail,
	"AllowRemoteDesktop":                       wbxml.PVAllowRemoteDesktop,
	"AllowInternetSharing":                     wbxml.PVAllowInternetSharing,
}

// defaultSyncPolicyProvider is the optional directory capability the Provision handler
// uses to read the server-wide default device policy; the concrete SQLDirectory
// satisfies it. The accounts interface stays minimal — only this handler needs it.
type defaultSyncPolicyProvider interface {
	GetDefaultSyncPolicy() (easpolicy.Policy, error)
}

// devicePolicy resolves the policy a device must be served: the server-wide default
// (when the directory provides one) with the mailbox's per-user override merged on top.
// A missing layer simply contributes nothing, so an unconfigured server serves no
// policy. Errors are swallowed to a less-restrictive policy rather than failing
// provisioning, which would lock the device out of mail entirely.
func (s *Server) devicePolicy(sess *session) easpolicy.Policy {
	var def easpolicy.Policy
	if p, ok := s.accounts.(defaultSyncPolicyProvider); ok {
		def, _ = p.GetDefaultSyncPolicy()
	}
	var override easpolicy.Policy
	if sess.mailbox != "" {
		if st, err := objectstore.Open(sess.mailbox); err == nil {
			override, _ = st.GetSyncPolicy()
			st.Close()
		}
	}
	return easpolicy.Merge(def, override)
}

// handleProvision answers the two-phase EAS provisioning handshake. Phase one
// (the request carries no policy key, or key 0) returns the policy key plus the
// policy document; phase two (the device echoes the key) returns the key alone.
// Both responses report Status 1. The key is the policy's generation token
// (easpolicy.Key) — it changes when the resolved policy changes, which is how a
// later command carrying a stale key is detected and forced to re-provision. When
// a remote wipe is outstanding the response also carries the wipe directive, and
// the device's acknowledgement in the request advances the wipe to completed.
func (s *Server) handleProvision(w http.ResponseWriter, r *http.Request, sess *session) {
	root, err := readWBXML(r)
	if err != nil {
		http.Error(w, "invalid WBXML: "+err.Error(), http.StatusBadRequest)
		return
	}
	phaseOne := requestPolicyKey(root) == "" || requestPolicyKey(root) == "0"
	wipe := s.provisionWipe(sess, requestWipeAck(root))
	merged := s.devicePolicy(sess)
	var policy easpolicy.Policy
	if phaseOne {
		policy = merged // the document rides only on phase one
	}
	writeWBXML(w, provisionResponse(easpolicy.Key(merged), phaseOne, wipe, policy))
}

// provisionWipe advances and reports the device's outstanding remote wipe for
// this Provision exchange (wipeEmitNone/Full/Account). A store error or a
// device-less request yields wipeEmitNone, so a transient failure never wipes a
// device prematurely.
func (s *Server) provisionWipe(sess *session, acked bool) int {
	if sess.req.deviceID == "" {
		return wipeEmitNone
	}
	st, err := objectstore.Open(sess.mailbox)
	if err != nil {
		return wipeEmitNone
	}
	defer st.Close()
	emit, err := advanceProvisionWipe(st, sess.req.deviceID, acked)
	if err != nil {
		return wipeEmitNone
	}
	return emit
}

// requestWipeAck reports whether the Provision request carries the device's
// acknowledgement of a remote wipe — a RemoteWipe or AccountOnlyRemoteWipe
// element, which the device sends once it has completed the wipe.
func requestWipeAck(root *wbxml.Node) bool {
	if root == nil {
		return false
	}
	return root.Child(wbxml.PVRemoteWipe) != nil || root.Child(wbxml.PVAccountOnlyRemoteWipe) != nil
}

// requestPolicyKey extracts Provision > Policies > Policy > PolicyKey, or "".
func requestPolicyKey(root *wbxml.Node) string {
	if root == nil {
		return ""
	}
	policies := root.Child(wbxml.PVPolicies)
	if policies == nil {
		return ""
	}
	policy := policies.Child(wbxml.PVPolicy)
	if policy == nil {
		return ""
	}
	return policy.ChildText(wbxml.PVPolicyKey)
}

// provisionResponse builds the Provision reply. When withDoc is set (phase one) it
// includes the EAS provision document carrying the resolved device policy — the fields
// set in pol, in canonical wire order; an empty policy yields the permissive
// DevicePasswordEnabled=0 document, matching the unconfigured default. When wipe is not
// wipeEmitNone it appends the corresponding empty remote-wipe element after the
// policies.
func provisionResponse(key string, withDoc bool, wipe int, pol easpolicy.Policy) *wbxml.Node {
	policy := []*wbxml.Node{
		wbxml.Str(wbxml.PVPolicyType, "MS-EAS-Provisioning-WBXML"),
		wbxml.Str(wbxml.PVStatus, "1"),
		wbxml.Str(wbxml.PVPolicyKey, key),
	}
	if withDoc {
		var doc []*wbxml.Node
		for _, f := range easpolicy.Fields { // canonical wire order
			if v, ok := pol[f.Name]; ok {
				doc = append(doc, wbxml.Str(provisionToken[f.Name], strconv.Itoa(v)))
			}
		}
		if len(doc) == 0 {
			doc = append(doc, wbxml.Str(wbxml.PVDevicePasswordEnabled, "0"))
		}
		policy = append(policy, wbxml.Elem(wbxml.PVData,
			wbxml.Elem(wbxml.PVEASProvisionDoc, doc...)))
	}
	children := []*wbxml.Node{
		wbxml.Str(wbxml.PVStatus, "1"),
		wbxml.Elem(wbxml.PVPolicies, wbxml.Elem(wbxml.PVPolicy, policy...)),
	}
	switch wipe {
	case wipeEmitFull:
		children = append(children, wbxml.Elem(wbxml.PVRemoteWipe))
	case wipeEmitAccount:
		children = append(children, wbxml.Elem(wbxml.PVAccountOnlyRemoteWipe))
	}
	return wbxml.Elem(wbxml.PVProvision, children...)
}
