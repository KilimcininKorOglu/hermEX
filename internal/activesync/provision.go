package activesync

import (
	"net/http"

	"hermex/internal/objectstore"
	"hermex/internal/wbxml"
)

// provisionPolicyKey is the single policy key v1 issues. Provisioning is a
// formality here: the server requires no device policy, so the same key is
// handed out and never enforced on later commands.
const provisionPolicyKey = "1"

// handleProvision answers the two-phase EAS provisioning handshake. Phase one
// (the request carries no policy key, or key 0) returns a temporary key plus a
// permissive policy document; phase two (the device echoes the key) returns the
// final key. Both responses report Status 1. When a remote wipe is outstanding
// for the device, the response also carries the wipe directive, and the device's
// acknowledgement in the request advances the wipe to its completed state.
func (s *Server) handleProvision(w http.ResponseWriter, r *http.Request, sess *session) {
	root, err := readWBXML(r)
	if err != nil {
		http.Error(w, "invalid WBXML: "+err.Error(), http.StatusBadRequest)
		return
	}
	phaseOne := requestPolicyKey(root) == "" || requestPolicyKey(root) == "0"
	wipe := s.provisionWipe(sess, requestWipeAck(root))
	writeWBXML(w, provisionResponse(provisionPolicyKey, phaseOne, wipe))
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

// provisionResponse builds the Provision reply. When withDoc is set (phase one)
// it includes a permissive EAS provision document; when wipe is not wipeEmitNone
// it appends the corresponding empty remote-wipe element after the policies.
func provisionResponse(key string, withDoc bool, wipe int) *wbxml.Node {
	policy := []*wbxml.Node{
		wbxml.Str(wbxml.PVPolicyType, "MS-EAS-Provisioning-WBXML"),
		wbxml.Str(wbxml.PVStatus, "1"),
		wbxml.Str(wbxml.PVPolicyKey, key),
	}
	if withDoc {
		policy = append(policy, wbxml.Elem(wbxml.PVData,
			wbxml.Elem(wbxml.PVEASProvisionDoc,
				wbxml.Str(wbxml.PVDevicePasswordEnabled, "0"),
			)))
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
