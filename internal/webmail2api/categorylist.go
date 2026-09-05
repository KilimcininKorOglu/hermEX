package webmail2api

import (
	"encoding/json"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/oxcfg"
	"hermex/internal/oxcmail"
)

// The master category list is stored the way Outlook stores it: an FAI
// configuration message of class IPM.Configuration.CategoryList in the mailbox's
// Calendar folder, holding the [MS-OXOCFG] 2.2.5 XML in PR_ROAMING_XMLSTREAM.
// Both clients then read and write the same item, so a category renamed or
// recoloured in one is renamed or recoloured in the other.
//
// The colour travels as an Outlook palette index rather than a hex value, so a
// colour outside that palette snaps to the nearest entry. That is inherent to the
// interoperable format, not a shortcut taken here.

// readCategoryList reads the mailbox's master category list. A mailbox with no
// stored list yields nothing, which the caller turns into a seed.
func readCategoryList(st *objectstore.Store) (oxcfg.List, bool, error) {
	id, ok, err := st.FindAssociatedByClass(int64(mapi.PrivateFIDCalendar), oxcfg.CategoryListClass)
	if err != nil || !ok {
		return oxcfg.List{}, false, err
	}
	props, err := st.GetMessageProperties(id, mapi.PrRoamingXmlStream)
	if err != nil {
		return oxcfg.List{}, false, err
	}
	raw, _ := props.Get(mapi.PrRoamingXmlStream)
	b, _ := raw.([]byte)
	l, err := oxcfg.Decode(b)
	if err != nil {
		// A stream that will not parse is Outlook's, and overwriting it would
		// destroy a list the user still has there. Report it rather than seeding.
		return oxcfg.List{}, false, err
	}
	return l, true, nil
}

// writeCategoryList stores the list back into the configuration message,
// creating it when the mailbox has none.
func writeCategoryList(st *objectstore.Store, l oxcfg.List) error {
	body, err := oxcfg.Encode(l)
	if err != nil {
		return err
	}
	id, ok, err := st.FindAssociatedByClass(int64(mapi.PrivateFIDCalendar), oxcfg.CategoryListClass)
	if err != nil {
		return err
	}
	if ok {
		var props mapi.PropertyValues
		props.Set(mapi.PrRoamingXmlStream, body)
		return st.SetMessageProperties(id, props)
	}
	var props mapi.PropertyValues
	props.Set(mapi.PrMessageClass, oxcfg.CategoryListClass)
	props.Set(mapi.PrAssociated, true)
	props.Set(mapi.PrRoamingXmlStream, body)
	_, err = st.CreateMessage(int64(mapi.PrivateFIDCalendar), &oxcmail.Message{Props: props})
	return err
}

// categoriesFromList projects the stored list into the shape the SPA already
// speaks, resolving each palette index back to a hex colour.
func categoriesFromList(l oxcfg.List) []categoryJSON {
	out := make([]categoryJSON, 0, len(l.Categories))
	for _, c := range l.Categories {
		out = append(out, categoryJSON{Name: c.Name, Color: oxcfg.HexForPalette(c.Color)})
	}
	return out
}

// mergeCategories folds an edited list onto the stored one. A category matched by
// name keeps every attribute Outlook owns (its guid, shortcut and usage counts)
// and takes only the new colour; one the edit dropped is removed; a new one is
// appended. Rebuilding the list from the edit alone would hand Outlook a set of
// categories it has never seen and lose that bookkeeping on the first save.
func mergeCategories(stored oxcfg.List, edited []categoryJSON) oxcfg.List {
	byName := make(map[string]oxcfg.Category, len(stored.Categories))
	for _, c := range stored.Categories {
		byName[c.Name] = c
	}
	out := oxcfg.List{Default: stored.Default, LastSaved: stored.LastSaved}
	for _, e := range edited {
		if e.Name == "" {
			continue
		}
		c, ok := byName[e.Name]
		if !ok {
			c = oxcfg.Category{Name: e.Name}
		}
		c.Color = oxcfg.NearestPalette(e.Color)
		out.Categories = append(out.Categories, c)
	}
	return out
}

// seedCategoryList moves a mailbox's existing webmail-only category list into the
// interoperable one, once. Without it the first read after this change would show
// an empty list to a user who already had categories.
func seedCategoryList(st *objectstore.Store, m map[string]json.RawMessage) (oxcfg.List, error) {
	cats := []categoryJSON{}
	if raw, ok := m["categories"]; ok {
		_ = json.Unmarshal(raw, &cats)
	}
	l := mergeCategories(oxcfg.List{}, cats)
	if len(l.Categories) == 0 {
		return l, nil
	}
	return l, writeCategoryList(st, l)
}
