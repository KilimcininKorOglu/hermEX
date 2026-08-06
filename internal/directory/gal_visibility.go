package directory

// Address-book hide bits, the PR_ATTR_HIDDEN mask an operator sets per user with
// the panel's "Hide user from..." switches. SearchGAL loads the mask into
// GALEntry.HiddenFrom but deliberately does not apply it, because each surface
// applies a different combination; the filters below are how a surface applies it.
const (
	HideFromGAL      uint32 = 0x01 // absent from the Global Address List
	HideFromAL       uint32 = 0x02 // absent from the typed address lists
	HideFromDelegate uint32 = 0x04 // not offered as a delegate
	HideFromResolve  uint32 = 0x08 // not found by ambiguous name resolution
)

// VisibleGAL returns the entries an address-book browse or search may show: those
// not hidden from the Global Address List. It matches what the address-book layer
// serves for a GAL browse, so a user hidden only from name resolution still
// appears in a directory listing.
func VisibleGAL(entries []GALEntry) []GALEntry {
	return filterHidden(entries, HideFromGAL)
}

// ResolvableGAL returns the entries a name-resolution query may answer with:
// those hidden from neither the Global Address List nor ambiguous name
// resolution. It matches what the address-book layer serves for a resolve, so an
// entry an operator withheld from auto-completion stays withheld here.
func ResolvableGAL(entries []GALEntry) []GALEntry {
	return filterHidden(entries, HideFromGAL|HideFromResolve)
}

// filterHidden drops every entry carrying any bit of mask. It returns a new slice
// so the caller's input is untouched, and nil stays nil.
func filterHidden(entries []GALEntry, mask uint32) []GALEntry {
	if entries == nil {
		return nil
	}
	out := make([]GALEntry, 0, len(entries))
	for _, e := range entries {
		if e.HiddenFrom&mask == 0 {
			out = append(out, e)
		}
	}
	return out
}
