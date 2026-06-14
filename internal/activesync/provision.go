package activesync

import (
	"net/http"

	"hermex/internal/wbxml"
)

// provisionPolicyKey is the single policy key v1 issues. Provisioning is a
// formality here: the server requires no device policy, so the same key is
// handed out and never enforced on later commands.
const provisionPolicyKey = "1"

// handleProvision answers the two-phase EAS provisioning handshake. Phase one
// (the request carries no policy key, or key 0) returns a temporary key plus a
// permissive policy document; phase two (the device echoes the key) returns the
// final key. Both responses report Status 1.
func (s *Server) handleProvision(w http.ResponseWriter, r *http.Request) {
	root, err := readWBXML(r)
	if err != nil {
		http.Error(w, "invalid WBXML: "+err.Error(), http.StatusBadRequest)
		return
	}
	phaseOne := requestPolicyKey(root) == "" || requestPolicyKey(root) == "0"
	writeWBXML(w, provisionResponse(provisionPolicyKey, phaseOne))
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
// it includes a permissive EAS provision document.
func provisionResponse(key string, withDoc bool) *wbxml.Node {
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
	return wbxml.Elem(wbxml.PVProvision,
		wbxml.Str(wbxml.PVStatus, "1"),
		wbxml.Elem(wbxml.PVPolicies, wbxml.Elem(wbxml.PVPolicy, policy...)),
	)
}
