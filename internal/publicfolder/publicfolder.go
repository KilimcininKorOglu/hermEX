// Package publicfolder routes callers to their own domain's public-folder store
// and lists the folders each caller may see. It is the single home of the
// tenant-isolation invariant for public folders: the domain a caller reaches is
// always derived from the authenticated caller's own address, never supplied by
// the caller, so no protocol surface (EWS, IMAP, webmail, admin) can be tricked
// into opening another domain's public tree. Every surface goes through this
// service rather than opening a public store directly.
//
// A per-domain public store mirrors the reference store model: each domain owns a
// distinct store under its home directory, and a domain "has" public folders
// exactly when that store has been provisioned. There is no separate runtime
// enable flag — provision implies enabled, un-provisioned implies absent.
package publicfolder

import (
	"errors"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// ErrStructuralFolder is returned by DeleteFolder when the target is one of the
// store's built-in skeleton folders (Root / IPM_SUBTREE / NON_IPM_SUBTREE / EFORMS
// REGISTRY), which must never be deleted. Only administrator-created folders, whose
// ids start at PublicFIDUnassignedStart, may be removed.
var ErrStructuralFolder = errors.New("publicfolder: cannot delete a structural folder")

// Paths supplies the per-domain public-store directory. *config.Config satisfies
// it via HomedirFor, the documented domain public-store directory.
type Paths interface {
	HomedirFor(domain string) string
}

// Service resolves callers to their domain's public-folder store. It holds no
// store handles; each call opens and closes a store, matching the rest of the
// codebase's per-operation store lifetime.
type Service struct {
	paths Paths
}

// New returns a Service resolving public stores through paths.
func New(paths Paths) *Service { return &Service{paths: paths} }

// Folder is a public folder a caller may see, carrying the caller's effective
// rights so a surface can decide read versus post without a second resolve.
type Folder struct {
	ID          int64
	DisplayName string
	Rights      uint32
}

// domainOf extracts the lowercased domain from an email address, or "" when the
// address carries no domain.
func domainOf(addr string) string {
	_, domain, _ := strings.Cut(strings.ToLower(addr), "@")
	return domain
}

// Provision ensures the domain's public store exists, creating and seeding it if
// absent. It is idempotent and is the administrator's enable action: after it the
// domain has public folders. domain is taken as given (an administrative input),
// not derived from a caller.
func (svc *Service) Provision(domain string) error {
	st, err := objectstore.OpenPublic(svc.paths.HomedirFor(strings.ToLower(domain)))
	if err != nil {
		return err
	}
	return st.Close()
}

// DirForCaller returns the public-store directory for the caller's own domain, or
// "" when the address carries no domain. It performs no I/O and does not report
// whether the store is provisioned — it is the tenant-isolation routing in a form a
// caller (e.g. the EWS store cache) can key its own handle cache on, while keeping
// the "domain comes from the authenticated caller" rule in this one place.
func (svc *Service) DirForCaller(callerEmail string) string {
	domain := domainOf(callerEmail)
	if domain == "" {
		return ""
	}
	return svc.paths.HomedirFor(domain)
}

// OpenForDomain opens an administrative handle on a domain's public store (read
// and write), without creating it. It returns ErrNotProvisioned when the domain
// has no public store. The caller owns the returned store and must Close it. This
// is for admin management, which addresses a domain directly; caller-facing
// surfaces use OpenForCaller so tenant isolation cannot be bypassed.
func (svc *Service) OpenForDomain(domain string) (*objectstore.Store, error) {
	return objectstore.OpenPublicExisting(svc.paths.HomedirFor(strings.ToLower(domain)))
}

// OpenForCaller opens the public store of the CALLER's own domain for reading,
// without creating it. ok is false (with a nil store and nil error) when the
// caller's domain has no public store, so a surface renders an empty result
// rather than failing. The domain is taken from callerEmail alone — the
// tenant-isolation chokepoint. The caller owns the returned store and must Close
// it when ok is true.
func (svc *Service) OpenForCaller(callerEmail string) (st *objectstore.Store, ok bool, err error) {
	domain := domainOf(callerEmail)
	if domain == "" {
		return nil, false, nil
	}
	st, err = objectstore.OpenPublicExisting(svc.paths.HomedirFor(domain))
	if errors.Is(err, objectstore.ErrNotProvisioned) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return st, true, nil
}

// VisibleFolders returns the public folders in the caller's own domain that the
// caller may see — those on which the caller's effective rights include
// FrightsVisible — each with those rights. It returns nil when the caller's
// domain has no public store. This is the shared discovery the EWS, IMAP, and
// webmail surfaces all render.
func (svc *Service) VisibleFolders(callerEmail string) ([]Folder, error) {
	st, ok, err := svc.OpenForCaller(callerEmail)
	if err != nil || !ok {
		return nil, err
	}
	defer st.Close()

	all, err := st.ListFolders()
	if err != nil {
		return nil, err
	}
	user := strings.ToLower(callerEmail)
	var out []Folder
	for _, f := range all {
		rights, err := st.ResolvePermission(f.ID, user)
		if err != nil {
			return nil, err
		}
		if rights&mapi.FrightsVisible == 0 {
			continue
		}
		out = append(out, Folder{ID: f.ID, DisplayName: f.DisplayName, Rights: rights})
	}
	return out, nil
}

// FolderWithGrants is the administrative view of a public folder: its identity and
// its full permission table (every member, not ACL-filtered for a caller).
type FolderWithGrants struct {
	ID          int64
	DisplayName string
	Grants      []objectstore.PermissionEntry
}

// Folders returns every public folder in a domain with its full permission table,
// for administrative management. It returns nil when the domain has no public store
// yet, never creating one (a management read must not provision as a side effect).
func (svc *Service) Folders(domain string) ([]FolderWithGrants, error) {
	st, err := svc.OpenForDomain(domain)
	if errors.Is(err, objectstore.ErrNotProvisioned) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer st.Close()

	folders, err := st.ListFolders()
	if err != nil {
		return nil, err
	}
	out := make([]FolderWithGrants, 0, len(folders))
	for _, f := range folders {
		grants, err := st.ListPermissions(f.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, FolderWithGrants{ID: f.ID, DisplayName: f.DisplayName, Grants: grants})
	}
	return out, nil
}

// CreateFolder provisions the domain's public store if absent and creates a folder
// named name directly under its IPM subtree, returning the new folder id. Creating
// the first folder is what enables a domain's public folders.
func (svc *Service) CreateFolder(domain, name string) (int64, error) {
	st, err := objectstore.OpenPublic(svc.paths.HomedirFor(strings.ToLower(domain)))
	if err != nil {
		return 0, err
	}
	defer st.Close()
	return st.CreateFolder(nil, name)
}

// DeleteFolder removes an administrator-created public folder by id. It refuses a
// structural id (below PublicFIDUnassignedStart) with ErrStructuralFolder so a
// request cannot delete the store's skeleton, and ErrNotProvisioned when the domain
// has no public store.
func (svc *Service) DeleteFolder(domain string, fid int64) error {
	if fid < int64(mapi.PublicFIDUnassignedStart) {
		return ErrStructuralFolder
	}
	st, err := svc.OpenForDomain(domain)
	if err != nil {
		return err
	}
	defer st.Close()
	return st.DeleteFolder(fid)
}

// Grant applies one permission change (an add/modify/remove the admin has already
// shaped) to a public folder. The store must already exist; it is never created by
// a grant.
func (svc *Service) Grant(domain string, fid int64, change objectstore.PermissionChange) error {
	st, err := svc.OpenForDomain(domain)
	if err != nil {
		return err
	}
	defer st.Close()
	return st.ModifyPermissions(fid, false, []objectstore.PermissionChange{change})
}
