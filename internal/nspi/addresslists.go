package nspi

// Named address-list containers: recipient-type-defined address lists served
// alongside the Global Address List (container 0). Outlook selects one with
// STAT.container_id and browses its members, exactly as it browses the GAL.
//
// Container ids live in 0x3..0xF: above the table sentinels (BEGINNING=0x0,
// CURRENT=0x1, END=0x2) and below the first entry MId (midBase=0x10), so a
// container id can never collide with an entry MId — even though GetMatches
// copies cur_rec (which may be an entry MId) into container_id — nor with the
// PR_EMS_AB_MEMBER selector. 0x8..0xF stay reserved for future lists.
const (
	alContainerUsers     int32 = 0x3
	alContainerDistLists int32 = 0x4
	alContainerContacts  int32 = 0x5
	alContainerRooms     int32 = 0x6
	alContainerEquipment int32 = 0x7
)

// Recipient display types (users.display_type / PR_DISPLAY_TYPE_EX) that classify
// the named lists. They mirror the directory's display_type column, carried into
// each GAL entry as galUser.dispType.
const (
	rtUser      = 0 // DT_MAILUSER
	rtDistList  = 1 // DT_DISTLIST
	rtContact   = 6 // DT_REMOTE_MAILUSER (a mail contact)
	rtRoom      = 7 // DT_ROOM
	rtEquipment = 8 // DT_EQUIPMENT
)

// addressList is one named container: its STAT container id, display name, and
// the recipient display type its members carry.
type addressList struct {
	id       int32
	name     string
	dispType int
}

// addressLists is the registry of named, type-defined address lists. The order
// is the order they appear in GetSpecialTable. "All Contacts" is served here, but
// mail contacts (dtContact) are not yet creatable, so that list stays empty until
// the contacts subsystem lands — the container surface is complete now.
var addressLists = []addressList{
	{alContainerUsers, "All Users", rtUser},
	{alContainerDistLists, "All Distribution Lists", rtDistList},
	{alContainerContacts, "All Contacts", rtContact},
	{alContainerRooms, "All Rooms", rtRoom},
	{alContainerEquipment, "All Equipment", rtEquipment},
}

// addressListByID returns the named list a STAT container id selects, or false
// when the id is the GAL (0), the member selector, or anything else.
func addressListByID(containerID int32) (addressList, bool) {
	for _, al := range addressLists {
		if al.id == containerID {
			return al, true
		}
	}
	return addressList{}, false
}
