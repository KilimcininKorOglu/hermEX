package objectstore

import (
	"embed"
	"fmt"

	"hermex/internal/migrate"
)

// Schema baselines for the two per-mailbox databases, stored in the object
// store's configurations table (config_id = cfgSchemaVersion) and the IMAP
// index's PRAGMA user_version. A store at or above its baseline is carried
// forward by the migration runner; one below the baseline is a pre-migration dev
// schema (disposable, never deployed) and is refused, as is one recorded newer
// than this binary.
const (
	objectSchemaVersion = 25
	indexSchemaVersion  = 5
)

//go:embed migrations/objects/*.sql
var objectMigrationFS embed.FS

//go:embed migrations/index/*.sql
var indexMigrationFS embed.FS

// objectMigrations and indexMigrations are the ordered schema histories loaded
// from the numbered .sql files. The lowest version is the baseline (the full
// schema, run only to create a fresh store); higher-numbered files are applied
// forward on open and run exactly once.
var (
	objectMigrations = migrate.MustLoadFS(objectMigrationFS, "migrations/objects")
	indexMigrations  = migrate.MustLoadFS(indexMigrationFS, "migrations/index")

	objectBaseline = baselineSteps(objectMigrations, objectSchemaVersion)
	indexBaseline  = baselineSteps(indexMigrations, indexSchemaVersion)
)

// baselineSteps returns the statements of the baseline migration, used to create
// a fresh database before the runner applies anything newer. A missing baseline
// is a build-time mistake, so it panics.
func baselineSteps(migs []migrate.Migration, version int) []string {
	for _, m := range migs {
		if m.Version == version {
			return m.Steps
		}
	}
	panic(fmt.Sprintf("objectstore: no baseline migration v%d", version))
}

// configurations config_id rows: store-wide counters and metadata on the object
// store root, stored bare.
const (
	cfgMailboxGUID       = 1
	cfgCurrentEID        = 2
	cfgMaximumEID        = 3
	cfgLastChangeNumber  = 4
	cfgLastArticleNumber = 5
	cfgLastCID           = 6
	cfgSearchState       = 7
	cfgDefaultPermission = 8
	cfgAnonymousPerm     = 9
	cfgSchemaVersion     = 10
	cfgMappingSignature  = 11
)
