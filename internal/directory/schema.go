package directory

import (
	"embed"

	"hermex/internal/migrate"
)

//go:embed migrations/*.sql
var directoryMigrationFS embed.FS

// directoryMigrations is the ordered schema history the migration runner applies,
// loaded from the numbered .sql files in migrations/. v1 is the current full
// schema: it is idempotent (CREATE TABLE IF NOT EXISTS plus ADD COLUMN IF NOT
// EXISTS), so adopting an already-populated database is a clean no-op that simply
// records v1. Every later schema change is added as a new numbered file and runs
// exactly once.
var directoryMigrations = migrate.MustLoadFS(directoryMigrationFS, "migrations")

// address_status packing: low nibble = user status, bits 4-5 = domain status.
// Only AF_USER_NORMAL may log in.
const (
	afUserNormal     = 0x00
	afUserSuspended  = 0x01
	afUserSharedMbox = 0x04
	afUserMask       = 0x0F
	afDomainMask     = 0x30
)

// display_type (PR_DISPLAY_TYPE_EX) values. dtMailuser is a normal mailbox user
// and login requires it; dtDistlist is a distribution list (a users row with no
// mailbox, expanded by the address book and the MTA). dtRoom/dtEquipment are
// resource mailboxes; dtContact is a mail contact (an external address with no
// mailbox). All five are address-book recipients and classify the named address
// lists (All Users/Distribution Lists/Contacts/Rooms/Equipment).
const (
	dtMailuser  = 0
	dtDistlist  = 1
	dtContact   = 6 // DT_REMOTE_MAILUSER
	dtRoom      = 7 // DT_ROOM
	dtEquipment = 8 // DT_EQUIPMENT
)
