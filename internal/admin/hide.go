package admin

import (
	"net/http"
	"strconv"
	"strings"

	"hermex/internal/mapi"
)

// Address-book hide bits (the PR_ATTR_HIDDEN mask): which surfaces a user is
// hidden from. GAL and ANR are enforced by the NSPI layer today; AL and Delegate
// are stored faithfully and take effect when those address-book surfaces exist.
const (
	hideGAL      = 0x01
	hideAL       = 0x02
	hideDelegate = 0x04
	hideANR      = 0x08
)

// hideView is the "Hide user from..." control state: one bool per surface.
type hideView struct {
	GAL      bool
	AL       bool
	Delegate bool
	ANR      bool
}

// effectiveHideMask reads a user's hide mask from its stored properties: the
// PtLong mask form wins (parsed base-0), else the legacy boolean expands to
// "hidden from GAL and address lists" (0x03). Anything unparsable reads visible.
func effectiveHideMask(props map[uint32]string) uint32 {
	if s, ok := props[uint32(mapi.PrAttrHiddenMask)]; ok {
		if v, err := strconv.ParseUint(strings.TrimSpace(s), 0, 32); err == nil {
			return uint32(v)
		}
	}
	if s, ok := props[uint32(mapi.PrAttrHidden)]; ok {
		if v, err := strconv.ParseUint(strings.TrimSpace(s), 0, 32); err == nil && v != 0 {
			return 0x03
		}
	}
	return 0
}

// hideViewOf projects the stored hide mask into the four checkbox states.
func hideViewOf(props map[uint32]string) hideView {
	m := effectiveHideMask(props)
	return hideView{
		GAL:      m&hideGAL != 0,
		AL:       m&hideAL != 0,
		Delegate: m&hideDelegate != 0,
		ANR:      m&hideANR != 0,
	}
}

// hideMaskFromForm builds the hide mask from the four "Hide user from..."
// checkboxes.
func hideMaskFromForm(r *http.Request) uint32 {
	var m uint32
	if r.PostFormValue("hide_gal") != "" {
		m |= hideGAL
	}
	if r.PostFormValue("hide_al") != "" {
		m |= hideAL
	}
	if r.PostFormValue("hide_delegate") != "" {
		m |= hideDelegate
	}
	if r.PostFormValue("hide_anr") != "" {
		m |= hideANR
	}
	return m
}

// handleUIUserHide saves which address-book surfaces a user is hidden from,
// writing the whole PR_ATTR_HIDDEN mask (an empty mask clears the property), and
// returns the refreshed status panel.
func (s *Server) handleUIUserHide(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.uiAuthorized(w, r); !ok {
		return
	}
	val := ""
	if mask := hideMaskFromForm(r); mask != 0 {
		val = strconv.FormatUint(uint64(mask), 10)
	}
	found, err := s.dir.SetUserProperties(r.PathValue("email"),
		map[uint32]string{uint32(mapi.PrAttrHiddenMask): val})
	data := map[string]any{}
	switch {
	case err != nil:
		data["Error"] = "Could not save visibility: " + err.Error()
	case !found:
		data["Error"] = "No such user."
	default:
		data["Saved"] = true
	}
	s.render(w, "user-status", data)
}
